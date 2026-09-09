package config

import "testing"

func TestObservabilityRequiresExplicitStorageAndBoundedSizing(t *testing.T) {
	cfg := Config{Spec: Spec{Nodes: []NodeGroup{{Count: 1}}}}
	if _, err := cfg.RequireObservability(); err == nil {
		t.Fatal("implicit ephemeral history accepted")
	}
	cfg.Spec.Observability = &ObservabilityConfig{Storage: ObservabilityStorage{Mode: "ephemeral"}}
	settings, err := cfg.RequireObservability()
	if err != nil || settings.RetentionHours != 24 || cfg.Spec.Observability.RetentionHours != 0 {
		t.Fatal(settings, err)
	}
	for _, hours := range []int{-1, 1, 5, 169, int(^uint(0) >> 1)} {
		cfg.Spec.Observability.RetentionHours = hours
		if _, err := cfg.RequireObservability(); err == nil {
			t.Fatal("unbounded retention accepted")
		}
	}
	cfg.Spec.Observability.RetentionHours = 24
	for _, mode := range []string{"", "pvc", "hostPath", "persistent"} {
		cfg.Spec.Observability.Storage.Mode = mode
		if _, err := cfg.RequireObservability(); err == nil {
			t.Fatal("unsupported storage silently accepted")
		}
	}
	cfg.Spec.Observability.Storage.Mode = "ephemeral"
	for _, counts := range [][]int{{100, 1}, {int(^uint(0) >> 1)}, {-1}, {0}} {
		cfg.Spec.Nodes = nil
		for _, count := range counts {
			cfg.Spec.Nodes = append(cfg.Spec.Nodes, NodeGroup{Count: count})
		}
		if _, err := cfg.RequireObservability(); err == nil {
			t.Fatal("unsupported topology size accepted")
		}
	}
	for _, count := range []int{1, 100} {
		cfg.Spec.Nodes = []NodeGroup{{Count: count}}
		if _, err := cfg.RequireObservability(); err != nil {
			t.Fatal(err)
		}
	}
}
