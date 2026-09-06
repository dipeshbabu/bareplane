package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunBootstrapRenderWritesManagedInventory(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")
	writeBootstrapRenderConfig(t, configPath)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"bootstrap", "render", configPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	inventoryPath := filepath.Join(root, ".bareplane", "bootstrap", "inventory.yaml")
	inventory, err := os.ReadFile(inventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(inventory)
	if !strings.Contains(text, "lab-control-1") || strings.Contains(text, "id_ed25519") {
		t.Fatalf("unexpected inventory: %s", text)
	}
	if !strings.Contains(stdout.String(), "rendered bootstrap bundle") {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	for _, name := range []string{"ansible.cfg", "site.yaml", "group_vars/all/cluster.yaml", "roles/contract/templates/summary.j2"} {
		if _, err := os.Stat(filepath.Join(root, ".bareplane", "bootstrap", filepath.FromSlash(name))); err != nil {
			t.Fatalf("missing bundle asset %s: %v", name, err)
		}
	}
}

func TestBootstrapRenderMissingKubernetesDoesNotReplaceOutput(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")
	writeBootstrapRenderConfig(t, configPath)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"bootstrap", "render", configPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("%d: %s", code, stderr.String())
	}
	path := filepath.Join(root, ".bareplane", "bootstrap", "inventory.yaml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	start := bytes.Index(data, []byte("  kubernetes:"))
	end := bytes.Index(data, []byte("  features:"))
	if start < 0 || end <= start {
		t.Fatal("bad test fixture")
	}
	data = append(data[:start], data[end:]...)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := Run([]string{"bootstrap", "render", configPath}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "spec.kubernetes") {
		t.Fatalf("%d: %s", code, stderr.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("invalid config replaced output: %v", err)
	}
}

func TestRunBootstrapRenderRefusesUnmanagedDirectory(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")
	writeBootstrapRenderConfig(t, configPath)
	destination := filepath.Join(root, ".bareplane", "bootstrap")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"bootstrap", "render", configPath}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "refusing to replace unmanaged output directory") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if data, err := os.ReadFile(filepath.Join(destination, "keep.txt")); err != nil || string(data) != "keep" {
		t.Fatalf("unmanaged content changed: data=%q err=%v", data, err)
	}
}

func TestRunBootstrapUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"bootstrap"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "bareplane bootstrap render") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func writeBootstrapRenderConfig(t *testing.T, path string) {
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
  kubernetes:
    version: 1.36.4
    apiVIP: 192.168.1.100
    podCIDR: 10.244.0.0/16
    serviceCIDR: 10.96.0.0/12
    kubeVIPVersion: 1.2.3
    ciliumVersion: 1.20.1
    kubeProxyReplacement: true
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
