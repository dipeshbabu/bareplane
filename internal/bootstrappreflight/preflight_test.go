package bootstrappreflight

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/doctor"
	"github.com/dipeshbabu/bareplane/internal/topology"
)

func TestInspectPassesReadyMachinesInDeterministicOrder(t *testing.T) {
	configPath, keyPath := writePreflightConfig(t)
	knownHosts := filepath.Join(filepath.Dir(configPath), "known_hosts")
	var requests []Request
	report := Inspect(context.Background(), Options{
		ConfigPath: configPath,
		ResolveKnownHosts: func(path string) (string, error) {
			if path != configPath {
				t.Fatalf("trust resolver path = %q", path)
			}
			return knownHosts, nil
		},
		Runner: func(ctx context.Context, request Request) ([]byte, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("runner context has no deadline")
			}
			requests = append(requests, request)
			facts := readyFacts()
			facts.Hostname = request.Machine.Name
			facts.GPU = request.Machine.GPU
			return encodeFacts(facts), nil
		},
	})
	if report.HasFailures() || len(report.Results) != 2 {
		t.Fatalf("unexpected report %#v", report.Results)
	}
	if got := []string{report.Results[0].Name, report.Results[1].Name}; !reflect.DeepEqual(got, []string{"lab-control-1", "lab-worker-1"}) {
		t.Fatalf("result order = %#v", got)
	}
	for _, result := range report.Results {
		if result.Status != doctor.StatusPass || !strings.Contains(result.Message, "ready:") {
			t.Fatalf("unexpected result %#v", result)
		}
	}
	if len(requests) != 2 || requests[0].User != "debian" || requests[0].Port != 2222 || requests[0].PrivateKeyFile != keyPath || requests[0].KnownHostsFile != knownHosts {
		t.Fatalf("unexpected requests %#v", requests)
	}
}

func TestInspectRedactsAuthenticationFailureAndContinues(t *testing.T) {
	configPath, _ := writePreflightConfig(t)
	report := Inspect(context.Background(), testOptions(configPath, func(_ context.Context, request Request) ([]byte, error) {
		if request.Machine.Name == "lab-control-1" {
			return nil, errors.New("secret authentication detail")
		}
		facts := readyFacts()
		facts.Hostname = request.Machine.Name
		facts.GPU = request.Machine.GPU
		return encodeFacts(facts), nil
	}))
	if len(report.Results) != 2 || report.Results[0].Status != doctor.StatusFail || report.Results[1].Status != doctor.StatusPass {
		t.Fatalf("unexpected report %#v", report.Results)
	}
	if strings.Contains(report.Results[0].Message, "secret") || report.Results[0].Message != errRemoteFailed.Error() {
		t.Fatalf("runner error leaked: %q", report.Results[0].Message)
	}
}

func TestInspectBoundsEachRunner(t *testing.T) {
	configPath, _ := writePreflightConfig(t)
	report := Inspect(context.Background(), Options{
		ConfigPath:        configPath,
		Timeout:           5 * time.Millisecond,
		ResolveKnownHosts: func(string) (string, error) { return "known_hosts", nil },
		Runner: func(ctx context.Context, _ Request) ([]byte, error) {
			<-ctx.Done()
			return nil, nil
		},
	})
	for _, result := range report.Results {
		if result.Status != doctor.StatusFail || result.Message != errRemoteTimeout.Error() {
			t.Fatalf("unexpected timeout result %#v", result)
		}
	}
	called := false
	report = Inspect(context.Background(), Options{
		ConfigPath: configPath,
		Timeout:    MaximumTimeout + time.Second,
		Runner: func(context.Context, Request) ([]byte, error) {
			called = true
			return nil, nil
		},
	})
	if !report.HasFailures() || called {
		t.Fatalf("maximum timeout not enforced: called=%t report=%#v", called, report.Results)
	}
}

