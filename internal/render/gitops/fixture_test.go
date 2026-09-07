package gitops

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
)

func TestPublishedArgoFixtureMatchesRenderer(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	data, err := os.ReadFile(filepath.Join(root, "examples", "gitops-fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Metadata.Name != "lab" || cfg.Spec.GitOps == nil || cfg.Spec.GitOps.RootPath != "examples/gitops/root" || cfg.Spec.GitOps.RepoURL != "https://github.com/dipeshbabu/bareplane.git" || cfg.Spec.GitOps.Revision != "main" {
		t.Fatal("public acceptance fixture must use its fixed, non-secret repository contract")
	}
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0)
	for name := range files {
		if strings.HasPrefix(name, "components/argocd/") || strings.HasPrefix(name, "examples/gitops/root/") || name == "bootstrap/lab-root-application.yaml" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(root, filepath.FromSlash(name))
		if os.Getenv("BAREPLANE_UPDATE_GITOPS_FIXTURE") == "1" {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, files[name], 0o644); err != nil {
				t.Fatal(err)
			}
		}
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(bytes.ReplaceAll(actual, []byte("\r\n"), []byte("\n")), files[name]) {
			t.Fatalf("public fixture %s differs from the reviewed renderer; explicitly regenerate and review it: %v", name, err)
		}
	}
}
