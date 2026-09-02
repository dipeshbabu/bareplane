package terraform

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
)

const testPublicKey = "ssh-ed25519 dGVzdA== bareplane-test"

func TestRenderProxmoxIsDeterministicAndUsesMatureResource(t *testing.T) {
	cfg := renderConfig()
	first, err := RenderProxmox(cfg, testPublicKey)
	if err != nil {
		t.Fatalf("RenderProxmox() error = %v", err)
	}
	second, err := RenderProxmox(cfg, testPublicKey)
	if err != nil {
		t.Fatalf("RenderProxmox() second error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("identical inputs produced different Terraform")
	}

	root := decodeDocument(t, first)
	required := root["terraform"].(map[string]any)["required_providers"].(map[string]any)["proxmox"].(map[string]any)
	if required["source"] != ProxmoxProviderSource || required["version"] != ProxmoxProviderVersion {
		t.Fatalf("unexpected provider requirement: %#v", required)
	}

	resourceRoot := root["resource"].(map[string]any)
	if _, ok := resourceRoot["proxmox_vm"]; ok {
		t.Fatal("renderer used experimental proxmox_vm resource")
	}
	resources := resourceRoot[VMResourceType].(map[string]any)
	if len(resources) != 2 {
		t.Fatalf("expected 2 VM resources, got %d", len(resources))
	}
}

func TestRenderProxmoxRendersPlacementOwnershipAndCapacity(t *testing.T) {
	cfg := renderConfig()
	encoded, err := RenderProxmox(cfg, testPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	root := decodeDocument(t, encoded)
	resources := root["resource"].(map[string]any)[VMResourceType].(map[string]any)
	vm := resources["machine_lab_control_1"].(map[string]any)

	if vm["name"] != "lab-control-1" || vm["node_name"] != "pve1" {
		t.Fatalf("unexpected VM identity: %#v", vm)
	}
	cpu := firstBlock(t, vm, "cpu")
	if cpu["cores"] != float64(4) {
		t.Fatalf("unexpected CPU block: %#v", cpu)
	}
	memory := firstBlock(t, vm, "memory")
	if memory["dedicated"] != float64(8192) {
		t.Fatalf("unexpected memory block: %#v", memory)
	}
	disk := firstBlock(t, vm, "disk")
	if disk["datastore_id"] != "local-lvm" || disk["size"] != float64(64) {
		t.Fatalf("unexpected disk block: %#v", disk)
	}
	if disk["interface"] != "virtio0" || disk["import_from"] != "local:import/debian.qcow2" {
		t.Fatalf("unexpected disk source or interface: %#v", disk)
	}
	if _, exists := disk["file_id"]; exists {
		t.Fatal("import image unexpectedly rendered file_id")
	}

	tags := vm["tags"].([]any)
	if len(tags) != 2 || tags[0] != "bareplane" || tags[1] != "bareplane-cluster-lab" {
		t.Fatalf("unexpected ownership tags: %#v", tags)
	}
	agent := firstBlock(t, vm, "agent")
	if agent["enabled"] != false {
		t.Fatalf("guest agent must remain disabled initially: %#v", agent)
	}
	initialization := firstBlock(t, vm, "initialization")
	ipConfig := firstBlock(t, initialization, "ip_config")
	ipv4 := firstBlock(t, ipConfig, "ipv4")
	if ipv4["address"] != "dhcp" {
		t.Fatalf("unexpected IP config: %#v", ipv4)
	}
	user := firstBlock(t, initialization, "user_account")
	if user["username"] != "debian" {
		t.Fatalf("unexpected cloud-init user: %#v", user)
	}
}

func TestRenderProxmoxUsesFileIDForISOContent(t *testing.T) {
	cfg := renderConfig()
	cfg.Spec.Provider.Proxmox.CloudImageFileID = "local:iso/debian.qcow2.xz"
	encoded, err := RenderProxmox(cfg, testPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	root := decodeDocument(t, encoded)
	vm := root["resource"].(map[string]any)[VMResourceType].(map[string]any)["machine_lab_control_1"].(map[string]any)
	disk := firstBlock(t, vm, "disk")
	if disk["file_id"] != "local:iso/debian.qcow2.xz" {
		t.Fatalf("expected file_id image source, got %#v", disk)
	}
	if _, exists := disk["import_from"]; exists {
		t.Fatal("ISO image unexpectedly rendered import_from")
	}
}

func TestRenderProxmoxEscapesTerraformTemplateSequences(t *testing.T) {
	cfg := renderConfig()
	key := "ssh-ed25519 dGVzdA== comment-${danger}-%{if true}"
	encoded, err := RenderProxmox(cfg, key)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, `comment-${danger}-%{if true}`) {
		t.Fatalf("Terraform template sequence was not escaped: %s", text)
	}
	if !strings.Contains(text, `comment-$${danger}-%%{if true}`) {
		t.Fatalf("expected escaped Terraform template sequence: %s", text)
	}
}

func TestRenderProxmoxContainsNoCredentialOrPrivateKeyFields(t *testing.T) {
	cfg := renderConfig()
	encoded, err := RenderProxmox(cfg, testPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{
		"api_token",
		"token-secret",
		"private_key",
		"id_ed25519.pub",
		"password",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("generated Terraform contains forbidden material %q", forbidden)
		}
	}
	if !strings.Contains(text, testPublicKey) {
		t.Fatal("generated Terraform does not contain the configured public key")
	}
}

func TestRenderProxmoxRejectsInvalidPublicKey(t *testing.T) {
	for _, key := range []string{"", "not-a-key", "ssh-ed25519 !!!"} {
		t.Run(key, func(t *testing.T) {
			if _, err := RenderProxmox(renderConfig(), key); err == nil {
				t.Fatal("expected public key validation error")
			}
		})
	}
}

func TestRenderProxmoxRequiresProvisioningReadyConfig(t *testing.T) {
	if _, err := RenderProxmox(config.Default(), testPublicKey); err == nil {
		t.Fatal("expected provisioning readiness error")
	}
}

func firstBlock(t *testing.T, parent map[string]any, name string) map[string]any {
	t.Helper()
	blocks, ok := parent[name].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("expected exactly one %s block, got %#v", name, parent[name])
	}
	block, ok := blocks[0].(map[string]any)
	if !ok {
		t.Fatalf("expected %s block object, got %#v", name, blocks[0])
	}
	return block
}

func decodeDocument(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("generated Terraform is not valid JSON: %v", err)
	}
	return decoded
}

func renderConfig() config.Config {
	cfg := config.Default()
	cfg.Metadata.Name = "lab"
	cfg.Spec.Provider.Targets = []string{"pve2", "pve1"}
	cfg.Spec.Provider.Proxmox = &config.ProxmoxProvisioning{
		Bridge:           "vmbr0",
		SystemDatastore:  "local-lvm",
		CloudImageFileID: "local:import/debian.qcow2",
		SSH: config.SSHProvisioning{
			User:          "debian",
			PublicKeyFile: "~/.ssh/id_ed25519.pub",
		},
	}
	cfg.Spec.Nodes = []config.NodeGroup{
		{Name: "control", Role: "control-plane", Count: 1, CPU: 4, MemoryGB: 8, DiskGB: 64},
		{Name: "worker", Role: "worker", Count: 1, CPU: 8, MemoryGB: 16, DiskGB: 128},
	}
	return cfg
}
