package config

import "testing"

func TestMetricsSelectionRequiresItsTLSDependenciesAndBoundedSizing(t *testing.T) {
	cfg := gitOpsFixture(t)
	cfg.Spec.Components = &ComponentSelection{Enabled: []string{"metrics-server"}}
	for _, dependency := range []string{"cert-manager", "kubelet-serving-tls"} {
		cfg.Spec.Components.Disabled = []string{dependency}
		if err := cfg.Validate(); err == nil {
			t.Fatal("Metrics Server accepted an explicitly disabled TLS dependency")
		}
	}
	cfg.Spec.Components.Disabled = nil
	cfg.Spec.Nodes[0].Count = MetricsServerMaxNodes + 1
	if len(cfg.validateMetricsSizing()) != 1 {
		t.Fatal("selected metrics sizing was not validated")
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("oversized metrics topology accepted")
	}
	cfg.Spec.Components = nil
	if problems := cfg.validateMetricsSizing(); len(problems) != 0 {
		t.Fatal("metrics sizing limited a topology without metrics selected")
	}
}
