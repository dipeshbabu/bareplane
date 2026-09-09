package gitops

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPublishedVaultFixtureMatchesRenderer(t *testing.T) {
	cfg := vaultConfig(t)
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	overlay := kustomization("../../../components/argocd")
	overlay["patches"] = []map[string]string{{"path": "configuration.yaml"}}
	files["components/argocd/kustomization.yaml"], err = yaml.Marshal(overlay)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for name, expected := range files {
		if strings.HasPrefix(name, "components/argocd/") {
			if name != "components/argocd/configuration.yaml" && name != "components/argocd/kustomization.yaml" {
				continue
			}
			name = "examples/gitops/vault-argocd/" + strings.TrimPrefix(name, "components/argocd/")
		} else if !strings.HasPrefix(name, "components/vault/") && !strings.HasPrefix(name, "examples/gitops/vault-root/") {
			continue
		}
		if name == "examples/gitops/vault-root/applications/argocd.yaml" {
			expected = bytes.ReplaceAll(expected, []byte("path: components/argocd"), []byte("path: examples/gitops/vault-argocd"))
		}
		count++
		path := filepath.Join("..", "..", "..", filepath.FromSlash(name))
		if os.Getenv("BAREPLANE_UPDATE_VAULT_FIXTURE") == "1" {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, expected, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(bytes.ReplaceAll(actual, []byte("\r\n"), []byte("\n")), expected) {
			t.Fatalf("Vault fixture differs: %s; explicitly regenerate and review: %v", name, err)
		}
	}
	if count != 10 {
		t.Fatalf("unexpected Vault fixture file count: %d", count)
	}
}
