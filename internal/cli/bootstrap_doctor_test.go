package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/project"
	ansiblerender "github.com/dipeshbabu/bareplane/internal/render/ansible"
)

func TestRunBootstrapDoctorPassesReadyProject(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "bareplane.yaml")
	writeBootstrapDoctorConfig(t, configPath)
	destination := filepath.Join(root, ".bareplane", "bootstrap")
	if err := project.ReplaceGeneratedDirectory(destination, "bootstrap", map[string][]byte{
		ansiblerender.InventoryFilename: []byte("all:\n  children: {}\n"),
	}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runBootstrapDoctor(
		[]string{configPath},
		&stdout,
		&stderr,
		func(name string) (string, error) { return "/usr/bin/" + name, nil },
		func() (string, error) { return home, nil },
	)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q output=%q", code, stderr.String(), stdout.String())
	}
	for _, expected := range []string{"PASS  bootstrap-config", "PASS  inventory", "PASS  ssh-private-key", "PASS  ssh", "PASS  ansible-playbook"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("output missing %q: %s", expected, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "secret") {
		t.Fatalf("private key material leaked into output: %s", stdout.String())
	}
}

func TestRunBootstrapDoctorReportsMissingTool(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "bareplane.yaml")
	writeBootstrapDoctorConfig(t, configPath)
	destination := filepath.Join(root, ".bareplane", "bootstrap")
	if err := project.ReplaceGeneratedDirectory(destination, "bootstrap", map[string][]byte{
		ansiblerender.InventoryFilename: []byte("all:\n  children: {}\n"),
	}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runBootstrapDoctor([]string{configPath}, &stdout, &stderr, func(name string) (string, error) {
		if name == "ansible-playbook" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + name, nil
	}, func() (string, error) { return home, nil })
	if code != 1 || !strings.Contains(stdout.String(), "FAIL  ansible-playbook") {
		t.Fatalf("code=%d output=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunBootstrapDoctorArgumentValidation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runBootstrapDoctor([]string{"one", "two"}, &stdout, &stderr, nil, nil)
	if code != 2 || !strings.Contains(stderr.String(), "usage: bareplane bootstrap doctor [path]") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func writeBootstrapDoctorConfig(t *testing.T, path string) {
	t.Helper()
	content := `apiVersion: bareplane.io/v1alpha1
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
  bootstrap:
    ssh:
      user: debian
      privateKeyFile: ~/.ssh/id_ed25519
      hosts:
        lab-control-1: 10.0.0.10
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
}
