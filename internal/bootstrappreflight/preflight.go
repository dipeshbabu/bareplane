package bootstrappreflight

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/doctor"
	"github.com/dipeshbabu/bareplane/internal/sshtrust"
	"github.com/dipeshbabu/bareplane/internal/topology"
)

const (
	DefaultTimeout    = 15 * time.Second
	MaximumTimeout    = 60 * time.Second
	MaximumOutputSize = 32 * 1024
	protocolHeader    = "BAREPLANE_PREFLIGHT_V1"
)

var (
	errRemoteFailed   = errors.New("SSH authentication or remote preflight failed")
	errRemoteTimeout  = errors.New("SSH remote preflight timed out")
	errOutputTooLarge = errors.New("SSH remote preflight exceeded the output limit")
)

// Request contains only validated connection metadata and local path references.
type Request struct {
	Machine        topology.Machine
	Host           string
	Port           int
	User           string
	PrivateKeyFile string
	KnownHostsFile string
}

// Runner performs one authenticated, read-only remote facts collection.
type Runner func(context.Context, Request) ([]byte, error)

// UserHomeDirFunc resolves a leading ~/ in the configured private-key path.
type UserHomeDirFunc func() (string, error)

// TrustResolver verifies project-scoped SSH host trust and returns its path.
type TrustResolver func(string) (string, error)

// Options controls preflight dependencies and per-machine resource bounds.
type Options struct {
	ConfigPath        string
	Timeout           time.Duration
	Runner            Runner
	UserHomeDir       UserHomeDirFunc
	ResolveKnownHosts TrustResolver
}

// Facts is the bounded, parsed host state used for readiness classification.
type Facts struct {
	OSID             string
	OSVersion        string
	Architecture     string
	Kernel           string
	Hostname         string
	DefaultInterface string
	CPUCount         uint64
	MemoryBytes      uint64
	DiskBytes        uint64
	SwapBytes        uint64
	TimeSynchronized string
	Sudo             bool
	Containerd       bool
	Kubelet          bool
	Kubeadm          bool
	Kubectl          bool
	CNI              bool
	KubernetesPKI    bool
	KubernetesDir    bool
	ClusterState     bool
	GPU              bool
}

type machineObservation struct {
	machine topology.Machine
	facts   Facts
	err     error
}

// Inspect authenticates to every desired machine and evaluates only read-only facts.
func Inspect(ctx context.Context, options Options) doctor.Report {
	if ctx == nil {
		return failedReport("bootstrap-preflight", "preflight context is nil")
	}
	if strings.TrimSpace(options.ConfigPath) == "" {
		options.ConfigPath = "bareplane.yaml"
	}
	if options.Timeout <= 0 {
		options.Timeout = DefaultTimeout
	}
	if options.Timeout > MaximumTimeout {
		return failedReport("bootstrap-preflight", fmt.Sprintf("preflight timeout must not exceed %s", MaximumTimeout))
	}

	cfg, err := loadConfig(options.ConfigPath)
	if err != nil {
		return failedReport("bootstrap-config", err.Error())
	}
	resolveTrust := options.ResolveKnownHosts
	if resolveTrust == nil {
		resolveTrust = sshtrust.RequireKnownHosts
	}
	knownHosts, err := resolveTrust(options.ConfigPath)
	if err != nil {
		return failedReport("ssh-known-hosts", err.Error())
	}
	privateKey, err := resolvePrivateKey(options.ConfigPath, cfg.Spec.Bootstrap.SSH.PrivateKeyFile, options.UserHomeDir)
	if err != nil {
		return failedReport("ssh-private-key", err.Error())
	}
	runner := options.Runner
	if runner == nil {
		binary, err := exec.LookPath("ssh")
		if err != nil {
			return failedReport("ssh", "ssh executable was not found")
		}
		runner = commandRunner(binary)
	}

	topo, err := topology.Build(cfg)
	if err != nil {
		return failedReport("topology", err.Error())
	}
	machines := append([]topology.Machine(nil), topo.Machines...)
	sort.Slice(machines, func(i, j int) bool { return machines[i].Name < machines[j].Name })
	observations := make([]machineObservation, 0, len(machines))
	port := cfg.Spec.Bootstrap.SSH.EffectivePort()
	for _, machine := range machines {
		if err := ctx.Err(); err != nil {
			observations = append(observations, machineObservation{machine: machine, err: err})
			continue
		}
		request := Request{
			Machine:        machine,
			Host:           cfg.Spec.Bootstrap.SSH.Hosts[machine.Name],
			Port:           port,
			User:           cfg.Spec.Bootstrap.SSH.User,
			PrivateKeyFile: privateKey,
			KnownHostsFile: knownHosts,
		}
		machineCtx, cancel := context.WithTimeout(ctx, options.Timeout)
		output, runErr := runner(machineCtx, request)
		contextErr := machineCtx.Err()
		cancel()
		if runErr != nil || contextErr != nil {
			observations = append(observations, machineObservation{machine: machine, err: safeRunnerError(runErr, contextErr)})
			continue
		}
		facts, parseErr := parseFacts(output)
		observations = append(observations, machineObservation{machine: machine, facts: facts, err: parseErr})
	}

	hostnameCounts := make(map[string]int)
	for _, observation := range observations {
		if observation.err == nil {
			hostnameCounts[strings.ToLower(observation.facts.Hostname)]++
		}
	}
	results := make([]doctor.Result, 0, len(observations))
	for _, observation := range observations {
		if observation.err != nil {
			results = append(results, doctor.Result{Name: observation.machine.Name, Status: doctor.StatusFail, Message: observation.err.Error()})
			continue
		}
		results = append(results, evaluate(observation.machine, observation.facts, hostnameCounts[strings.ToLower(observation.facts.Hostname)] > 1))
	}
	return doctor.Report{Results: results}
}

