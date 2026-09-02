package ansible

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

func TestRenderInventoryIsDeterministic(t *testing.T) {
	cfg := inventoryConfig()
	first, err := RenderInventory(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderInventory(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("identical bootstrap configuration produced different inventory")
	}
}

func TestRenderInventoryGroupsHostsAndMetadata(t *testing.T) {
	encoded, err := RenderInventory(inventoryConfig())
	if err != nil {
		t.Fatal(err)
	}
	root := decodeInventory(t, encoded)
	all := root["all"].(map[string]any)
	children := all["children"].(map[string]any)
	control := children["control_plane"].(map[string]any)["hosts"].(map[string]any)
	workers := children["workers"].(map[string]any)["hosts"].(map[string]any)

	controlHost := control["lab-control-1"].(map[string]any)
	if controlHost["ansible_host"] != "10.0.0.10" || controlHost["ansible_user"] != "debian" || controlHost["ansible_port"] != 22 {
		t.Fatalf("unexpected control host vars: %#v", controlHost)
	}
	if controlHost["bareplane_role"] != "control-plane" || controlHost["bareplane_group"] != "control" {
		t.Fatalf("unexpected topology metadata: %#v", controlHost)
	}
	if controlHost["bareplane_provider_target"] != "pve1" {
		t.Fatalf("unexpected provider target: %#v", controlHost)
	}
	worker := workers["lab-worker-1"].(map[string]any)
	if worker["bareplane_gpu"] != true || worker["bareplane_cpu"] != 8 || worker["bareplane_memory_gb"] != 16 || worker["bareplane_disk_gb"] != 128 {
		t.Fatalf("unexpected worker metadata: %#v", worker)
	}
}

func TestRenderInventoryAcceptsIPv6DNSAndCustomPort(t *testing.T) {
	cfg := inventoryConfig()
	cfg.Spec.Bootstrap.SSH.Port = 2222
	cfg.Spec.Bootstrap.SSH.Hosts["lab-control-1"] = "2001:db8::10"
	cfg.Spec.Bootstrap.SSH.Hosts["lab-worker-1"] = "worker.lab.example.com"
	encoded, err := RenderInventory(cfg)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, expected := range []string{"2001:db8::10", "worker.lab.example.com", "ansible_port: 2222"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("inventory missing %q: %s", expected, text)
		}
	}
}

func TestRenderInventoryExcludesPrivateKeyPath(t *testing.T) {
	cfg := inventoryConfig()
	cfg.Spec.Bootstrap.SSH.PrivateKeyFile = "/very/secret/id_ed25519"
	encoded, err := RenderInventory(cfg)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "id_ed25519") || strings.Contains(text, "privateKeyFile") {
		t.Fatalf("private key path leaked into inventory: %s", text)
	}
}

func TestRenderInventoryRequiresBootstrapReadyConfig(t *testing.T) {
	cfg := inventoryConfig()
	delete(cfg.Spec.Bootstrap.SSH.Hosts, "lab-worker-1")
	if _, err := RenderInventory(cfg); err == nil || !strings.Contains(err.Error(), "required for bootstrap") {
		t.Fatalf("expected bootstrap validation failure, got %v", err)
	}
}

func decodeInventory(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode inventory: %v", err)
	}
	return decoded
}

func inventoryConfig() config.Config {
	cfg := config.Default()
	cfg.Metadata.Name = "lab"
	cfg.Spec.Provider.Targets = []string{"pve2", "pve1"}
	cfg.Spec.Nodes = []config.NodeGroup{
		{Name: "control", Role: "control-plane", Count: 1, CPU: 4, MemoryGB: 8, DiskGB: 64},
		{Name: "worker", Role: "worker", Count: 1, CPU: 8, MemoryGB: 16, DiskGB: 128, GPU: true},
	}
	cfg.Spec.Bootstrap = &config.BootstrapConfig{SSH: &config.SSHBootstrap{
		User:           "debian",
		PrivateKeyFile: "~/.ssh/id_ed25519",
		Hosts: map[string]string{
			"lab-control-1": "10.0.0.10",
			"lab-worker-1":  "10.0.0.11",
		},
	}}
	return cfg
}
