package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("expected help output, got %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
}

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if !strings.HasPrefix(stdout.String(), "bareplane ") {
		t.Fatalf("unexpected version output %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"nope"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("expected unknown command error, got %q", stderr.String())
	}
}

func TestRunInitAndValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster", "bareplane.yaml")

	var initOut, initErr bytes.Buffer
	if code := Run([]string{"init", path}, &initOut, &initErr); code != 0 {
		t.Fatalf("init exit code = %d: %s", code, initErr.String())
	}
	if !strings.Contains(initOut.String(), "created "+path) {
		t.Fatalf("unexpected init output %q", initOut.String())
	}

	var validateOut, validateErr bytes.Buffer
	if code := Run([]string{"validate", path}, &validateOut, &validateErr); code != 0 {
		t.Fatalf("validate exit code = %d: %s", code, validateErr.String())
	}
	if !strings.Contains(validateOut.String(), `cluster "bareplane"`) {
		t.Fatalf("unexpected validate output %q", validateOut.String())
	}
}

func TestRunInitRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bareplane.yaml")
	if err := os.WriteFile(path, []byte("existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"init", path}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "refusing to overwrite") {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}
}

func TestRunValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bareplane.yaml")
	input := `apiVersion: bareplane.io/v1alpha1
kind: BareplaneCluster
metadata:
  name: test
spec:
  domain: test.example.com
  provider:
    type: proxmox
    endpoint: https://proxmox.example.com:8006
  nodes:
    - name: control
      role: control-plane
      count: 1
      cpu: 2
      memoryGB: 4
      diskGB: 32
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
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"validate", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `cluster "test"`) {
		t.Fatalf("unexpected output %q", stdout.String())
	}
}

func TestRunDoctorAllowsWarnings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bareplane.yaml")
	var initOut, initErr bytes.Buffer
	if code := Run([]string{"init", path}, &initOut, &initErr); code != 0 {
		t.Fatalf("init exit code = %d: %s", code, initErr.String())
	}

	lookPath := func(name string) (string, error) {
		if name == "helm" {
			return "", fmt.Errorf("not found")
		}
		return filepath.Join("/usr/bin", name), nil
	}
	var stdout, stderr bytes.Buffer
	code := runDoctor([]string{path}, &stdout, &stderr, lookPath)
	if code != 0 {
		t.Fatalf("doctor exit code = %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "WARN  helm") {
		t.Fatalf("expected helm warning, got %q", stdout.String())
	}
}

func TestRunDoctorFailsForRequiredTool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bareplane.yaml")
	var initOut, initErr bytes.Buffer
	if code := Run([]string{"init", path}, &initOut, &initErr); code != 0 {
		t.Fatalf("init exit code = %d: %s", code, initErr.String())
	}

	lookPath := func(name string) (string, error) {
		if name == "terraform" {
			return "", fmt.Errorf("not found")
		}
		return filepath.Join("/usr/bin", name), nil
	}
	var stdout, stderr bytes.Buffer
	code := runDoctor([]string{path}, &stdout, &stderr, lookPath)
	if code != 1 {
		t.Fatalf("expected doctor exit code 1, got %d", code)
	}
	if !strings.Contains(stdout.String(), "FAIL  terraform") {
		t.Fatalf("expected terraform failure, got %q", stdout.String())
	}
}