func TestInspectRejectsMissingTrustOrPrivateKeyBeforeRunning(t *testing.T) {
	configPath, keyPath := writePreflightConfig(t)
	called := false
	report := Inspect(context.Background(), Options{
		ConfigPath: configPath,
		ResolveKnownHosts: func(string) (string, error) {
			return "", errors.New("trust unavailable")
		},
		Runner: func(context.Context, Request) ([]byte, error) { called = true; return nil, nil },
	})
	if !report.HasFailures() || called || report.Results[0].Name != "ssh-known-hosts" {
		t.Fatalf("unexpected missing trust behavior: called=%t report=%#v", called, report.Results)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	report = Inspect(context.Background(), testOptions(configPath, func(context.Context, Request) ([]byte, error) {
		called = true
		return nil, nil
	}))
	if !report.HasFailures() || called || report.Results[0].Name != "ssh-private-key" {
		t.Fatalf("unexpected missing key behavior: called=%t report=%#v", called, report.Results)
	}
}

func TestResolvePrivateKeyRejectsSymlinkAndBroadPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file semantics are not available")
	}
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("secret-not-read"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePrivateKey(configPath, "link", nil); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePrivateKey(configPath, "target", nil); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("expected permission error, got %v", err)
	}
}

func TestEvaluateRejectsUnsafeHostState(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Facts)
		expected string
	}{
		{name: "sudo", mutate: func(f *Facts) { f.Sudo = false }, expected: "non-interactive sudo"},
		{name: "unsupported OS", mutate: func(f *Facts) { f.OSID = "alpine"; f.OSVersion = "3.22" }, expected: "unsupported OS"},
		{name: "architecture", mutate: func(f *Facts) { f.Architecture = "riscv64" }, expected: "unsupported architecture"},
		{name: "kernel", mutate: func(f *Facts) { f.Kernel = "5.9.19" }, expected: "kernel must be 5.10"},
		{name: "swap", mutate: func(f *Facts) { f.SwapBytes = 1 }, expected: "swap is enabled"},
		{name: "time", mutate: func(f *Facts) { f.TimeSynchronized = "unknown" }, expected: "time synchronization"},
		{name: "route", mutate: func(f *Facts) { f.DefaultInterface = "none" }, expected: "default route"},
		{name: "CPU", mutate: func(f *Facts) { f.CPUCount = 1 }, expected: "at least 2 CPU"},
		{name: "memory", mutate: func(f *Facts) { f.MemoryBytes = 1 }, expected: "at least 2 GiB"},
		{name: "disk", mutate: func(f *Facts) { f.DiskBytes = 1 }, expected: "at least 10 GiB"},
		{name: "CNI", mutate: func(f *Facts) { f.CNI = true }, expected: "existing Kubernetes or CNI"},
		{name: "PKI", mutate: func(f *Facts) { f.KubernetesPKI = true }, expected: "existing Kubernetes or CNI"},
		{name: "Kubernetes directory", mutate: func(f *Facts) { f.KubernetesDir = true }, expected: "existing Kubernetes or CNI"},
		{name: "cluster state", mutate: func(f *Facts) { f.ClusterState = true }, expected: "existing Kubernetes or CNI"},
		{name: "missing GPU", mutate: func(f *Facts) { f.GPU = false }, expected: "GPU intent"},
	}
	machine := topology.Machine{Name: "gpu", Role: "control-plane", CPU: 4, MemoryGB: 8, DiskGB: 64, GPU: true}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := readyFacts()
			facts.GPU = true
			test.mutate(&facts)
			result := evaluate(machine, facts, false)
			if result.Status != doctor.StatusFail || !strings.Contains(result.Message, test.expected) {
				t.Fatalf("unexpected result %#v", result)
			}
		})
	}
}

func TestEvaluateWarnsForExistingToolsCapacityAndUnexpectedGPU(t *testing.T) {
	facts := readyFacts()
	facts.Containerd, facts.Kubelet, facts.Kubeadm, facts.Kubectl, facts.GPU = true, true, true, true, true
	machine := topology.Machine{Name: "worker", Role: "worker", CPU: 32, MemoryGB: 64, DiskGB: 300}
	result := evaluate(machine, facts, false)
	if result.Status != doctor.StatusWarn {
		t.Fatalf("unexpected result %#v", result)
	}
	for _, expected := range []string{"containerd", "kubelet", "kubeadm", "kubectl", "CPU is below", "memory is below", "free disk is below", "GPU hardware"} {
		if !strings.Contains(result.Message, expected) {
			t.Fatalf("warning %q missing: %s", expected, result.Message)
		}
	}
}

