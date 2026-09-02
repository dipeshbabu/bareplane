package config

import (
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// Default returns a minimal valid Bareplane configuration suitable as a starting point.
func Default() Config {
	return Config{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata: Metadata{
			Name: "bareplane",
		},
		Spec: Spec{
			Domain: "lab.example.com",
			Provider: Provider{
				Type:     "proxmox",
				Endpoint: "https://proxmox.example.com:8006",
			},
			Nodes: []NodeGroup{
				{
					Name:     "control-plane",
					Role:     "control-plane",
					Count:    1,
					CPU:      4,
					MemoryGB: 8,
					DiskGB:   64,
				},
			},
			Features: Features{},
			Profiles: []string{"minimal"},
			DNS: DNS{
				Provider: "manual",
			},
			Secrets: Secrets{
				Provider: "sops",
			},
		},
	}
}

// Encode writes cfg as deterministic YAML after validating it.
func Encode(w io.Writer, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate configuration before encoding: %w", err)
	}

	encoder := yaml.NewEncoder(w)
	encoder.SetIndent(2)
	defer encoder.Close()

	if err := encoder.Encode(cfg); err != nil {
		return fmt.Errorf("encode configuration: %w", err)
	}
	return nil
}