func failedReport(name, message string) doctor.Report {
	return doctor.Report{Results: []doctor.Result{{Name: name, Status: doctor.StatusFail, Message: message}}}
}

func loadConfig(path string) (config.Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return config.Config{}, fmt.Errorf("open configuration: %w", err)
	}
	cfg, loadErr := config.Load(file)
	closeErr := file.Close()
	if loadErr != nil {
		return config.Config{}, loadErr
	}
	if closeErr != nil {
		return config.Config{}, fmt.Errorf("close configuration: %w", closeErr)
	}
	if err := cfg.ValidateKubernetesBootstrap(); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func resolvePrivateKey(configPath, configured string, homeDir UserHomeDirFunc) (string, error) {
	path := configured
	if strings.HasPrefix(path, "~/") {
		if homeDir == nil {
			homeDir = os.UserHomeDir
		}
		home, err := homeDir()
		if err != nil {
			return "", fmt.Errorf("resolve configured private key: %w", err)
		}
		path = filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(path, "~/")))
	} else if strings.HasPrefix(path, "~") {
		return "", errors.New("resolve configured private key: only ~/ home-relative paths are supported")
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(filepath.Clean(configPath)), filepath.FromSlash(path))
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve configured private key: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", errors.New("configured private key file is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("configured private key must be a regular file and not a symlink")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("configured private key permissions must be 0600 or stricter")
	}
	return path, nil
}

func safeRunnerError(runErr, contextErr error) error {
	if errors.Is(runErr, errOutputTooLarge) {
		return errOutputTooLarge
	}
	if errors.Is(runErr, errRemoteTimeout) || errors.Is(contextErr, context.DeadlineExceeded) {
		return errRemoteTimeout
	}
	if errors.Is(contextErr, context.Canceled) {
		return context.Canceled
	}
	return errRemoteFailed
}

func commandRunner(binary string) Runner {
	return func(ctx context.Context, request Request) ([]byte, error) {
		timeoutSeconds := 1
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining > 0 {
				timeoutSeconds = int((remaining + time.Second - 1) / time.Second)
			}
		}
		var stdout boundedBuffer
		stdout.limit = MaximumOutputSize
		command := exec.CommandContext(ctx, binary, sshArguments(request, timeoutSeconds)...)
		command.Stdin = strings.NewReader(remoteFactsScript)
		command.Stdout = &stdout
		command.Stderr = io.Discard
		err := command.Run()
		if ctx.Err() != nil {
			return nil, errRemoteTimeout
		}
		if stdout.overflow {
			return nil, errOutputTooLarge
		}
		if err != nil {
			return nil, errRemoteFailed
		}
		return bytes.Clone(stdout.Bytes()), nil
	}
}

