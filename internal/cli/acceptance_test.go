package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/bootstrapapply"
	"github.com/dipeshbabu/bareplane/internal/bootstrappreflight"
	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/doctor"
	"github.com/dipeshbabu/bareplane/internal/project"
	gitopsrender "github.com/dipeshbabu/bareplane/internal/render/gitops"
	terraformrender "github.com/dipeshbabu/bareplane/internal/render/terraform"
	providerruntime "github.com/dipeshbabu/bareplane/internal/runtime"
	"github.com/dipeshbabu/bareplane/internal/terraformexec"
)

// This test exercises the real CLI/control-plane boundaries in one project.
// Only provider responses and external command effects are synthetic. The VM
// CI jobs independently prove real Linux/Kubernetes/Argo behavior.
func TestAcceptanceMockedLifecycle(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bareplane.yaml")
	var transcript bytes.Buffer
	check := func(want int, invoke func(io.Writer, io.Writer) int) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := invoke(&stdout, &stderr)
		transcript.Write(stdout.Bytes())
		transcript.Write(stderr.Bytes())
		if code != want {
			t.Fatalf("exit=%d want=%d\n%s\n%s", code, want, stdout.String(), stderr.String())
		}
		return stdout.String() + stderr.String()
	}
	command := func(want int, args ...string) string {
		return check(want, func(out, err io.Writer) int { return Run(args, out, err) })
	}
	command(0, "init", path)
	initial, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	command(1, "init", path)
	if after, _ := os.ReadFile(path); !bytes.Equal(initial, after) {
		t.Fatal("repeat init overwrote user configuration")
	}
	var created atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("provider discovery attempted mutation: %s", r.Method)
			http.Error(w, "forbidden", 405)
			return
		}
		if r.Header.Get("Authorization") != "PVEAPIToken=acceptance@pve!fixture=ACCEPTANCE-PROVIDER-SECRET" {
			t.Error("provider credentials did not cross the intended authenticated boundary")
		}
		switch r.URL.Path {
		case "/api2/json/version":
			fmt.Fprint(w, `{"data":{"version":"9.2.0","release":"9.2"}}`)
		case "/api2/json/cluster/resources":
			if created.Load() {
				fmt.Fprint(w, `{"data":[{"vmid":101,"type":"qemu","name":"lab-control-1","node":"pve1","status":"running","maxcpu":2,"maxmem":4294967296,"maxdisk":25769803776,"tags":"bareplane;bareplane-cluster-lab"}]}`)
			} else {
				fmt.Fprint(w, `{"data":[]}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			fmt.Fprint(connection, "SSH-2.0-Bareplane-Acceptance\r\n")
			connection.Close()
		}
	}()
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "bareplane.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Metadata.Name = "lab"
	cfg.Spec.Provider.Endpoint = server.URL
	cfg.Spec.Provider.Targets = []string{"pve1"}
	cfg.Spec.Provider.Proxmox = &config.ProxmoxProvisioning{Bridge: "vmbr0", SystemDatastore: "local-lvm", CloudImageFileID: "local:import/ubuntu.qcow2", SSH: config.SSHProvisioning{User: "ubuntu", PublicKeyFile: "id_ed25519.pub"}}
	cfg.Spec.Nodes = []config.NodeGroup{{Name: "control", Role: "control-plane", Count: 1, CPU: 2, MemoryGB: 4, DiskGB: 24}}
	cfg.Spec.Features = config.Features{}
	cfg.Spec.Profiles = []string{"minimal"}
	cfg.Spec.DNS.Provider = "manual"
	cfg.Spec.Secrets.Provider = "sops"
	cfg.Spec.Bootstrap.SSH = &config.SSHBootstrap{User: "ubuntu", PrivateKeyFile: "id_ed25519", Port: listener.Addr().(*net.TCPAddr).Port, Hosts: map[string]string{"lab-control-1": "127.0.0.1"}}
	cfg.Spec.GitOps = &config.GitOpsConfig{RepoURL: "https://github.com/example/acceptance.git", Revision: "main", RootPath: "clusters/lab"}
	var encoded bytes.Buffer
	if err := config.Encode(&encoded, cfg); err != nil {
		t.Fatal(err)
	}
	acceptanceWrite(t, path, encoded.Bytes())
	acceptanceWrite(t, filepath.Join(root, "id_ed25519"), []byte("ACCEPTANCE-PRIVATE-KEY"))
	acceptanceWrite(t, filepath.Join(root, "id_ed25519.pub"), []byte("ssh-ed25519 "+cliTestEncodedED25519(73)+" acceptance\n"))
	command(0, "validate", path)
	lookup := terraformCredentialLookup("acceptance@pve!fixture", "ACCEPTANCE-PROVIDER-SECRET")
	tools := func(name string) (string, error) { return "/fixture/bin/" + name, nil }
	deps := providerruntime.ProviderDependencies{LookupEnv: lookup, HTTPClient: server.Client()}
	check(0, func(out, err io.Writer) int {
		return runDoctor([]string{path}, out, err, tools, providerruntime.ProviderProbe(deps))
	})
	plan := check(0, func(out, err io.Writer) int { return runPlan([]string{path}, out, err, deps) })
	if !strings.Contains(plan, "CREATE") || created.Load() {
		t.Fatal("read-only infrastructure plan did not preserve discovery boundary")
	}
	command(0, "render", path)
	workspace, err := project.TerraformWorkspaceFor(path)
	if err != nil {
		t.Fatal(err)
	}
	terra := &acceptanceTerraform{t: t, workspace: workspace, created: &created}
	planner := func(ctx context.Context, options terraformexec.PlanOptions) (terraformexec.PlanResult, error) {
		options.Runner = terra
		return terraformexec.Plan(ctx, options)
	}
	applier := func(ctx context.Context, options terraformexec.ApplyOptions) (terraformexec.ApplyResult, error) {
		options.Runner = terra
		return terraformexec.Apply(ctx, options)
	}
	tf := func(want int, args ...string) string {
		return check(want, func(out, err io.Writer) int { return runTerraformWith(args, out, err, lookup, tools, planner, applier) })
	}
	terra.fail = "plan"
	tf(1, "plan", path)
	if _, err := os.Stat(workspace.PlanManifestFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed plan created an attestation")
	}
	terra.fail = ""
	tf(0, "plan", path)
	tf(1, "apply", "--approve", "other", path)
	if created.Load() {
		t.Fatal("wrong approval created infrastructure")
	}
	terra.fail = "apply"
	tf(1, "apply", "--approve", "lab", path)
	if _, err := os.Stat(workspace.PlanManifestFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed apply retained replayable attestation")
	}
	terra.fail = ""
	tf(0, "plan", path)
	tf(0, "apply", "--approve", "lab", path)
	if !created.Load() {
		t.Fatal("saved-plan apply did not reach fake provider effect")
	}
	command(0, "bootstrap", "render", path)
	check(0, func(out, err io.Writer) int {
		return runBootstrapDoctor([]string{path}, out, err, tools, os.UserHomeDir)
	})
	command(0, "bootstrap", "check", path)
	scan := func(_ context.Context, host string, port int) ([]byte, error) {
		return []byte(fmt.Sprintf("[%s]:%d ssh-ed25519 %s\n", host, port, cliTestEncodedED25519(74))), nil
	}
	check(1, func(out, err io.Writer) int {
		return runBootstrapTrust([]string{path}, strings.NewReader("wrong\n"), out, err, scan)
	})
	check(0, func(out, err io.Writer) int {
		return runBootstrapTrust([]string{path}, strings.NewReader("lab\n"), out, err, scan)
	})
	initialized := false
	facts := func(_ context.Context, request bootstrappreflight.Request) ([]byte, error) {
		return []byte(acceptanceFacts(request.Machine.Name, initialized)), nil
	}
	check(0, func(out, err io.Writer) int { return runBootstrapPreflight([]string{path}, out, err, facts) })
	state, err := project.BootstrapStateDirFor(path)
	if err != nil {
		t.Fatal(err)
	}
	fail := "join"
	var phases []string
	options := bootstrapapply.Options{LookPath: tools, Preflight: func(ctx context.Context, options bootstrappreflight.Options) doctor.Report {
		options.Runner = facts
		return bootstrappreflight.Inspect(ctx, options)
	}, Runner: func(_ context.Context, request bootstrapapply.Request) error {
		phases = append(phases, request.Phase)
		if request.Phase == fail {
			fmt.Fprint(request.Log, "ACCEPTANCE-PRIVATE-LOG")
			return errors.New("ACCEPTANCE-PRIVATE-LOG")
		}
		if request.Phase == "control_plane_init" {
			initialized = true
		}
		if request.Phase == "kubeconfig" {
			acceptanceWrite(t, filepath.Join(state, "admin.conf"), []byte("ACCEPTANCE-PRIVATE-KUBECONFIG"))
		}
		return nil
	}}
	boot := func(want int) {
		check(want, func(out, err io.Writer) int {
			return runBootstrapApply([]string{"--approve", "lab", path}, out, err, options)
		})
	}
	boot(1)
	progress, _, err := bootstrapapply.ReadProgress(path)
	if err != nil || progress.Completed != 5 || progress.Active != "join" {
		t.Fatalf("failed bootstrap advanced: %+v %v", progress, err)
	}
	fail = ""
	boot(0)
	status := command(0, "status", path)
	if !strings.Contains(status, "kubernetes-ready: true") || !strings.Contains(status, "argocd-ready: false") {
		t.Fatal("Kubernetes-ready transition missing")
	}
	command(0, "gitops", "render", path)
	install := func(want int) {
		check(want, func(out, err io.Writer) int {
			return runGitOpsInstall([]string{"--approve", "lab", path}, out, err, options)
		})
	}
	handoff := func(want int) {
		check(want, func(out, err io.Writer) int {
			return runGitOpsHandoff([]string{"--approve", "lab", path}, out, err, options)
		})
	}
	fail = "argocd"
	install(1)
	status = command(0, "status", path)
	if !strings.Contains(status, "argocd-ready: false") {
		t.Fatal("failed Argo phase advanced")
	}
	fail = ""
	install(0)
	status = command(0, "status", path)
	if !strings.Contains(status, "argocd-ready: true") || !strings.Contains(status, "gitops-handed-off: false") {
		t.Fatal("Argo-ready transition missing")
	}
	fail = "handoff"
	handoff(1)
	status = command(0, "status", path)
	if !strings.Contains(status, "gitops-handed-off: false") {
		t.Fatal("failed handoff advanced")
	}
	fail = ""
	handoff(0)
	status = command(0, "status", path)
	if !strings.Contains(status, "gitops-handed-off: true") {
		t.Fatal("handoff transition missing")
	}
	// Stage-aware repeat: init and installer refusals are safety boundaries,
	// while existing infrastructure/cluster/GitOps reconciliation stays stable.
	beforeState, _ := os.ReadFile(workspace.StateFile)
	command(1, "init", path)
	command(0, "validate", path)
	command(0, "render", path)
	plan = check(0, func(out, err io.Writer) int { return runPlan([]string{path}, out, err, deps) })
	if !strings.Contains(plan, "NOOP") {
		t.Fatal("repeated provider plan drifted")
	}
	if output := tf(0, "plan", path); !strings.Contains(output, "no changes") {
		t.Fatal("repeated Terraform plan drifted")
	}
	tf(0, "apply", "--approve", "lab", path)
	if after, _ := os.ReadFile(workspace.StateFile); !bytes.Equal(beforeState, after) {
		t.Fatal("no-op fake apply changed state")
	}
	command(0, "bootstrap", "render", path)
	check(0, func(out, err io.Writer) int {
		return runBootstrapTrust([]string{path}, &rejectingApprovalReader{}, out, err, scan)
	})
	phases = nil
	boot(0)
	if len(phases) != 1 || phases[0] != "health" {
		t.Fatalf("repeat bootstrap reconfigured hosts: %v", phases)
	}
	command(0, "gitops", "render", path)
	install(1)
	handoff(0)
	for _, directory := range []string{workspace.GeneratedDir, filepath.Join(root, ".bareplane", "bootstrap"), filepath.Join(root, "gitops")} {
		if err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte("ACCEPTANCE-PRIVATE")) || bytes.Contains(data, []byte("ACCEPTANCE-PROVIDER-SECRET")) {
				t.Fatalf("private fixture leaked to generated payload %s", path)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(transcript.String(), "ACCEPTANCE-PRIVATE") || strings.Contains(transcript.String(), "ACCEPTANCE-PROVIDER-SECRET") {
		t.Fatal("private fixture leaked to public CLI output")
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{workspace.StateFile, filepath.Join(state, "admin.conf"), filepath.Join(state, "progress.json"), filepath.Join(state, "gitops.json"), filepath.Join(state, "known_hosts")} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm()&0o077 != 0 {
				t.Fatalf("private artifact permissions: %s %v", path, err)
			}
		}
	}
}

func acceptanceWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

type acceptanceTerraform struct {
	t         *testing.T
	workspace project.TerraformWorkspace
	created   *atomic.Bool
	fail      string
}

func (runner *acceptanceTerraform) Run(_ context.Context, command terraformexec.Command) (int, error) {
	runner.t.Helper()
	for _, arg := range command.Args {
		if strings.Contains(arg, "ACCEPTANCE-PROVIDER-SECRET") {
			runner.t.Fatal("provider secret entered process arguments")
		}
	}
	switch command.Args[0] {
	case "version":
		fmt.Fprint(command.Stdout, `{"terraform_version":"1.16.0"}`)
		return 0, nil
	case "init":
		acceptanceWrite(runner.t, filepath.Join(command.Dir, ".terraform.lock.hcl"), []byte("synthetic-provider-lock\n"))
		return 0, nil
	case "plan":
		if runner.fail == "plan" {
			return 1, nil
		}
		acceptanceWrite(runner.t, runner.workspace.PlanFile, []byte("synthetic-saved-plan-not-executable"))
		if runner.created.Load() {
			return 0, nil
		}
		return 2, nil
	case "apply":
		if _, err := os.Stat(runner.workspace.PlanManifestFile); !errors.Is(err, os.ErrNotExist) {
			runner.t.Fatal("apply began with replayable attestation")
		}
		if runner.fail == "apply" {
			return 1, nil
		}
		if data, err := os.ReadFile(runner.workspace.StateFile); err == nil {
			acceptanceWrite(runner.t, runner.workspace.StateBackupFile, data)
		}
		acceptanceWrite(runner.t, runner.workspace.StateFile, []byte(`{"version":4,"serial":1,"lineage":"synthetic-acceptance","resources":[]}`))
		runner.created.Store(true)
		return 0, nil
	default:
		runner.t.Fatalf("unexpected Terraform command: %v", command.Args)
		return 1, nil
	}
}

func acceptanceFacts(host string, initialized bool) string {
	value := "false"
	if initialized {
		value = "true"
	}
	return fmt.Sprintf("BAREPLANE_PREFLIGHT_V1\nos_id=ubuntu\nos_version=24.04\narch=x86_64\nkernel=6.8.0\nhostname=%s\ndefault_interface=eth0\ncpu_count=2\nmemory_bytes=4294967296\ndisk_bytes=25769803776\nswap_bytes=0\ntime_synchronized=true\nsudo=true\ncontainerd=%s\nkubelet=%s\nkubeadm=%s\nkubectl=%s\ncni=%s\nkubernetes_pki=%s\nkubernetes_dir=%s\ncluster_state=%s\ngpu=false\n", host, value, value, value, value, value, value, value, value)
}

func TestAcceptanceReferenceCompatibilityMatchesImplementedPins(t *testing.T) {
	root := filepath.Join("..", "..")
	data, err := os.ReadFile(filepath.Join(root, "examples", "acceptance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, validate := range []func() error{cfg.ValidateProvisioning, cfg.ValidateKubernetesBootstrap, cfg.ValidateGitOps} {
		if err := validate(); err != nil {
			t.Fatal(err)
		}
	}
	data, err = os.ReadFile(filepath.Join(root, "docs", "compatibility.json"))
	if err != nil {
		t.Fatal(err)
	}
	var versions map[string]string
	if err := json.Unmarshal(data, &versions); err != nil {
		t.Fatal(err)
	}
	for key, actual := range map[string]string{
		"proxmox_provider_constraint": terraformrender.ProxmoxProviderVersion,
		"kubernetes_reference":        cfg.Spec.Kubernetes.Version,
		"kube_vip":                    cfg.Spec.Kubernetes.KubeVIPVersion,
		"cilium":                      cfg.Spec.Kubernetes.CiliumVersion,
		"argocd":                      gitopsrender.ArgoVersion,
	} {
		if versions[key] != actual {
			t.Fatalf("compatibility %s is stale: %q != %q", key, versions[key], actual)
		}
	}
	module, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || !strings.Contains(strings.ReplaceAll(string(module), "\r\n", "\n"), "go "+versions["go_minimum"]+"\n") {
		t.Fatal("Go compatibility minimum is stale")
	}
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil || !strings.Contains(string(workflow), "terraform_version: "+versions["terraform"]) || !strings.Contains(string(workflow), "ansible-core=="+versions["ansible_core"]) {
		t.Fatal("CI tool compatibility pins are stale")
	}
	if !strings.Contains(versions["proxmox_evidence"], "operator") {
		t.Fatal("mocked CI must not claim real Proxmox certification")
	}
}
