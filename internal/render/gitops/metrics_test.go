package gitops

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

func TestMetricsRenderingUsesItsNamespaceAndExcludesBootstrapPKI(t *testing.T) {
	cfg := fixture(t)
	base, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := base["components/metrics-server/upstream.yaml"]; exists {
		t.Fatal("metrics enabled implicitly")
	}
	cfg.Spec.Components = &config.ComponentSelection{Enabled: []string{"metrics-server"}}
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		if strings.HasPrefix(name, "components/kubelet") || bytes.Contains(data, []byte("BAREPLANE_METRICS_")) {
			t.Fatal("bootstrap PKI or unresolved resource markers entered GitOps")
		}
	}
	var app map[string]any
	if err := yaml.Unmarshal(files[cfg.Spec.GitOps.RootPath+"/applications/metrics-server.yaml"], &app); err != nil {
		t.Fatal(err)
	}
	spec := app["spec"].(map[string]any)
	if spec["destination"].(map[string]any)["namespace"] != "metrics-server" {
		t.Fatal("metrics targets the wrong namespace")
	}
	ignored := spec["ignoreDifferences"].([]any)
	if len(ignored) != 1 || !reflect.DeepEqual(ignored[0].(map[string]any)["jsonPointers"], []any{"/spec/caBundle"}) {
		t.Fatal("metrics ignored fields beyond its injected trust bundle")
	}
	resolved, err := cfg.ResolvePlatform()
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range resolved.GitOpsComponents() {
		if component.ID == "kubelet-serving-tls" {
			t.Fatal("bootstrap TLS changed ownership")
		}
	}
}

func TestMetricsAssetHasVerifiedTLSAndNoEmbeddedSecrets(t *testing.T) {
	data, err := assets.ReadFile("assets/metrics-server/upstream.yaml")
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != "bd21428c6a8f1d7d12c9f7e1a3c4c10d4a3dcacb4d7f0f4da5933701fdd2b5a0" {
		t.Fatalf("unreviewed Metrics Server asset: %s", got)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	count, deployment, apiService, certificate := 0, false, false, false
	for {
		var obj map[string]any
		if err := decoder.Decode(&obj); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		count++
		metadata := obj["metadata"].(map[string]any)
		if obj["kind"] == "Secret" {
			t.Fatal("Metrics Server embedded private material")
		}
		if metadata["namespace"] == "kube-system" && obj["kind"] != "RoleBinding" {
			t.Fatal("Metrics Server workload claimed the bootstrap namespace")
		}
		switch obj["kind"] {
		case "Deployment":
			deployment = true
			pod := obj["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			container := pod["containers"].([]any)[0].(map[string]any)
			if container["image"] != "registry.k8s.io/metrics-server/metrics-server:v"+MetricsServerVersion {
				t.Fatal("unpinned Metrics Server image")
			}
			ca, address, cert, key := false, false, false, false
			for _, value := range container["args"].([]any) {
				argument := value.(string)
				if strings.Contains(argument, "insecure") {
					t.Fatal("TLS verification bypass present")
				}
				ca = ca || argument == "--kubelet-certificate-authority=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
				address = address || argument == "--kubelet-preferred-address-types=InternalIP"
				cert = cert || argument == "--tls-cert-file=/etc/metrics-server/tls/tls.crt"
				key = key || argument == "--tls-private-key-file=/etc/metrics-server/tls/tls.key"
			}
			if !ca || !address || !cert || !key || len(pod["tolerations"].([]any)) != 1 {
				t.Fatal("verified TLS or single-node scheduling contract missing")
			}
		case "APIService":
			apiService = true
			if obj["spec"].(map[string]any)["insecureSkipTLSVerify"] != false || metadata["annotations"].(map[string]any)["cert-manager.io/inject-ca-from"] != "metrics-server/metrics-server-serving" {
				t.Fatal("aggregation TLS trust is not configured")
			}
		case "Certificate":
			certificate = true
			spec := obj["spec"].(map[string]any)
			if len(spec["usages"].([]any)) != 2 || fmt.Sprint(spec["usages"]) != "[digital signature server auth]" || spec["isCA"] == true {
				t.Fatal("serving certificate grants unrelated privileges")
			}
		}
	}
	if count != 12 || !deployment || !apiService || !certificate {
		t.Fatal("Metrics Server payload is incomplete")
	}
}

func TestMetricsResourceSizingIsBoundedAndFollowsUpstreamEnvelope(t *testing.T) {
	for _, test := range []struct {
		nodes              int
		cpu, memory, limit string
	}{
		{1, "100m", "200Mi", "400Mi"}, {100, "100m", "200Mi", "400Mi"},
		{101, "101m", "202Mi", "404Mi"}, {5000, "5000m", "10000Mi", "20000Mi"},
	} {
		cfg := config.Config{Spec: config.Spec{Nodes: []config.NodeGroup{{Count: test.nodes}}}}
		cpu, memory, limit, err := metricsResources(cfg)
		if err != nil || cpu != test.cpu || memory != test.memory || limit != test.limit {
			t.Fatalf("incorrect sizing for %d nodes: %s %s %s %v", test.nodes, cpu, memory, limit, err)
		}
	}
	for _, nodes := range [][]config.NodeGroup{nil, {{Count: 0}}, {{Count: -1}}, {{Count: 5001}},
		{{Count: 5000}, {Count: 1}}, {{Count: int(^uint(0) >> 1)}}} {
		if _, _, _, err := metricsResources(config.Config{Spec: config.Spec{Nodes: nodes}}); err == nil {
			t.Fatal("invalid or overflowing metrics resource envelope accepted")
		}
	}
}

func TestMetricsSizingReplacesEveryExactMarkerWithoutPrefixCorruption(t *testing.T) {
	cfg := config.Config{Spec: config.Spec{Nodes: []config.NodeGroup{{Count: 1}}}}
	name := "components/metrics-server/upstream.yaml"
	files := map[string][]byte{name: []byte("BAREPLANE_METRICS_CPU BAREPLANE_METRICS_MEMORY BAREPLANE_METRICS_MEMORY_LIMIT")}
	if err := renderMetricsResources(cfg, files); err != nil {
		t.Fatal(err)
	}
	if string(files[name]) != "100m 200Mi 400Mi" || strings.Contains(string(files[name]), "BAREPLANE_") {
		t.Fatal("overlapping resource markers were not replaced safely")
	}
	if err := renderMetricsResources(cfg, map[string][]byte{}); err == nil {
		t.Fatal("missing payload accepted")
	}
}
