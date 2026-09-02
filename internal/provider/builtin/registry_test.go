package builtin

import (
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/provider/proxmox"
)

func TestRegistryIncludesProxmox(t *testing.T) {
	registry, err := Registry()
	if err != nil {
		t.Fatalf("Registry() error = %v", err)
	}

	resolved, err := registry.Resolve(config.Provider{
		Type:     proxmox.Type,
		Endpoint: "https://proxmox.example.com:8006",
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Type() != proxmox.Type {
		t.Fatalf("unexpected provider type %q", resolved.Type())
	}
}