func TestInspectRejectsDuplicateHostnames(t *testing.T) {
	configPath, _ := writePreflightConfig(t)
	report := Inspect(context.Background(), testOptions(configPath, func(_ context.Context, request Request) ([]byte, error) {
		facts := readyFacts()
		facts.Hostname = "duplicate"
		if request.Machine.Name == "lab-worker-1" {
			facts.Hostname = "Duplicate"
		}
		facts.GPU = true
		return encodeFacts(facts), nil
	}))
	for _, result := range report.Results {
		if result.Status != doctor.StatusFail || !strings.Contains(result.Message, "hostname is not unique") {
			t.Fatalf("unexpected duplicate hostname result %#v", result)
		}
	}
}

func TestParseFactsRejectsMalformedOrUnboundedOutput(t *testing.T) {
	valid := string(encodeFacts(readyFacts()))
	tests := []struct {
		name   string
		output []byte
	}{
		{name: "oversized", output: bytes.Repeat([]byte{'x'}, MaximumOutputSize+1)},
		{name: "header", output: []byte(strings.Replace(valid, protocolHeader, "wrong", 1))},
		{name: "missing", output: []byte(strings.Replace(valid, "sudo=true\n", "", 1))},
		{name: "duplicate", output: []byte(valid + "sudo=true\n")},
		{name: "unknown", output: []byte(valid + "unexpected=true\n")},
		{name: "integer", output: []byte(strings.Replace(valid, "cpu_count=16", "cpu_count=-1", 1))},
		{name: "boolean", output: []byte(strings.Replace(valid, "sudo=true", "sudo=yes", 1))},
		{name: "time", output: []byte(strings.Replace(valid, "time_synchronized=true", "time_synchronized=yes", 1))},
		{name: "unsafe text", output: []byte(strings.Replace(valid, "hostname=node", "hostname=node\r", 1))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseFacts(test.output); err == nil {
				t.Fatal("expected malformed output error")
			}
		})
	}
}

func TestSSHArgumentsEnforceProjectTrustAndDisableFallbacks(t *testing.T) {
	request := Request{Host: "2001:db8::10", Port: 2222, User: "debian", PrivateKeyFile: "/secret/key", KnownHostsFile: "/project/known_hosts"}
	args := sshArguments(request, 7)
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"BatchMode=yes", "IdentitiesOnly=yes", "StrictHostKeyChecking=yes", "UserKnownHostsFile=/project/known_hosts",
		"GlobalKnownHostsFile=", "PasswordAuthentication=no", "KbdInteractiveAuthentication=no", "PubkeyAuthentication=yes", "PreferredAuthentications=publickey",
		"ConnectionAttempts=1", "ConnectTimeout=7", "ClearAllForwardings=yes", "ForwardAgent=no", "ForwardX11=no",
		"PermitLocalCommand=no", "ControlMaster=no", "-i /secret/key", "-p 2222", "-l debian", "2001:db8::10 sh -s",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("SSH argument %q missing: %#v", expected, args)
		}
	}
	lowerArguments := strings.ToLower(joined)
	if strings.Contains(lowerArguments, "stricthostkeychecking=no") || strings.Contains(lowerArguments, "passwordauthentication=yes") {
		t.Fatalf("unsafe SSH fallback present: %#v", args)
	}
}

func TestSupportedHostMatrixAndKernelParsing(t *testing.T) {
	for _, pair := range [][2]string{{"debian", "12"}, {"debian", "13.1"}, {"ubuntu", "22.04"}, {"ubuntu", "24.04"}, {"ubuntu", "26.04"}} {
		if !supportedOS(pair[0], pair[1]) {
			t.Fatalf("supported OS rejected: %#v", pair)
		}
	}
	for _, pair := range [][2]string{{"debian", "11"}, {"ubuntu", "22.10"}, {"ubuntu", "24"}, {"rhel", "9"}} {
		if supportedOS(pair[0], pair[1]) {
			t.Fatalf("unsupported OS accepted: %#v", pair)
		}
	}
	for _, kernel := range []string{"5.10.0", "5.10.0-cloud", "6.1.0"} {
		if !kernelAtLeast(kernel, 5, 10) {
			t.Fatalf("supported kernel rejected: %q", kernel)
		}
	}
	for _, kernel := range []string{"", "5", "5.9.99", "x.10", "4.18.0"} {
		if kernelAtLeast(kernel, 5, 10) {
			t.Fatalf("unsupported kernel accepted: %q", kernel)
		}
	}
}

