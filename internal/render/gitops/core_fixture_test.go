package gitops

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
)

func coreConfig(t *testing.T) config.Config {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "core-fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestCoreFixtureComposesOnlyAvailableComponents(t *testing.T) {
	cfg := coreConfig(t)
	resolution, err := cfg.ResolvePlatform()
	if err != nil || resolution.RequireAvailable(false) != nil {
		t.Fatalf("core component closure is unavailable: %v", err)
	}
	var names []string
	for _, component := range resolution.GitOpsComponents() {
		names = append(names, component.ID)
	}
	sort.Strings(names)
	want := []string{"argocd", "cert-manager", "metrics-server", "observability", "storage"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("unexpected core component set: %v", names)
	}
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for name := range files {
		for _, forbidden := range []string{"components/cilium/", "components/kubelet-serving-tls/", "components/vault/", "components/secrets-sops/", "components/external-dns/"} {
			if strings.HasPrefix(name, forbidden) {
				t.Fatalf("core fixture changed ownership or opted into external integration: %s", name)
			}
		}
	}
}

func TestPublishedCoreFixtureMatchesSharedComponentPayloads(t *testing.T) {
	cfg := coreConfig(t)
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("..", "..", "..")
	count := 0
	for name, expected := range files {
		if !strings.HasPrefix(name, "components/") && !strings.HasPrefix(name, cfg.Spec.GitOps.RootPath+"/") {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(name))
		if strings.HasPrefix(name, cfg.Spec.GitOps.RootPath+"/") {
			count++
			if os.Getenv("BAREPLANE_UPDATE_CORE_FIXTURE") == "1" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, expected, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(bytes.ReplaceAll(actual, []byte("\r\n"), []byte("\n")), expected) {
			t.Fatalf("core fixture changes a shared payload or needs explicit regeneration: %s: %v", name, err)
		}
	}
	if count != 6 {
		t.Fatalf("unexpected core root file count: %d", count)
	}
}

func TestCoreRequiresExplicitContractsAndRejectsUnavailableBreadth(t *testing.T) {
	for name, change := range map[string]func(*config.Config){
		"storage contract":              func(c *config.Config) { c.Spec.Storage = nil },
		"observability acknowledgement": func(c *config.Config) { c.Spec.Observability = nil },
		"AI capability":                 func(c *config.Config) { c.Spec.Components.Enabled = append(c.Spec.Components.Enabled, "ray") },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := coreConfig(t)
			change(&cfg)
			files, err := Render(cfg)
			if err == nil || files != nil {
				t.Fatalf("incomplete or unavailable core selection emitted partial files: %v", err)
			}
		})
	}
}

func TestCoreCompatibilityRecordMatchesReviewedRendererPins(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "compatibility.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Core map[string]string `json:"core_components"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"argocd": ArgoVersion, "cert-manager": CertManagerVersion,
		"metrics-server": MetricsServerVersion, "prometheus": PrometheusVersion,
		"kube-state-metrics": KubeStateMetricsVersion, "external-dns": ExternalDNSVersion,
		"sops": SOPSVersion, "external-secrets": VaultOperatorVersion, "storage-inspector-image": StorageCheckImage}
	if !reflect.DeepEqual(record.Core, want) {
		t.Fatalf("core compatibility record does not match actual renderer pins: %v", record.Core)
	}
}

func TestCoreComposesWithExplicitExternalIntegrations(t *testing.T) {
	base, err := Render(coreConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"external-dns", "sops", "vault"} {
		t.Run(backend, func(t *testing.T) {
			cfg := coreConfig(t)
			data, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", backend+"-fixture.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			integration, err := config.Load(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			id := backend
			if backend == "external-dns" {
				cfg.Spec.DNS = integration.Spec.DNS
			} else {
				cfg.Spec.Secrets = integration.Spec.Secrets
				if backend == "sops" {
					id = "secrets-sops"
				}
			}
			var disabled []string
			for _, item := range cfg.Spec.Components.Disabled {
				if item != id {
					disabled = append(disabled, item)
				}
			}
			cfg.Spec.Components.Disabled = disabled
			files, err := Render(cfg)
			if err != nil {
				t.Fatal(err)
			}
			for name, expected := range base {
				if !strings.HasPrefix(name, "components/") || strings.HasPrefix(name, "components/argocd/") {
					continue
				}
				if !bytes.Equal(files[name], expected) {
					t.Fatalf("integration rewrote a shared core component: %s", name)
				}
			}
			if _, present := files[cfg.Spec.GitOps.RootPath+"/applications/"+id+".yaml"]; !present {
				t.Fatal("integration disappeared from the resolved core graph")
			}
		})
	}
}