func sshArguments(request Request, timeoutSeconds int) []string {
	nullDevice := "/dev/null"
	if runtime.GOOS == "windows" {
		nullDevice = "NUL"
	}
	return []string{
		"-T",
		"-F", nullDevice,
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + request.KnownHostsFile,
		"-o", "GlobalKnownHostsFile=" + nullDevice,
		"-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "PubkeyAuthentication=yes",
		"-o", "PreferredAuthentications=publickey",
		"-o", "ConnectionAttempts=1",
		"-o", "ConnectTimeout=" + strconv.Itoa(timeoutSeconds),
		"-o", "ClearAllForwardings=yes",
		"-o", "ForwardAgent=no",
		"-o", "ForwardX11=no",
		"-o", "PermitLocalCommand=no",
		"-o", "ControlMaster=no",
		"-o", "LogLevel=ERROR",
		"-i", request.PrivateKeyFile,
		"-p", strconv.Itoa(request.Port),
		"-l", request.User,
		request.Host,
		"sh", "-s",
	}
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.Len()
	if remaining < len(data) {
		b.overflow = true
		if remaining < 0 {
			remaining = 0
		}
		data = data[:remaining]
	}
	if len(data) > 0 {
		_, _ = b.Buffer.Write(data)
	}
	return original, nil
}

func parseFacts(output []byte) (Facts, error) {
	if len(output) > MaximumOutputSize {
		return Facts{}, errOutputTooLarge
	}
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	if len(lines) == 0 || lines[0] != protocolHeader {
		return Facts{}, errors.New("remote preflight returned an invalid protocol header")
	}
	values := make(map[string]string, len(lines)-1)
	for _, line := range lines[1:] {
		if len(line) == 0 || len(line) > 512 || strings.ContainsRune(line, '\r') {
			return Facts{}, errors.New("remote preflight returned a malformed fact")
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return Facts{}, errors.New("remote preflight returned a malformed fact")
		}
		if _, duplicate := values[key]; duplicate {
			return Facts{}, errors.New("remote preflight returned a duplicate fact")
		}
		values[key] = value
	}

	required := []string{"os_id", "os_version", "arch", "kernel", "hostname", "default_interface", "cpu_count", "memory_bytes", "disk_bytes", "swap_bytes", "time_synchronized", "sudo", "containerd", "kubelet", "kubeadm", "kubectl", "cni", "kubernetes_pki", "kubernetes_dir", "cluster_state", "gpu"}
	if len(values) != len(required) {
		return Facts{}, errors.New("remote preflight returned an incomplete fact set")
	}
	for _, key := range required {
		if _, ok := values[key]; !ok {
			return Facts{}, errors.New("remote preflight returned an incomplete fact set")
		}
	}

	var facts Facts
	facts.OSID = values["os_id"]
	facts.OSVersion = values["os_version"]
	facts.Architecture = values["arch"]
	facts.Kernel = values["kernel"]
	facts.Hostname = values["hostname"]
	facts.DefaultInterface = values["default_interface"]
	if !safeText(facts.OSID) || !safeText(facts.OSVersion) || !safeText(facts.Architecture) || !safeText(facts.Kernel) || !safeText(facts.Hostname) || !safeText(facts.DefaultInterface) {
		return Facts{}, errors.New("remote preflight returned an unsafe text fact")
	}
	var err error
	if facts.CPUCount, err = parseUintFact(values, "cpu_count"); err != nil {
		return Facts{}, err
	}
	if facts.MemoryBytes, err = parseUintFact(values, "memory_bytes"); err != nil {
		return Facts{}, err
	}
	if facts.DiskBytes, err = parseUintFact(values, "disk_bytes"); err != nil {
		return Facts{}, err
	}
	if facts.SwapBytes, err = parseUintFact(values, "swap_bytes"); err != nil {
		return Facts{}, err
	}
	if values["time_synchronized"] != "true" && values["time_synchronized"] != "false" && values["time_synchronized"] != "unknown" {
		return Facts{}, errors.New("remote preflight returned an invalid time synchronization fact")
	}
	facts.TimeSynchronized = values["time_synchronized"]
	for _, field := range []struct {
		name   string
		target *bool
	}{
		{name: "sudo", target: &facts.Sudo}, {name: "containerd", target: &facts.Containerd},
		{name: "kubelet", target: &facts.Kubelet}, {name: "kubeadm", target: &facts.Kubeadm},
		{name: "kubectl", target: &facts.Kubectl}, {name: "cni", target: &facts.CNI},
		{name: "kubernetes_pki", target: &facts.KubernetesPKI}, {name: "kubernetes_dir", target: &facts.KubernetesDir},
		{name: "cluster_state", target: &facts.ClusterState}, {name: "gpu", target: &facts.GPU},
	} {
		value, ok := parseBool(values[field.name])
		if !ok {
			return Facts{}, fmt.Errorf("remote preflight returned an invalid %s fact", field.name)
		}
		*field.target = value
	}
	return facts, nil
}

