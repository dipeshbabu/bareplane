package bootstrapapply

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunnerAcceptsOnlyOwnedPhasesAndControlledArguments(t *testing.T) {
	request := Request{Phase: "join", BundleDir: "/project 'quoted' %h/bootstrap", PrivateKeyFile: "/private/key %h", KnownHostsFile: "/project/state/known_hosts"}
	args, err := phaseArguments(request)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--inventory", filepath.Join(request.BundleDir, "inventory.yaml"), "--private-key", "/private/key %%h", "join.yaml"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	request.PrivateKeyFile = `/private/"quoted"\%h`
	escaped, err := phaseArguments(request)
	if err != nil || escaped[3] != `/private/\"quoted\"\\%%h` {
		t.Fatalf("IdentityFile quoting was not preserved: %#v %v", escaped, err)
	}
	for _, phase := range []string{"site", "../../custom", "join.yaml", "join; reset", "--extra-vars"} {
		request.Phase = phase
		if _, err := phaseArguments(request); err == nil {
			t.Fatalf("unowned phase %q accepted", phase)
		}
	}
}

func TestRunnerDropsEnvironmentOverridesAndProviderCredentials(t *testing.T) {
	request := Request{BundleDir: "/owned/bootstrap", StateDir: "/owned/state"}
	env := controlledEnvironment([]string{"PATH=/bin", "HOME=/controller", "USER=operator", "KUBECONFIG=/global/config",
		"ANSIBLE_CONFIG=/unowned", "ANSIBLE_DEBUG=True", "ANSIBLE_CALLBACK_PLUGINS=/unowned", "PYTHONPATH=/unowned",
		"SSH_AUTH_SOCK=/agent", "PROXMOX_API_TOKEN=SECRET", "HTTP_PROXY=SECRET"}, request)
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"SECRET", "/unowned", "KUBECONFIG", "SSH_AUTH_SOCK", "PYTHONPATH"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("unsafe environment survived: %s", forbidden)
		}
	}
	for _, required := range []string{"PATH=/bin", "HOME=/controller", "ANSIBLE_HOST_KEY_CHECKING=True", "ANSIBLE_DEBUG=False", "ANSIBLE_STDOUT_CALLBACK=default"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing environment control %s", required)
		}
	}
}

func TestControllerToolVersionPins(t *testing.T) {
	for _, test := range []struct {
		tool, output string
		valid        bool
	}{
		{"ansible-playbook", "ansible-playbook [core 2.19.9]\n", true},
		{"ansible-playbook", "ansible-playbook [core 2.20.0]\n", false},
		{"openssl", "OpenSSL 3.0.13", true}, {"openssl", "LibreSSL 3.0.0", false},
		{"kubectl", `{"clientVersion":{"gitVersion":"v1.36.4"}}`, true},
		{"kubectl", `{"clientVersion":{"gitVersion":"v1.36.3"}}`, false},
		{"kubectl", "malformed", false},
	} {
		if got := supportedToolVersion(test.tool, []byte(test.output), "1.36.4"); got != test.valid {
			t.Fatalf("version %s: %v", test.tool, got)
		}
	}
}

func TestLogsAreBoundedWithoutReturningSecretOutput(t *testing.T) {
	var output bytes.Buffer
	writer := &limitedLog{writer: &output, remaining: 4}
	for _, value := range []string{"123456", "private overflow"} {
		if written, err := writer.Write([]byte(value)); err != nil || written != len(value) {
			t.Fatalf("write = %d %v", written, err)
		}
	}
	if output.String() != "1234" || writer.remaining != 0 {
		t.Fatalf("unbounded log: %q", output.String())
	}
}

func TestControlledAnsibleIntegration(t *testing.T) {
	binary, err := exec.LookPath("ansible-playbook")
	if err != nil {
		if os.Getenv("BAREPLANE_TEST_ANSIBLE") == "1" {
			t.Fatal(err)
		}
		t.Skip("ansible-playbook is not installed")
	}
	f := newFixture(t)
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	request := Request{Phase: "health", BundleDir: f.bundle, StateDir: filepath.Dir(f.trust), PrivateKeyFile: f.key, KnownHostsFile: f.trust, Log: &output}
	err = commandRunner(binary)(ctx, request)
	if err == nil || ctx.Err() != nil || !strings.Contains(output.String(), "Private kubeconfig is unavailable") || strings.Contains(output.String(), "UNREACHABLE") {
		t.Fatalf("controlled controller-only refusal failed: %v\n%s", err, output.String())
	}
	if strings.Contains(output.String(), "PRIVATE-FIXTURE-NOT-A-REAL-KEY") {
		t.Fatal("private key leaked")
	}
	if _, _, err := verifyBundle(f.path, f.cfg); err != nil {
		t.Fatalf("Ansible changed the owned bundle: %v", err)
	}
}

