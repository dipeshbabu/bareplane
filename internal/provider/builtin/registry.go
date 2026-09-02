package builtin

import (
	"fmt"

	"github.com/dipeshbabu/bareplane/internal/provider"
	"github.com/dipeshbabu/bareplane/internal/provider/proxmox"
)

func Registry() (*provider.Registry, error) {
	registry := provider.NewRegistry()
	if err := registry.Register(proxmox.Type, proxmox.New); err != nil {
		return nil, fmt.Errorf("register %s provider: %w", proxmox.Type, err)
	}
	return registry, nil
}
