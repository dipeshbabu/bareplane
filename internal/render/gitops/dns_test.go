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

func dnsFixture(t *testing.T) config.Config {
	t.Helper()
	cfg := fixture(t)
	cfg.Spec.DNS = config.DNS{Provider: "cloudflare", Automation: &config.DNSAutomation{
		ZoneID: strings.Repeat("a", 32), ZoneName: "example.test", Domain: "apps.example.test", SourceNamespace: "apps",
		OwnerID: strings.Repeat("b", 32), TokenSecret: config.DNSSecretReference{Name: "cloudflare-api", Key: "apiToken"}}}
	return cfg
}

func TestDNSTemplateLineEndingsDoNotChangeCommentsOrOwnershipBytes(t *testing.T) {
	data, err := assets.ReadFile("assets/external-dns/upstream.yaml")
	if err != nil {
		t.Fatal(err)
	}
	lf := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	inputs := map[string][]byte{
		"lf":    lf,
		"crlf":  bytes.ReplaceAll(lf, []byte("\n"), []byte("\r\n")),
		"mixed": bytes.Replace(lf, []byte("\n"), []byte("\r\n"), 2),
	}
	for _, mode := range []string{"", "dry-run", "apply"} {
		cfg := dnsFixture(t)
		cfg.Metadata.Name = "false"
		cfg.Spec.DNS.Automation.Mode = mode
		cfg.Spec.DNS.Automation.SourceNamespace = "on"
		cfg.Spec.DNS.Automation.TokenSecret = config.DNSSecretReference{Name: "123", Key: "false", Revision: "2026-01-01"}
		expected, err := renderDNSTemplate(cfg, lf)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(expected, lf[:bytes.Index(lf, []byte("apiVersion:"))]) {
			t.Fatal("DNS provenance comments changed")
		}
		for name, input := range inputs {
			t.Run(mode+"/"+name, func(t *testing.T) {
				before := bytes.Clone(input)
				actual, err := renderDNSTemplate(cfg, input)
				if err != nil || !bytes.Equal(actual, expected) {
					t.Fatal("checkout line endings changed the DNS export", err)
				}
				if !bytes.Equal(input, before) {
					t.Fatal("renderer modified source bytes")
				}
			})
		}
	}
}

func TestDNSTemplateRefusalReturnsNoPartialOutput(t *testing.T) {
	for _, data := range []string{"invalid: [", "value: BAREPLANE_UNKNOWN\r\n"} {
		if output, err := renderDNSTemplate(dnsFixture(t), []byte(data)); err == nil || output != nil {
			t.Fatal("invalid template returned a partial payload")
		}
	}
}

