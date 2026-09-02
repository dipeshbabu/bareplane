package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunStatusPrintsOfflineLifecycleState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bareplane.yaml")
	content := `apiVersion: bareplane.io/v1alpha1
kind: BareplaneCluster
metadata:
  name: status-test
spec:
  domain: status.example.com
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

	var stdout, stderr bytes.Buffer
	code := Run([]string{"status", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("status exit code = %d, stderr = %q", code, stderr.String())
	}
	for _, expected := range []string{
		"cluster: status-test",
		"provisioning-ready: true",
		"rendered: false",
		"operation: none",
		"next: run bareplane render",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("status output missing %q: %s", expected, stdout.String())
		}
	}
}

func TestRunStatusRejectsTooManyArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"status", "one", "two"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "usage: bareplane status [path]") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}
