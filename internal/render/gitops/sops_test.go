package gitops

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

func sopsConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := fixture(t)
	cfg.Spec.Components = nil
	cfg.Spec.Secrets.SOPS = &config.SOPSConfig{
		AgeKey:        &config.SOPSKeyReference{Name: "bareplane-sops-age", Key: "keys.txt"},
		PGPKey:        &config.SOPSKeyReference{Name: "bareplane-sops-pgp", Key: "private.asc"},
		PGPPassphrase: &config.SOPSKeyReference{Name: "bareplane-sops-pass", Key: "passphrase"},
		Namespaces:    []string{"workloads", "apps"},
	}
	return cfg
}

func TestSOPSPreservesSingleArgoOwnerAndColdInstallationInventory(t *testing.T) {
	cfg := sopsConfig(t)
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for name := range files {
		if strings.HasPrefix(name, "components/argocd/") {
			count++
		}
		if strings.HasPrefix(name, "components/secrets-sops/") && name != "components/secrets-sops/kustomization.yaml" && name != "components/secrets-sops/ownership.yaml" {
			t.Fatalf("SOPS introduced another Argo owner: %s", name)
		}
	}
	if count != 6 {
		t.Fatalf("cold installer input inventory changed: %d", count)
	}
	var cm, kustomize map[string]any
	if err := yaml.Unmarshal(files["components/argocd/configuration.yaml"], &cm); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(files["components/argocd/kustomization.yaml"], &kustomize); err != nil {
		t.Fatal(err)
	}
	data := cm["data"].(map[string]any)
	var policy map[string]any
	if err := json.Unmarshal([]byte(data["sops-policy.json"].(string)), &policy); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(policy["namespaces"], []any{"apps", "workloads"}) {
		t.Fatal(policy)
	}
	var plugin map[string]any
	if err := yaml.Unmarshal([]byte(data["sops-plugin.yaml"].(string)), &plugin); err != nil {
		t.Fatal(err)
	}
	pluginSpec := plugin["spec"].(map[string]any)
	if pluginSpec["provideGitCreds"] != false || pluginSpec["preserveFileMode"] != false || pluginSpec["discover"] != nil {
		t.Fatal("implicit or privileged plugin")
	}
	command := pluginSpec["generate"].(map[string]any)["command"].([]any)
	if !reflect.DeepEqual(command, []any{"/usr/bin/python3", "-I", "/home/argocd/cmp-server/config/generate.py"}) {
		t.Fatal(command)
	}
	patches := kustomize["patches"].([]any)
	var patch map[string]any
	if err := yaml.Unmarshal([]byte(patches[len(patches)-1].(map[string]any)["patch"].(string)), &patch); err != nil {
		t.Fatal(err)
	}
	if patch["metadata"].(map[string]any)["name"] != "argocd-repo-server" {
		t.Fatal("wrong deployment patch")
	}
	pod := patch["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	if pod["automountServiceAccountToken"] != false {
		t.Fatal("sidecar acquired API credentials")
	}
	containers := pod["containers"].([]any)
	if len(containers) != 1 || containers[0].(map[string]any)["image"] != "ghcr.io/getsops/sops:v"+SOPSVersion {
		t.Fatal(containers)
	}
	for _, raw := range pod["volumes"].([]any) {
		volume := raw.(map[string]any)
		if secret, ok := volume["secret"].(map[string]any); ok {
			if secret["optional"] != true || secret["defaultMode"] != 0o440 {
				t.Fatal("keys block initial Argo namespace creation or have unsafe mode")
			}
		}
		if volume["name"] == "sops-tmp" && volume["emptyDir"].(map[string]any)["medium"] != "Memory" {
			t.Fatal("plaintext workspace is not memory backed")
		}
	}
	if bytes.Contains(files["components/secrets-sops/ownership.yaml"], []byte("kind: Deployment")) {
		t.Fatal("competing Argo Deployment owner")
	}
}

func TestSOPSIsExplicitDeterministicAndRevisionRollsKeys(t *testing.T) {
	cfg := sopsConfig(t)
	first, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Spec.Secrets.SOPS.Namespaces = []string{"apps", "workloads"}
	second, err := Render(cfg)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("namespace ordering changed public export", err)
	}
	cfg.Spec.Secrets.SOPS.Revision = "rotated"
	third, err := Render(cfg)
	if err != nil || bytes.Equal(second["components/argocd/kustomization.yaml"], third["components/argocd/kustomization.yaml"]) {
		t.Fatal("key revision did not roll repo-server", err)
	}
	cfg.Spec.Secrets.SOPS = nil
	cfg.Spec.Components = &config.ComponentSelection{Enabled: []string{"secrets-sops"}}
	if files, err := Render(cfg); err == nil || files != nil {
		t.Fatal("missing key references produced a partial export")
	}
	cfg.Spec.Components = nil
	plain, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range plain {
		if bytes.Contains(data, []byte("bareplane-sops")) {
			t.Fatal("default provider activated SOPS plugin")
		}
	}
}
