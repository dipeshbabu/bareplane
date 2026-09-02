package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRenderWritesManagedTerraform(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")
	keyPath := filepath.Join(root, "id_ed25519.pub")
	writeRenderFixture(t, configPath, keyPath)

	var stdout, stderr bytes.Buffer
	code := runRender([]string{configPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runRender() code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), filepath.Join(".bareplane", "terraform")) {
		t.Fatalf("unexpected stdout %q", stdout.String())
	}

	generated := filepath.Join(root, ".bareplane", "terraform", "main.tf.json")
	data, err := os.ReadFile(generated)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{"proxmox_virtual_environment_vm", "lab-control-1", "bareplane-cluster-lab"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("generated Terraform missing %q", expected)
		}
	}
	if strings.Contains(text, "id_ed25519.pub") {
		t.Fatal("generated Terraform leaked public-key path")
	}
}

func TestRunRenderRefusesUnmanagedOutput(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")
	keyPath := filepath.Join(root, "id_ed25519.pub")
	writeRenderFixture(t, configPath, keyPath)

	output := filepath.Join(root, ".bareplane", "terraform")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "keep.txt"), []byte("important"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runRender([]string{configPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected refusal exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "refusing to replace unmanaged output directory") {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(output, "keep.txt"))
	if err != nil || string(data) != "important" {
		t.Fatalf("unmanaged output changed: %q, %v", data, err)
	}
}

func TestResolveConfiguredPath(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "nested", "bareplane.yaml")
	got, err := resolveConfiguredPath(configPath, "keys/admin.pub")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "nested", "keys", "admin.pub")
	if got != want {
		t.Fatalf("resolveConfiguredPath() = %q, want %q", got, want)
	}
	if _, err := resolveConfiguredPath(configPath, "~other/key.pub"); err == nil {
		t.Fatal("expected unsupported tilde path error")
	}
}

func TestReadBoundedFileRejectsOversize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedFile(path, 4); err == nil {
		t.Fatal("expected size limit error")
	}
}

func writeRenderFixture(t *testing.T, configPath, keyPath string) {
	t.Helper()
	if err := os.WriteFile(keyPath, []byte("ssh-ed25519 dGVzdA== test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := `apiVersion: bareplane.io/v1alpha1
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
	if err := os.WriteFile(configPath, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
}
