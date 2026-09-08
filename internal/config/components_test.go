package config

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestComponentSelectionIsOptionalStrictAndDoesNotInstallUnavailableCapabilities(t *testing.T) {
	cfg := gitOpsFixture(t)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Spec.Components = &ComponentSelection{Enabled: []string{"metrics-server"}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	resolved, err := cfg.ResolvePlatform()
	if err != nil || resolved.RequireAvailable(false) == nil {
		t.Fatal("unavailable selection was treated as implemented")
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(strings.NewReader(string(data)))
	if err != nil || !reflect.DeepEqual(loaded.Spec.Components, cfg.Spec.Components) {
		t.Fatalf("selection round trip failed: %v", err)
	}
	bad := strings.Replace(string(data), "components:\n", "components:\n        token: PRIVATE-SENTINEL\n", 1)
	if _, err := Load(strings.NewReader(bad)); err == nil || strings.Contains(err.Error(), "PRIVATE-SENTINEL") {
		t.Fatalf("unsafe schema result: %v", err)
	}
}

func TestComponentSelectionRejectsUnknownConflictingUnsafeAndRequiredDisables(t *testing.T) {
	for _, selection := range []*ComponentSelection{
		{Enabled: []string{"unknown"}}, {Disabled: []string{"unknown"}},
		{Enabled: []string{"metrics-server", "metrics-server"}}, {Disabled: []string{"storage", "storage"}},
		{Enabled: []string{"metrics-server"}, Disabled: []string{"metrics-server"}},
		{Enabled: []string{"vault", "secrets-sops"}},
		{Disabled: []string{"argocd"}}, {Disabled: []string{"cilium"}},
		{Enabled: []string{"../PRIVATE-SENTINEL"}}, {Enabled: []string{"trailing-"}},
	} {
		cfg := gitOpsFixture(t)
		cfg.Spec.Components = selection
		if err := cfg.Validate(); err == nil || strings.Contains(err.Error(), "PRIVATE-SENTINEL") {
			t.Fatalf("invalid selection accepted/exposed: %v", err)
		}
	}
	cfg := gitOpsFixture(t)
	cfg.Spec.Components = &ComponentSelection{Enabled: make([]string, 513)}
	if err := cfg.Validate(); err == nil {
		t.Fatal("unbounded selection accepted")
	}
}

func TestConfigFlagsUseTheSameRegistryAndCannotBypassDisabledDependencies(t *testing.T) {
	for _, change := range []func(*Config){
		func(c *Config) { c.Spec.Features.GPU = true }, func(c *Config) { c.Spec.Features.Observability = true },
		func(c *Config) { c.Spec.Secrets.Provider = "vault" },
	} {
		cfg := gitOpsFixture(t)
		change(&cfg)
		resolved, err := cfg.ResolvePlatform()
		if err != nil || resolved.RequireAvailable(false) == nil {
			t.Fatal("unimplemented flag bypassed availability")
		}
	}
	cfg := gitOpsFixture(t)
	cfg.Spec.Features.Observability = true
	cfg.Spec.Components = &ComponentSelection{Disabled: []string{"observability"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("feature flag bypassed explicit disable")
	}
	cfg = gitOpsFixture(t)
	cfg.Spec.Profiles = []string{"ai"}
	cfg.Spec.Components = &ComponentSelection{Disabled: []string{"gpu-drivers"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("AI profile omitted a required host dependency")
	}
}

func TestExplicitBootstrapOnlyFixturePreservesRequiredOwnersAndCopiesSelection(t *testing.T) {
	cfg := gitOpsFixture(t)
	cfg.Spec.Features = Features{}
	cfg.Spec.Profiles = []string{"minimal"}
	cfg.Spec.DNS.Provider = "manual"
	cfg.Spec.Secrets.Provider = "sops"
	cfg.Spec.Components = &ComponentSelection{Disabled: []string{"metrics-server", "cert-manager", "observability", "external-dns", "storage", "secrets-sops", "vault"}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	resolved, err := cfg.ResolvePlatform()
	if err != nil || resolved.RequireAvailable(false) != nil || len(resolved.GitOpsComponents()) != 1 || resolved.GitOpsComponents()[0].ID != "argocd" {
		t.Fatalf("baseline changed: %+v %v", resolved, err)
	}
	selection := cfg.PlatformSelection()
	selection.Disabled[0] = "argocd"
	selection.Profiles[0] = "ai"
	if cfg.Spec.Components.Disabled[0] != "metrics-server" || cfg.Spec.Profiles[0] != "minimal" {
		t.Fatal("selection aliases user configuration")
	}
}
