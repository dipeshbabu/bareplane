package platform

func Builtin() (*Registry, error) {
	components := []Component{
		{ID: "cilium", Owner: Bootstrap, Status: Implemented, DefaultEnabled: true, BaseWave: -50},
		{ID: "argocd", Dependencies: []string{"cilium"}, Owner: GitOps, Status: Implemented, DefaultEnabled: true, Profiles: []string{"minimal"}, BaseWave: -30},
		{ID: "cert-manager", Dependencies: []string{"argocd"}, Owner: GitOps, Status: Implemented, Namespace: "cert-manager", BaseWave: -20},
		{ID: "kubelet-serving-tls", Dependencies: []string{"cilium"}, Owner: Bootstrap, Status: Implemented, BaseWave: -40},
		{ID: "metrics-server", Dependencies: []string{"argocd", "cert-manager", "kubelet-serving-tls"}, Owner: GitOps, Status: Implemented, Namespace: "metrics-server", BaseWave: -10},
		{ID: "external-dns", Dependencies: []string{"argocd"}, Owner: GitOps, Status: Implemented, Namespace: "external-dns", BaseWave: -20},
		{ID: "secrets-sops", Dependencies: []string{"argocd"}, Conflicts: []string{"vault"}, Owner: GitOps, Status: Implemented, BaseWave: -20},
		{ID: "observability", Dependencies: []string{"argocd"}, Owner: GitOps, Status: Implemented, Namespace: "observability", BaseWave: -20},
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
	for _, id := range []string{"vault"} {
		component := Component{ID: id, Dependencies: []string{"argocd"}, Owner: GitOps, Status: Unavailable, BaseWave: -20}
		if id == "vault" {
			component.Conflicts = []string{"secrets-sops"}
		}
		components = append(components, component)
	}
	return New(components)
}
