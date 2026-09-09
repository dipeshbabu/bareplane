package gitops

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

// The public CI overlay reuses the unchanged baseline Argo assets. The VM
// retargets the same existing lab-argocd Application after handoff; ordinary
// exports still contain the original six self-contained installer inputs.
func TestPublishedSOPSFixtureMatchesRenderer(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	data, err := os.ReadFile(filepath.Join(root, "examples", "sops-fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Metadata.Name != "lab" || cfg.Spec.GitOps.RootPath != "examples/gitops/secrets-sops-root" || cfg.Spec.GitOps.RepoURL != "https://github.com/dipeshbabu/bareplane.git" || cfg.Spec.GitOps.Revision != "main" {
		t.Fatal("SOPS fixture must retain its fixed public contract")
	}
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var rendered map[string]any
	if err := yaml.Unmarshal(files["components/argocd/kustomization.yaml"], &rendered); err != nil {
		t.Fatal(err)
	}
	patches := rendered["patches"].([]any)
	overlay := kustomization("../../../components/argocd")
	overlay["patches"] = []any{map[string]string{"path": "configuration.yaml"}, patches[len(patches)-1]}
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
			name = "examples/gitops/sops-argocd/" + strings.TrimPrefix(name, "components/argocd/")
		} else if !strings.HasPrefix(name, "components/secrets-sops/") && !strings.HasPrefix(name, "examples/gitops/secrets-sops-root/") {
			continue
		}
		if name == "examples/gitops/secrets-sops-root/applications/argocd.yaml" {
			expected = bytes.ReplaceAll(expected, []byte("path: components/argocd"), []byte("path: examples/gitops/sops-argocd"))
		}
		count++
		path := filepath.Join(root, filepath.FromSlash(name))
		if os.Getenv("BAREPLANE_UPDATE_SOPS_FIXTURE") == "1" {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, expected, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(bytes.ReplaceAll(actual, []byte("\r\n"), []byte("\n")), expected) {
			t.Fatalf("SOPS fixture differs; explicitly regenerate and review %s: %v", name, err)
		}
	}
	if count != 7 {
		t.Fatalf("unexpected public SOPS fixture file count: %d", count)
	}
}
