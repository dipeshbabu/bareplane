package platform

import "github.com/dipeshbabu/bareplane/internal/config"

func Builtin() (*Registry, error) {
	components := []Component{
		{ID: "cilium", Owner: Bootstrap, Status: Implemented, DefaultEnabled: true, BaseWave: -50},
		{ID: "argocd", Dependencies: []string{"cilium"}, Owner: GitOps, Status: Implemented, DefaultEnabled: true, Profiles: []string{"minimal"}, BaseWave: -30},
		{ID: "gpu-passthrough", Owner: Infrastructure, Status: Unavailable, BaseWave: -60},
		{ID: "gpu-drivers", Dependencies: []string{"gpu-passthrough"}, Owner: Bootstrap, Status: Unavailable, BaseWave: -50},
		{ID: "node-feature-discovery", Dependencies: []string{"argocd"}, Owner: GitOps, Status: Unavailable, BaseWave: -20},
		{ID: "gpu-scheduling", Dependencies: []string{"argocd", "gpu-drivers", "node-feature-discovery"}, Owner: GitOps, Status: Unavailable, Profiles: []string{"ai"}, BaseWave: -10},
		{ID: "model-cache", Dependencies: []string{"storage"}, Owner: GitOps, Status: Unavailable, BaseWave: -10},
		{ID: "model-serving", Dependencies: []string{"gpu-scheduling", "model-cache"}, Owner: GitOps, Status: Unavailable, Profiles: []string{"ai"}, BaseWave: 0},
		{ID: "ray", Dependencies: []string{"argocd"}, Owner: GitOps, Status: Unavailable, Profiles: []string{"ai"}, BaseWave: -10},
		{ID: "jupyter", Dependencies: []string{"argocd", "storage"}, Owner: GitOps, Status: Unavailable, BaseWave: 0},
		{ID: "open-webui", Dependencies: []string{"model-serving"}, Owner: GitOps, Status: Unavailable, BaseWave: 1},
		{ID: "storage", Dependencies: []string{"argocd"}, Owner: GitOps, Status: Unavailable, BaseWave: -20},
		{ID: "data-recovery", Dependencies: []string{"storage"}, Owner: GitOps, Status: Unavailable, BaseWave: -10},
		{ID: "postgres", Dependencies: []string{"storage", "data-recovery"}, Owner: GitOps, Status: Unavailable, Profiles: []string{"data"}, BaseWave: -10},
		{ID: "kafka", Dependencies: []string{"storage", "data-recovery"}, Owner: GitOps, Status: Unavailable, Profiles: []string{"data"}, BaseWave: -10},
		{ID: "spark", Dependencies: []string{"kafka", "storage"}, Owner: GitOps, Status: Unavailable, Profiles: []string{"data"}, BaseWave: 0},
		{ID: "airflow", Dependencies: []string{"postgres"}, Owner: GitOps, Status: Unavailable, Profiles: []string{"data"}, BaseWave: 0},
		{ID: "trino", Dependencies: []string{"postgres", "storage"}, Owner: GitOps, Status: Unavailable, Profiles: []string{"data"}, BaseWave: 0},
		{ID: "flink", Dependencies: []string{"kafka", "storage"}, Owner: GitOps, Status: Unavailable, BaseWave: 0},
		{ID: "clickhouse", Dependencies: []string{"storage", "data-recovery"}, Owner: GitOps, Status: Unavailable, BaseWave: -9},
		{ID: "redis", Dependencies: []string{"storage", "data-recovery"}, Owner: GitOps, Status: Unavailable, BaseWave: -9},
		{ID: "superset", Dependencies: []string{"postgres", "trino"}, Owner: GitOps, Status: Unavailable, BaseWave: 1},
	}
	for _, id := range []string{"metrics-server", "cert-manager", "external-dns", "observability", "secrets-sops", "vault"} {
		component := Component{ID: id, Dependencies: []string{"argocd"}, Owner: GitOps, Status: Unavailable, BaseWave: -20}
		if id == "secrets-sops" {
			component.Conflicts = []string{"vault"}
		}
		if id == "vault" {
			component.Conflicts = []string{"secrets-sops"}
		}
		components = append(components, component)
	}
	return New(components)
}

// ResolveConfig maps existing config choices to one graph. The default SOPS
// value remains an extension boundary until secret delivery is implemented;
// it does not silently request a currently unavailable secret controller.
func ResolveConfig(cfg config.Config) (Resolution, error) {
	registry, err := Builtin()
	if err != nil {
		return Resolution{}, err
	}
	selection := Selection{Profiles: cfg.Spec.Profiles}
	if cfg.Spec.Features.GPU {
		selection.Enabled = append(selection.Enabled, "gpu-scheduling")
	}
	if cfg.Spec.Features.Observability {
		selection.Enabled = append(selection.Enabled, "observability")
	}
	if cfg.Spec.DNS.Provider == "cloudflare" {
		selection.Enabled = append(selection.Enabled, "external-dns")
	}
	if cfg.Spec.Secrets.Provider == "vault" {
		selection.Enabled = append(selection.Enabled, "vault")
	}
	return registry.Resolve(selection)
}
