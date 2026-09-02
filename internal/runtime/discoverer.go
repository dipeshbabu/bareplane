package runtime

import (
	"fmt"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/provider"
	"github.com/dipeshbabu/bareplane/internal/provider/proxmox"
)

func Discoverer(cfg config.Config, deps ProviderDependencies) (provider.Discoverer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate configuration: %w", err)
	}

	switch cfg.Spec.Provider.Type {
	case proxmox.Type:
		credentials, err := proxmox.CredentialsFromEnv(deps.LookupEnv)
		if err != nil {
			return nil, err
		}
		client, err := proxmox.NewClient(cfg.Spec.Provider.Endpoint, credentials, deps.HTTPClient)
		if err != nil {
			return nil, err
		}
		runtimeProvider, err := proxmox.NewRuntime(cfg.Spec.Provider, client)
		if err != nil {
			return nil, err
		}
		return runtimeProvider, nil
	default:
		return nil, fmt.Errorf("no discoverer is registered for provider %q", cfg.Spec.Provider.Type)
	}
}