func TestControlledSSHArgumentIntegration(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Ansible controller is Linux-only")
	}
	binary, err := exec.LookPath("ansible-playbook")
	if err != nil {
		if os.Getenv("BAREPLANE_TEST_ANSIBLE") == "1" {
			t.Fatal(err)
		}
		t.Skip("ansible-playbook is not installed")
	}
	f := newFixture(t)
	original := filepath.Dir(f.path)
	directory := original + ` "double"`
	if err := os.Rename(original, directory); err != nil {
		t.Fatal(err)
	}
	f.path = filepath.Join(directory, "bareplane.yaml")
	f.bundle = filepath.Join(directory, ".bareplane", "bootstrap")
	f.key = filepath.Join(directory, "private-key")
	f.trust = filepath.Join(directory, ".bareplane", "state", "bootstrap", "known_hosts")
	bin := t.TempDir()
	trace := filepath.Join(bin, "ssh-arguments.json")
	encoded, _ := json.Marshal(trace)
	script := "#!/usr/bin/python3\nimport json,sys\nwith open(" + string(encoded) + ", 'w') as output: json.dump(sys.argv[1:], output)\nsys.exit(255)\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	err = commandRunner(binary)(ctx, Request{Phase: "host_prepare", BundleDir: f.bundle, StateDir: filepath.Dir(f.trust),
		PrivateKeyFile: f.key, KnownHostsFile: f.trust, Log: &output})
	if err == nil || ctx.Err() != nil {
		t.Fatalf("fake SSH did not stop the phase safely: %v", err)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("SSH was not invoked: %v\n%s", err, output.String())
	}
	var args []string
	if err := json.Unmarshal(data, &args); err != nil {
		t.Fatal(err)
	}
	wantTrust := "UserKnownHostsFile=" + strconv.Quote(strings.ReplaceAll(f.bundle+"/../state/bootstrap/known_hosts", "%", "%%"))
	wantKey := "IdentityFile=" + strconv.Quote(strings.ReplaceAll(f.key, "%", "%%"))
	for _, want := range []string{wantTrust, wantKey} {
		found := false
		for _, arg := range args {
			if arg == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("SSH argument %q missing from %#v", want, args)
		}
	}
}

func TestControlledArgoModuleRefusalIntegration(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Argo controller is Linux-only")
	}
	binary, err := exec.LookPath("ansible-playbook")
	if err != nil {
		if os.Getenv("BAREPLANE_TEST_ANSIBLE") == "1" {
			t.Fatal(err)
		}
		t.Skip("ansible-playbook is not installed")
	}
	f := newFixture(t)
	// Isolate module dependency packaging and its own approval boundary in an
	// owned temporary fixture. No kubeconfig, Git request, or cluster is accessed.
	playbook := `- hosts: localhost
  connection: local
  gather_facts: false
  tasks:
    - bareplane_argocd:
        kubeconfig: /unavailable/admin.conf
        cluster: lab
        vip: 192.0.2.100
        version: 1.36.4
        approved: false
        repository: https://git.example.com/team/repo.git
        revision: main
        root_path: clusters/lab
        input_dir: /unavailable/argocd-input
        contract: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`
	if err := os.WriteFile(filepath.Join(f.bundle, "argocd.yaml"), []byte(playbook), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Dir(f.trust)
	var output bytes.Buffer
	request := Request{Phase: "argocd", BundleDir: f.bundle, StateDir: state, PrivateKeyFile: f.key, KnownHostsFile: f.trust, Log: &output,
		Argo: &ArgoRequest{Contract: strings.Repeat("a", 64), PayloadDir: filepath.Join(state, "argocd-input")}}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := commandRunner(binary)(ctx, request); err == nil || ctx.Err() != nil || !strings.Contains(output.String(), "requires explicit approval") || strings.Contains(output.String(), "Traceback") {
		t.Fatalf("controlled Argo module refusal failed: %v\n%s", err, output.String())
	}
}

func TestControlledHandoffModuleRefusalIntegration(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("handoff controller is Linux-only")
	}
	binary, err := exec.LookPath("ansible-playbook")
	if err != nil {
		if os.Getenv("BAREPLANE_TEST_ANSIBLE") == "1" {
			t.Fatal(err)
		}
		t.Skip("ansible-playbook is not installed")
	}
	f := newFixture(t)
	playbook := `- hosts: localhost
  connection: local
  gather_facts: false
  tasks:
    - bareplane_handoff:
        kubeconfig: /unavailable/admin.conf
        cluster: lab
        vip: 192.0.2.100
        version: 1.36.4
        approved: false
        repository: https://git.example.com/team/repo.git
        revision: main
        root_path: clusters/lab
        input_dir: /unavailable/handoff-input
        contract: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
        argo_contract: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
`
	if err := os.WriteFile(filepath.Join(f.bundle, "handoff.yaml"), []byte(playbook), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Dir(f.trust)
	var output bytes.Buffer
	request := Request{Phase: "handoff", BundleDir: f.bundle, StateDir: state, PrivateKeyFile: f.key, KnownHostsFile: f.trust, Log: &output,
		Argo: &ArgoRequest{Handoff: true, Contract: strings.Repeat("a", 64), HandoffContract: strings.Repeat("b", 64), PayloadDir: filepath.Join(state, "handoff-input")}}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := commandRunner(binary)(ctx, request); err == nil || ctx.Err() != nil || !strings.Contains(output.String(), "requires explicit approval") || strings.Contains(output.String(), "Traceback") {
		t.Fatalf("controlled handoff module refusal failed: %v\n%s", err, output.String())
	}
}