func safeText(value string) bool {
	if value == "" || len(value) > 255 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7e || value[i] == '=' {
			return false
		}
	}
	return true
}

func parseUintFact(values map[string]string, key string) (uint64, error) {
	value, err := strconv.ParseUint(values[key], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("remote preflight returned an invalid %s fact", key)
	}
	return value, nil
}

func parseBool(value string) (bool, bool) {
	switch value {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

func evaluate(machine topology.Machine, facts Facts, duplicateHostname bool) doctor.Result {
	failures := make([]string, 0)
	warnings := make([]string, 0)
	arch := normalizeArchitecture(facts.Architecture)
	if !supportedOS(facts.OSID, facts.OSVersion) {
		failures = append(failures, "unsupported OS "+facts.OSID+" "+facts.OSVersion)
	}
	if arch == "" {
		failures = append(failures, "unsupported architecture "+facts.Architecture)
	}
	if !kernelAtLeast(facts.Kernel, 5, 10) {
		failures = append(failures, "kernel must be 5.10 or newer")
	}
	if facts.DefaultInterface == "" || facts.DefaultInterface == "none" {
		failures = append(failures, "default route interface is missing")
	}
	if duplicateHostname {
		failures = append(failures, "hostname is not unique across bootstrap machines")
	}
	minimumCPU := uint64(1)
	if machine.Role == "control-plane" {
		minimumCPU = 2
	}
	if facts.CPUCount < minimumCPU {
		failures = append(failures, fmt.Sprintf("requires at least %d CPU", minimumCPU))
	}
	if facts.MemoryBytes < 2*1024*1024*1024 {
		failures = append(failures, "requires at least 2 GiB memory")
	}
	if facts.DiskBytes < 10*1024*1024*1024 {
		failures = append(failures, "requires at least 10 GiB free disk")
	}
	if facts.SwapBytes > 0 {
		failures = append(failures, "swap is enabled")
	}
	if facts.TimeSynchronized != "true" {
		failures = append(failures, "time synchronization is not confirmed")
	}
	if !facts.Sudo {
		failures = append(failures, "non-interactive sudo is unavailable")
	}
	if facts.CNI || facts.KubernetesPKI || facts.KubernetesDir || facts.ClusterState {
		failures = append(failures, "existing Kubernetes or CNI state was detected")
	}
	if machine.GPU && !facts.GPU {
		failures = append(failures, "GPU intent has no matching PCI display controller")
	} else if !machine.GPU && facts.GPU {
		warnings = append(warnings, "GPU hardware is present without GPU intent")
	}
	for name, present := range map[string]bool{"containerd": facts.Containerd, "kubeadm": facts.Kubeadm, "kubectl": facts.Kubectl, "kubelet": facts.Kubelet} {
		if present {
			warnings = append(warnings, name+" is already installed")
		}
	}
	if facts.CPUCount < uint64(machine.CPU) {
		warnings = append(warnings, "CPU is below desired machine capacity")
	}
	if facts.MemoryBytes/(1024*1024*1024) < uint64(machine.MemoryGB) {
		warnings = append(warnings, "memory is below desired machine capacity")
	}
	if facts.DiskBytes/(1024*1024*1024) < uint64(machine.DiskGB) {
		warnings = append(warnings, "free disk is below desired machine capacity")
	}
	sort.Strings(failures)
	sort.Strings(warnings)
	status := doctor.StatusPass
	message := fmt.Sprintf("ready: %s %s, %s, kernel %s, hostname %s, interface %s, %d CPU, %d GiB memory, %d GiB free disk", facts.OSID, facts.OSVersion, arch, facts.Kernel, facts.Hostname, facts.DefaultInterface, facts.CPUCount, facts.MemoryBytes/(1024*1024*1024), facts.DiskBytes/(1024*1024*1024))
	if len(failures) > 0 {
		status = doctor.StatusFail
		message = "not ready: " + strings.Join(failures, "; ")
	} else if len(warnings) > 0 {
		status = doctor.StatusWarn
		message = "ready with warnings: " + strings.Join(warnings, "; ")
	}
	return doctor.Result{Name: machine.Name, Status: status, Message: message}
}

func normalizeArchitecture(value string) string {
	switch value {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return ""
	}
}

func supportedOS(id, version string) bool {
	switch id {
	case "debian":
		major, _, _ := strings.Cut(version, ".")
		return major == "12" || major == "13"
	case "ubuntu":
		return version == "22.04" || version == "24.04" || version == "26.04"
	default:
		return false
	}
}

func kernelAtLeast(value string, minimumMajor, minimumMinor int) bool {
	release, _, _ := strings.Cut(value, "-")
	parts := strings.Split(release, ".")
	if len(parts) < 2 {
		return false
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil {
		return false
	}
	return major > minimumMajor || major == minimumMajor && minor >= minimumMinor
}

const remoteFactsScript = `set -u
printf '%s\n' 'BAREPLANE_PREFLIGHT_V1'
os_id=$(awk -F= '$1 == "ID" {gsub(/^"|"$/, "", $2); print $2; exit}' /etc/os-release 2>/dev/null || true)
os_version=$(awk -F= '$1 == "VERSION_ID" {gsub(/^"|"$/, "", $2); print $2; exit}' /etc/os-release 2>/dev/null || true)
arch=$(uname -m 2>/dev/null || true)
kernel=$(uname -r 2>/dev/null || true)
host_name=$(hostname 2>/dev/null || true)
default_interface=$(ip route show default 2>/dev/null | awk 'NR == 1 {for (i=1; i<=NF; i++) if ($i == "dev" && i < NF) {print $(i+1); exit}}')
cpu_count=$(getconf _NPROCESSORS_ONLN 2>/dev/null || printf '0')
memory_bytes=$(awk '$1 == "MemTotal:" {printf "%.0f", $2 * 1024; exit}' /proc/meminfo 2>/dev/null || printf '0')
swap_bytes=$(awk '$1 == "SwapTotal:" {printf "%.0f", $2 * 1024; exit}' /proc/meminfo 2>/dev/null || printf '0')
disk_bytes=$(df -Pk / 2>/dev/null | awk 'NR == 2 {printf "%.0f", $4 * 1024; exit}')
if sudo -n true >/dev/null 2>&1; then sudo_ok=true; else sudo_ok=false; fi
time_sync=$(timedatectl show -p NTPSynchronized --value 2>/dev/null || true)
case "$time_sync" in yes) time_sync=true ;; no) time_sync=false ;; *) time_sync=unknown ;; esac
has_command() { if command -v "$1" >/dev/null 2>&1; then printf 'true'; else printf 'false'; fi; }
has_path() { if [ -e "$1" ]; then printf 'true'; else printf 'false'; fi; }
cni=false
if [ -d /etc/cni/net.d ]; then cni=true; fi
kubernetes_pki=false
if [ -d /etc/kubernetes/pki ]; then kubernetes_pki=true; fi
cluster_state=false
if [ -e /var/lib/kubelet/kubeadm-flags.env ] || [ -e /etc/kubernetes/kubelet.conf ] || [ -e /var/lib/etcd/member ]; then cluster_state=true; fi
gpu=false
if command -v lspci >/dev/null 2>&1 && lspci -Dn 2>/dev/null | grep -Eq ' (0300|0302):'; then gpu=true; fi
printf 'os_id=%s\n' "$os_id"
printf 'os_version=%s\n' "$os_version"
printf 'arch=%s\n' "$arch"
printf 'kernel=%s\n' "$kernel"
printf 'hostname=%s\n' "$host_name"
printf 'default_interface=%s\n' "$default_interface"
printf 'cpu_count=%s\n' "$cpu_count"
printf 'memory_bytes=%s\n' "$memory_bytes"
printf 'disk_bytes=%s\n' "$disk_bytes"
printf 'swap_bytes=%s\n' "$swap_bytes"
printf 'time_synchronized=%s\n' "$time_sync"
printf 'sudo=%s\n' "$sudo_ok"
printf 'containerd=%s\n' "$(has_command containerd)"
printf 'kubelet=%s\n' "$(has_command kubelet)"
printf 'kubeadm=%s\n' "$(has_command kubeadm)"
printf 'kubectl=%s\n' "$(has_command kubectl)"
printf 'cni=%s\n' "$cni"
printf 'kubernetes_pki=%s\n' "$kubernetes_pki"
printf 'kubernetes_dir=%s\n' "$(has_path /etc/kubernetes)"
printf 'cluster_state=%s\n' "$cluster_state"
printf 'gpu=%s\n' "$gpu"
`
