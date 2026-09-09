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

func vaultConfig(t *testing.T) config.Config {
	t.Helper()
	data, err := os.ReadFile("../../../examples/vault-fixture.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func vaultDocuments(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var result []map[string]any
	for {
		var obj map[string]any
		if err := decoder.Decode(&obj); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		result = append(result, obj)
	}
	return result
}

func TestVaultPayloadIsPinnedScopedAndContainsNoRuntimeSecrets(t *testing.T) {
	for name, expected := range map[string]string{"crds.yaml": "eb993d5e662858a6ae4b46a6ee453db7f9a18242ff72523bcdffbbbe39526823", "controller.yaml": "074e45e0cac6c5dfbbf6c803ee35a99e928118ca27c16eca1a5394428db93406"} {
		data, err := assets.ReadFile("assets/vault/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")))); got != expected {
			t.Fatalf("unreviewed Vault %s: %s", name, got)
		}
	}
	cfg := vaultConfig(t)
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	objects := vaultDocuments(t, files["components/vault/crds.yaml"])
	if len(objects) != 3 {
		t.Fatal("unexpected CRD footprint")
	}
	for _, obj := range objects {
		spec := obj["spec"].(map[string]any)
		if spec["scope"] != "Namespaced" || len(spec["versions"].([]any)) != 1 || !reflect.DeepEqual(spec["conversion"], map[string]any{"strategy": "None"}) {
			t.Fatal("cluster scope or conversion webhook introduced")
		}
	}
	objects = append(objects, vaultDocuments(t, files["components/vault/controller.yaml"])...)
	objects = append(objects, vaultDocuments(t, files["components/vault/integration.yaml"])...)
	for _, obj := range objects {
		if obj["kind"] == "Secret" || obj["kind"] == "ClusterRole" || obj["kind"] == "ClusterRoleBinding" {
			t.Fatal("runtime values or cluster-wide privileges introduced")
		}
		if obj["kind"] == "Role" {
			for _, raw := range obj["rules"].([]any) {
				rule := raw.(map[string]any)
				verbs := fmt.Sprint(rule["verbs"])
				if strings.Contains(verbs, "delete") || strings.Contains(verbs, "*") {
					t.Fatal("destructive or unrestricted RBAC")
				}
				if fmt.Sprint(rule["resources"]) == "[serviceaccounts/token]" && !reflect.DeepEqual(rule["resourceNames"], []any{"bareplane-vault-reader"}) {
					t.Fatal("unrestricted token impersonation")
				}
			}
		}
		if obj["kind"] == "Deployment" {
			pod := obj["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			container := pod["containers"].([]any)[0].(map[string]any)
			args := fmt.Sprint(container["args"])
			for _, required := range []string{"--namespace=vault-workloads", "--controller-class=bareplane-vault-", "--enable-cluster-store-reconciler=false", "--enable-push-secret-reconciler=false", "--enable-vault-token-cache=false"} {
				if !strings.Contains(args, required) {
					t.Fatal("missing scoped Vault argument", required)
				}
			}
			if container["image"] != "ghcr.io/external-secrets/external-secrets:v"+VaultOperatorVersion || container["env"] != nil {
				t.Fatal("unpinned image or inline credentials")
			}
		}
		if obj["kind"] == "ExternalSecret" {
			spec := obj["spec"].(map[string]any)
			target := spec["target"].(map[string]any)
			if target["creationPolicy"] != "Orphan" || target["deletionPolicy"] != "Retain" || len(spec["data"].([]any)) != 2 {
				t.Fatal("Vault Secret retention or explicit field selection changed")
			}
		}
	}
}

func TestVaultRenderingIsCanonicalAndRetiredConnectionsAreNotReused(t *testing.T) {
	cfg := vaultConfig(t)
	first, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	keys := cfg.Spec.Secrets.Vault.Secrets[0].Keys
	keys[0], keys[1] = keys[1], keys[0]
	second, err := Render(cfg)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("key ordering changed the export", err)
	}
	cfg.Spec.Secrets.Vault.Address = "https://replacement.example.test:8200"
	third, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(second["components/vault/controller.yaml"], third["components/vault/controller.yaml"]) || bytes.Equal(second["components/vault/integration.yaml"], third["components/vault/integration.yaml"]) {
		t.Fatal("retired connection still matches the active controller class")
	}
	cfg.Spec.Secrets.Vault = nil
	if files, err := Render(cfg); err == nil || files != nil {
		t.Fatal("unconfigured Vault request produced partial output")
	}
}

func TestVaultScalarIdentifiersRemainStringsAndShareOneArgoOwner(t *testing.T) {
	cfg := vaultConfig(t)
	cfg.Metadata.Name = "false"
	cfg.Spec.Secrets.Vault.WorkloadNamespace = "on"
	cfg.Spec.Secrets.Vault.Secrets[0].Name = "123"
	cfg.Spec.Secrets.Vault.CASecret.Key = "false"
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range vaultDocuments(t, files["components/vault/integration.yaml"]) {
		metadata := obj["metadata"].(map[string]any)
		if metadata["annotations"].(map[string]any)["bareplane.io/cluster"] != "false" {
			t.Fatal("cluster annotation lost its string type")
		}
		if obj["kind"] == "ExternalSecret" && (metadata["namespace"] != "on" || obj["spec"].(map[string]any)["target"].(map[string]any)["name"] != "123") {
			t.Fatal("Vault scalar lost its string type")
		}
	}
	if !bytes.Contains(files["components/argocd/configuration.yaml"], []byte("health.external-secrets.io_SecretStore")) {
		t.Fatal("Argo-owned health customization missing")
	}
	for name, data := range files {
		if strings.HasPrefix(name, "components/vault/") && bytes.Contains(data, []byte("name: argocd-cm")) {
			t.Fatal("Vault introduced a competing Argo configuration owner")
		}
	}
}
