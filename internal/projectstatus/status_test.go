package projectstatus

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/project"
)

func TestInspectProvisioningReadyProjectNeedsRender(t *testing.T) {
	configPath := writeConfig(t)
	report, err := Inspect(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if report.Cluster != "lab" || !report.ProvisioningReady || report.Rendered {
		t.Fatalf("unexpected report: %#v", report)
	}
	if report.Next != "run bareplane render" {
		t.Fatalf("next = %q", report.Next)
	}
}

func TestInspectAttestedPlanIsApplyReady(t *testing.T) {
	configPath := writeConfig(t)
	workspace, err := project.EnsureTerraformWorkspace(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.ReplaceGeneratedDirectory(workspace.GeneratedDir, "terraform", map[string][]byte{"main.tf.json": []byte("{}\n")}); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{
		workspace.LockFile:         "lock\n",
		workspace.PlanFile:         "plan",
		workspace.PlanManifestFile: "{}\n",
	} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	report, err := Inspect(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Rendered || !report.PlanPresent || !report.ManifestPresent || !report.LockPresent {
		t.Fatalf("unexpected report: %#v", report)
	}
	if want := "bareplane terraform apply --approve lab"; !strings.Contains(report.Next, want) {
		t.Fatalf("next = %q, want to contain %q", report.Next, want)
	}
}

func TestInspectUnattestedPlanIsDiagnosticOnly(t *testing.T) {
	configPath := writeConfig(t)
	workspace, err := project.EnsureTerraformWorkspace(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.ReplaceGeneratedDirectory(workspace.GeneratedDir, "terraform", map[string][]byte{"main.tf.json": []byte("{}\n")}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspace.PlanFile, []byte("diagnostic-plan"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := Inspect(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report.Next, "diagnostic-only") {
		t.Fatalf("next = %q", report.Next)
	}
}

func TestInspectRejectsSymlinkedStateArtifact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior depends on Windows privileges")
	}
	configPath := writeConfig(t)
	workspace, err := project.EnsureTerraformWorkspace(configPath)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, workspace.StateFile); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(configPath); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("expected unsafe workspace error, got %v", err)
	}
}

func TestInspectReportsOperationLock(t *testing.T) {
	configPath := writeConfig(t)
	lock, err := project.AcquireTerraformOperation(configPath, "terraform-plan")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	report, err := Inspect(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Operation.Present || report.Operation.Operation != "terraform-plan" {
		t.Fatalf("unexpected operation status: %#v", report.Operation)
	}
	if !strings.Contains(report.Next, "terraform-plan") {
		t.Fatalf("next = %q", report.Next)
	}
}

func writeConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bareplane.yaml")
	content := `apiVersion: bareplane.io/v1alpha1
kind: BareplaneCluster
metadata:
  name: lab
spec:
  domain: lab.example.com
  provider:
    type: proxmox
    endpoint: https://proxmox.example.com:8006
    targets:
      - pve1
    proxmox:
      bridge: vmbr0
      systemDatastore: local-lvm
      cloudImageFileID: local:import/debian.qcow2
      ssh:
        user: debian
        publicKeyFile: id_ed25519.pub
  nodes:
    - name: control
      role: control-plane
      count: 1
      cpu: 4
      memoryGB: 8
      diskGB: 64
  features:
    observability: true
    gpu: false
  profiles:
    - minimal
  dns:
    provider: manual
  secrets:
    provider: sops
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
