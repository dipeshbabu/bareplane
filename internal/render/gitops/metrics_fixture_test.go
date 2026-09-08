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

func TestPublishedMetricsFixtureMatchesRenderer(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	data, err := os.ReadFile(filepath.Join(root, "examples", "metrics-server-fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Metadata.Name != "lab" || cfg.Spec.GitOps.RootPath != "examples/gitops/metrics-server-root" || cfg.Spec.GitOps.RepoURL != "https://github.com/dipeshbabu/bareplane.git" || cfg.Spec.GitOps.Revision != "main" {
		t.Fatal("metrics fixture must retain its fixed public test contract")
	}
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for name, expected := range files {
		if strings.HasPrefix(name, "components/argocd/") || strings.HasPrefix(name, "components/cert-manager/") {
			actual, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
			if err != nil || !bytes.Equal(bytes.ReplaceAll(actual, []byte("\r\n"), []byte("\n")), expected) {
				t.Fatalf("metrics fixture changed a shared published dependency: %s", name)
			}
		}
		if strings.HasPrefix(name, "components/metrics-server/") || strings.HasPrefix(name, "examples/gitops/metrics-server-root/") {
			names = append(names, name)
		}
	}
	if len(names) != 7 {
		t.Fatalf("unexpected metrics fixture shape: %d files", len(names))
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(root, filepath.FromSlash(name))
		if os.Getenv("BAREPLANE_UPDATE_METRICS_FIXTURE") == "1" {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, files[name], 0o644); err != nil {
				t.Fatal(err)
			}
		}
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(bytes.ReplaceAll(actual, []byte("\r\n"), []byte("\n")), files[name]) {
			t.Fatalf("metrics fixture %s differs; explicitly regenerate and review: %v", name, err)
		}
	}
}
