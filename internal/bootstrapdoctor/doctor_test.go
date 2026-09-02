package bootstrapdoctor

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/doctor"
	"github.com/dipeshbabu/bareplane/internal/project"
	ansiblerender "github.com/dipeshbabu/bareplane/internal/render/ansible"
)

func TestInspectReadyProjectPassesAllChecks(t *testing.T) {
	configPath, keyPath := setupReadyProject(t)
	report := Inspect(Options{
		ConfigPath:  configPath,
		LookPath:    func(name string) (string, error) { return "/usr/bin/" + name, nil },
		UserHomeDir: func() (string, error) { return filepath.Dir(filepath.Dir(keyPath)), nil },
	})
	if report.HasFailures() {
		t.Fatalf("unexpected failures: %#v", report.Results)
	}
	if len(report.Results) != 5 {
		t.Fatalf("expected 5 checks, got %d", len(report.Results))
	}
}

func TestInspectMissingInventoryFails(t *testing.T) {
	configPath, keyPath := setupConfigAndKey(t)
	report := Inspect(Options{
		ConfigPath:  configPath,
		LookPath:    availableTool,
		UserHomeDir: func() (string, error) { return filepath.Dir(filepath.Dir(keyPath)), nil },
	})
	assertFailed(t, report, "inventory")
}

func TestInspectMissingToolFails(t *testing.T) {
	configPath, keyPath := setupReadyProject(t)
	report := Inspect(Options{
		ConfigPath: configPath,
		LookPath: func(name string) (string, error) {
			if name == "ansible-playbook" {
				return "", errors.New("not found")
			}
			return "/usr/bin/" + name, nil
		},
		UserHomeDir: func() (string, error) { return filepath.Dir(filepath.Dir(keyPath)), nil },
	})
	assertFailed(t, report, "ansible-playbook")
}

func TestInspectRejectsSymlinkedPrivateKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior depends on Windows privileges")
	}
	configPath, keyPath := setupReadyProject(t)
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "real-key")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, keyPath); err != nil {
		t.Fatal(err)
	}
	report := Inspect(Options{ConfigPath: configPath, LookPath: availableTool, UserHomeDir: func() (string, error) {
		return filepath.Dir(filepath.Dir(keyPath)), nil
	}})
	assertFailed(t, report, "ssh-private-key")
}

func TestInspectRejectsBroadPrivateKeyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions are not applicable")
	}
	configPath, keyPath := setupReadyProject(t)
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	report := Inspect(Options{ConfigPath: configPath, LookPath: availableTool, UserHomeDir: func() (string, error) {
		return filepath.Dir(filepath.Dir(keyPath)), nil
	}})
	assertFailed(t, report, "ssh-private-key")
}

func TestInspectInvalidBootstrapStopsAfterConfig(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")
	if err := os.WriteFile(configPath, []byte(validBaseConfig(false)), 0o600); err != nil {
		t.Fatal(err)
	}
	report := Inspect(Options{ConfigPath: configPath, LookPath: availableTool})
	if len(report.Results) != 1 || report.Results[0].Status != doctor.StatusFail {
		t.Fatalf("unexpected report: %#v", report.Results)
	}
}

func setupReadyProject(t *testing.T) (string, string) {
	t.Helper()
	configPath, keyPath := setupConfigAndKey(t)
	root := filepath.Dir(configPath)
	destination := filepath.Join(root, ".bareplane", "bootstrap")
	if err := project.ReplaceGeneratedDirectory(destination, "bootstrap", map[string][]byte{
		ansiblerender.InventoryFilename: []byte("all:\n  children: {}\n"),
	}); err != nil {
		t.Fatal(err)
	}
	return configPath, keyPath
}

func setupConfigAndKey(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(sshDir, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("secret-key-material-not-read"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "bareplane.yaml")
	if err := os.WriteFile(configPath, []byte(validBaseConfig(true)), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, keyPath
}

func validBaseConfig(includeBootstrap bool) string {
	bootstrap := ""
	if includeBootstrap {
		bootstrap = `  bootstrap:
    ssh:
      user: debian
      privateKeyFile: ~/.ssh/id_ed25519
      hosts:
        lab-control-1: 10.0.0.10
`
	}
	return `apiVersion: bareplane.io/v1alpha1
kind: BareplaneCluster
metadata:
  name: lab
spec:
  domain: lab.example.com
  provider:
    type: proxmox
    endpoint: https://proxmox.example.com:8006
  nodes:
    - name: control
      role: control-plane
      count: 1
      cpu: 4
      memoryGB: 8
      diskGB: 64
` + bootstrap + `  features:
    observability: true
    gpu: false
  profiles:
    - minimal
  dns:
    provider: manual
  secrets:
    provider: sops
`
}

func availableTool(name string) (string, error) {
	return "/usr/bin/" + name, nil
}

func assertFailed(t *testing.T, report doctor.Report, name string) {
	t.Helper()
	for _, result := range report.Results {
		if result.Name == name {
			if result.Status != doctor.StatusFail {
				t.Fatalf("%s status = %s", name, result.Status)
			}
			return
		}
	}
	t.Fatalf("missing result %q: %#v", name, report.Results)
}
