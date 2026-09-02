package runtime

import (
	"context"
	"fmt"
	"net/http"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/doctor"
	"github.com/dipeshbabu/bareplane/internal/provider/proxmox"
)

type ProbeDependencies struct {
	LookupEnv  proxmox.LookupEnvFunc
	HTTPClient *http.Client
}

func ProviderProbe(deps ProbeDependencies) doctor.ProviderProbe {
	return func(ctx context.Context, cfg config.Config) doctor.Result {
		switch cfg.Spec.Provider.Type {
		case proxmox.Type:
			return probeProxmox(ctx, cfg.Spec.Provider, deps)
		default:
			return doctor.Result{
				Name:    "provider runtime",
				Status:  doctor.StatusFail,
				Message: fmt.Sprintf("no runtime probe is registered for provider %q", cfg.Spec.Provider.Type),
			}
		}
	}
}

func probeProxmox(ctx context.Context, providerConfig config.Provider, deps ProbeDependencies) doctor.Result {
	credentials, err := proxmox.CredentialsFromEnv(deps.LookupEnv)
	if err != nil {
		return doctor.Result{
			Name:    "proxmox runtime",
			Status:  doctor.StatusFail,
			Message: err.Error(),
		}
	}

	client, err := proxmox.NewClient(providerConfig.Endpoint, credentials, deps.HTTPClient)
	if err != nil {
		return doctor.Result{
			Name:    "proxmox runtime",
			Status:  doctor.StatusFail,
			Message: err.Error(),
		}
	}
	version, err := client.Version(ctx)
	if err != nil {
		return doctor.Result{
			Name:    "proxmox runtime",
			Status:  doctor.StatusFail,
			Message: err.Error(),
		}
	}

	message := fmt.Sprintf("reachable; Proxmox %s", version.Version)
	if version.Release != "" {
		message += " (release " + version.Release + ")"
	}
	return doctor.Result{
		Name:    "proxmox runtime",
		Status:  doctor.StatusPass,
		Message: message,
	}
}
