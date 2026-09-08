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

func TestPublishedCertManagerFixtureMatchesRenderer(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	data, err := os.ReadFile(filepath.Join(root, "examples", "cert-manager-fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Metadata.Name != "lab" || cfg.Spec.GitOps.RootPath != "examples/gitops/cert-manager-root" || cfg.Spec.GitOps.RepoURL != "https://github.com/dipeshbabu/bareplane.git" || cfg.Spec.GitOps.Revision != "main" {
		t.Fatal("component fixture must retain its fixed public test contract")
	}
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for name := range files {
		if strings.HasPrefix(name, "components/cert-manager/") || strings.HasPrefix(name, "examples/gitops/cert-manager-root/") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(root, filepath.FromSlash(name))
		if os.Getenv("BAREPLANE_UPDATE_CERT_MANAGER_FIXTURE") == "1" {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, files[name], 0o644); err != nil {
				t.Fatal(err)
			}
		}
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(bytes.ReplaceAll(actual, []byte("\r\n"), []byte("\n")), files[name]) {
			t.Fatalf("cert-manager fixture %s differs; explicitly regenerate and review: %v", name, err)
		}
	}
}