func TestRemoteFactsScriptContainsNoMutationWorkflow(t *testing.T) {
	for _, forbidden := range []string{"apt install", "apt-get", "swapoff", "systemctl restart", "systemctl enable", "kubeadm init", "kubeadm join", "mkdir ", "rm -", "chmod ", "chown "} {
		if strings.Contains(remoteFactsScript, forbidden) {
			t.Fatalf("remote facts script contains mutating command %q", forbidden)
		}
	}
}

func testOptions(configPath string, runner Runner) Options {
	return Options{
		ConfigPath:        configPath,
		Runner:            runner,
		ResolveKnownHosts: func(string) (string, error) { return "known_hosts", nil },
	}
}

func writePreflightConfig(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	keyPath := filepath.Join(root, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("private-key-material-not-read"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Metadata.Name = "lab"
	cfg.Spec.Nodes = []config.NodeGroup{
		{Name: "worker", Role: "worker", Count: 1, CPU: 8, MemoryGB: 16, DiskGB: 128, GPU: true},
		{Name: "control", Role: "control-plane", Count: 1, CPU: 4, MemoryGB: 8, DiskGB: 64},
	}
	cfg.Spec.Bootstrap = &config.BootstrapConfig{SSH: &config.SSHBootstrap{
		User: "debian", PrivateKeyFile: "id_ed25519", Port: 2222,
		Hosts: map[string]string{"lab-control-1": "10.0.0.10", "lab-worker-1": "2001:db8::20"},
	}}
	cfg.Spec.Kubernetes = &config.KubernetesConfig{
		Version: "1.36.4", APIVIP: "192.168.1.100", PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12",
		KubeVIPVersion: "1.2.3", CiliumVersion: "1.20.1", KubeProxyReplacement: true,
	}
	var encoded bytes.Buffer
	if err := config.Encode(&encoded, cfg); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "bareplane.yaml")
	if err := os.WriteFile(configPath, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	absoluteKey, err := filepath.Abs(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return configPath, absoluteKey
}

func readyFacts() Facts {
	const gib = 1024 * 1024 * 1024
	return Facts{
		OSID: "debian", OSVersion: "12", Architecture: "x86_64", Kernel: "6.1.0", Hostname: "node",
		DefaultInterface: "eth0", CPUCount: 16, MemoryBytes: 32 * gib, DiskBytes: 256 * gib,
		TimeSynchronized: "true", Sudo: true,
	}
}

func encodeFacts(facts Facts) []byte {
	boolean := func(value bool) string { return strconv.FormatBool(value) }
	lines := []string{
		protocolHeader,
		"os_id=" + facts.OSID,
		"os_version=" + facts.OSVersion,
		"arch=" + facts.Architecture,
		"kernel=" + facts.Kernel,
		"hostname=" + facts.Hostname,
		"default_interface=" + facts.DefaultInterface,
		"cpu_count=" + strconv.FormatUint(facts.CPUCount, 10),
		"memory_bytes=" + strconv.FormatUint(facts.MemoryBytes, 10),
		"disk_bytes=" + strconv.FormatUint(facts.DiskBytes, 10),
		"swap_bytes=" + strconv.FormatUint(facts.SwapBytes, 10),
		"time_synchronized=" + facts.TimeSynchronized,
		"sudo=" + boolean(facts.Sudo),
		"containerd=" + boolean(facts.Containerd),
		"kubelet=" + boolean(facts.Kubelet),
		"kubeadm=" + boolean(facts.Kubeadm),
		"kubectl=" + boolean(facts.Kubectl),
		"cni=" + boolean(facts.CNI),
		"kubernetes_pki=" + boolean(facts.KubernetesPKI),
		"kubernetes_dir=" + boolean(facts.KubernetesDir),
		"cluster_state=" + boolean(facts.ClusterState),
		"gpu=" + boolean(facts.GPU),
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}