func TestDNSPayloadIsPinnedServiceOnlyAndDefaultsToDryRun(t *testing.T) {
	data, err := assets.ReadFile("assets/external-dns/upstream.yaml")
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != "7795a402d8183cf48301c370181c6f4c39ccca2bac92cdd52016658d4b161dd3" {
		t.Fatalf("unreviewed DNS payload: %s", got)
	}
	cfg := dnsFixture(t)
	files := map[string][]byte{}
	if err := renderDNS(cfg, files); err != nil {
		t.Fatal(err)
	}
	data = files["components/external-dns/upstream.yaml"]
	if bytes.Contains(data, []byte("BAREPLANE_")) {
		t.Fatal("unresolved DNS input")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	count := 0
	for {
		var obj map[string]any
		if err := decoder.Decode(&obj); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		count++
		metadata := obj["metadata"].(map[string]any)
		if obj["kind"] == "Secret" || obj["kind"] == "ClusterRole" || obj["kind"] == "ClusterRoleBinding" {
			t.Fatal("DNS payload contains credentials or cluster-wide privileges")
		}
		if obj["kind"] == "Role" {
			if metadata["namespace"] != "apps" || !reflect.DeepEqual(obj["rules"], []any{map[string]any{
				"apiGroups": []any{""}, "resources": []any{"services"}, "verbs": []any{"get", "list", "watch"},
			}}) {
				t.Fatal("DNS reader role exceeds its application Service scope")
			}
		}
		if obj["kind"] == "Deployment" {
			spec := obj["spec"].(map[string]any)
			if spec["replicas"] != 1 || spec["strategy"].(map[string]any)["type"] != "Recreate" {
				t.Fatal("DNS controller permits overlapping writers")
			}
			pod := spec["template"].(map[string]any)["spec"].(map[string]any)
			container := pod["containers"].([]any)[0].(map[string]any)
			if container["image"] != "registry.k8s.io/external-dns/external-dns:v"+ExternalDNSVersion {
				t.Fatal("unpinned DNS controller image")
			}
			args := fmt.Sprint(container["args"])
			for _, argument := range []string{"--source=service", "--service-type-filter=LoadBalancer", "--namespace=apps",
				"--domain-filter=apps.example.test", "--zone-id-filter=" + strings.Repeat("a", 32), "--policy=upsert-only",
				"--registry=txt", "--txt-owner-id=" + strings.Repeat("b", 32), "--dry-run"} {
				if !strings.Contains(args, argument) {
					t.Fatalf("missing scoped DNS argument: %s", argument)
				}
			}
			if strings.Contains(args, "--dry-run=") {
				t.Fatal("DNS renderer emitted unsupported boolean assignment syntax")
			}
			env := container["env"].([]any)[0].(map[string]any)
			if env["name"] != "CF_API_TOKEN" || env["value"] != nil || env["valueFrom"] == nil {
				t.Fatal("DNS credential is not a Secret reference")
			}
		}
	}
	if count != 5 {
		t.Fatalf("unexpected DNS component size: %d", count)
	}
}

func TestDNSInputStringsKeepTheirTypesAndApplyRequiresExplicitMode(t *testing.T) {
	cfg := dnsFixture(t)
	cfg.Metadata.Name = "false"
	cfg.Spec.DNS.Automation.SourceNamespace = "on"
	cfg.Spec.DNS.Automation.TokenSecret = config.DNSSecretReference{Name: "123", Key: "false", Revision: "2026-01-01"}
	cfg.Spec.DNS.Automation.Mode = "apply"
	files := map[string][]byte{}
	if err := renderDNS(cfg, files); err != nil {
		t.Fatal(err)
	}
	data := string(files["components/external-dns/upstream.yaml"])
	for _, expected := range []string{`bareplane.io/cluster: "false"`, `namespace: "on"`, `name: "123"`, `key: "false"`,
		`bareplane.io/credentials-revision: "2026-01-01"`, `"--namespace=on"`} {
		if !strings.Contains(data, expected) {
			t.Fatalf("DNS scalar or explicit mode lost its type: %s", expected)
		}
	}
	if strings.Contains(data, "--dry-run") {
		t.Fatal("explicit apply retained the dry-run switch")
	}
	cfg.Spec.DNS.Automation = nil
	if err := renderDNS(cfg, map[string][]byte{}); err == nil {
		t.Fatal("unconfigured DNS request silently rendered")
	}
}

func TestDNSFullRenderIsOptInAndRejectsUnconfiguredAutomation(t *testing.T) {
	cfg := fixture(t)
	baseline, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := baseline["components/external-dns/upstream.yaml"]; exists {
		t.Fatal("manual DNS enabled automation")
	}
	cfg.Spec.DNS.Provider = "cloudflare"
	if files, err := Render(cfg); err == nil || files != nil {
		t.Fatal("unconfigured DNS emitted a partial platform tree")
	}
	cfg = dnsFixture(t)
	files, err := Render(cfg)
	if err != nil || len(files["components/external-dns/upstream.yaml"]) == 0 {
		t.Fatalf("configured DNS failed to render: %v", err)
	}
	var app map[string]any
	if err := yaml.Unmarshal(files[cfg.Spec.GitOps.RootPath+"/applications/external-dns.yaml"], &app); err != nil {
		t.Fatal(err)
	}
	if app["spec"].(map[string]any)["destination"].(map[string]any)["namespace"] != "external-dns" {
		t.Fatal("DNS controller targets the wrong namespace")
	}
}
