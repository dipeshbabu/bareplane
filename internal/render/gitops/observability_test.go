package gitops

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

func observabilityConfig(t *testing.T) config.Config {
	t.Helper()
	data, err := os.ReadFile("../../../examples/observability-fixture.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestObservabilityPinsBoundsAndReadOnlyCollectorScope(t *testing.T) {
	for name, digest := range map[string]string{
		"collector.yaml":     "0e0b1b4e2b68248fc718b099ae51b8994b60bafb31a50a8ab5dd29aec45ddb2b",
		"prometheus.yaml":    "2e0ae3cc7aeeaae4c770576f96703c3e38f8d1b1bf06e14257997c243040acc9",
		"configuration.yaml": "2907a9dc5eb0431714213a5044dc56bb56955d273591309d07100a63df516938",
	} {
		data, err := assets.ReadFile("assets/observability/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")))) != digest {
			t.Fatal("unreviewed observability payload", name)
		}
	}
	cfg := observabilityConfig(t)
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, name := range []string{"collector.yaml", "configuration.yaml", "prometheus.yaml"} {
		decoder := yaml.NewDecoder(bytes.NewReader(files["components/observability/"+name]))
		for {
			var obj map[string]any
			if err := decoder.Decode(&obj); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			kind := obj["kind"].(string)
			counts[kind]++
			if kind == "Secret" || kind == "PersistentVolumeClaim" || kind == "DaemonSet" {
				t.Fatal("baseline introduced credential, persistence or host workloads")
			}
			if kind == "ClusterRole" {
				if !reflect.DeepEqual(obj["rules"], []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"nodes", "pods", "namespaces"}, "verbs": []any{"get", "list", "watch"}},
					map[string]any{"apiGroups": []any{"apps"}, "resources": []any{"deployments"}, "verbs": []any{"get", "list", "watch"}}}) {
					t.Fatal("collector permissions changed")
				}
			}
			if kind == "Service" && obj["spec"].(map[string]any)["type"] != "ClusterIP" {
				t.Fatal("observability exposed externally")
			}
			if kind == "Deployment" {
				pod := obj["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
				for _, raw := range pod["containers"].([]any) {
					container := raw.(map[string]any)
					if container["resources"] == nil || container["securityContext"].(map[string]any)["readOnlyRootFilesystem"] != true {
						t.Fatal("unbounded or writable workload")
					}
					if container["name"] == "prometheus" {
						if container["image"] != "quay.io/prometheus/prometheus:v"+PrometheusVersion || pod["automountServiceAccountToken"] != false {
							t.Fatal("Prometheus pin or API isolation changed")
						}
						args := fmt.Sprint(container["args"])
						for _, forbidden := range []string{"enable-admin-api", "enable-lifecycle", "remote-write-receiver"} {
							if strings.Contains(args, forbidden) {
								t.Fatal("unsafe Prometheus API enabled")
							}
						}
					}
				}
			}
			if kind == "ConfigMap" {
				var prometheus map[string]any
				if err := yaml.Unmarshal([]byte(obj["data"].(map[string]any)["prometheus.yml"].(string)), &prometheus); err != nil {
					t.Fatal(err)
				}
				retention := prometheus["storage"].(map[string]any)["tsdb"].(map[string]any)["retention"]
				if !reflect.DeepEqual(retention, map[string]any{"time": "24h", "size": "1GB"}) {
					t.Fatal(retention)
				}
				if prometheus["remote_write"] != nil || len(prometheus["scrape_configs"].([]any)) != 2 {
					t.Fatal("unexpected metrics destinations")
				}
			}
		}
	}
	if counts["Deployment"] != 2 || counts["ConfigMap"] != 1 || counts["Service"] != 2 {
		t.Fatal(counts)
	}
}

func TestObservabilityRequiresExplicitEphemeralChoiceAndRollsConfiguration(t *testing.T) {
	cfg := observabilityConfig(t)
	first, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Spec.Observability.RetentionHours = 48
	second, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first["components/observability/configuration.yaml"], second["components/observability/configuration.yaml"]) ||
		bytes.Equal(first["components/observability/prometheus.yaml"], second["components/observability/prometheus.yaml"]) {
		t.Fatal("configuration change did not roll the consumer")
	}
	cfg.Spec.Observability = nil
	if files, err := Render(cfg); err == nil || files != nil {
		t.Fatal("implicit ephemeral history was rendered")
	}
}
