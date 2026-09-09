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

func TestPublishedDNSFixtureMatchesRenderer(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	data, err := os.ReadFile(filepath.Join(root, "examples", "external-dns-fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Metadata.Name != "lab" || cfg.Spec.GitOps.RootPath != "examples/gitops/external-dns-root" || cfg.Spec.GitOps.RepoURL != "https://github.com/dipeshbabu/bareplane.git" || cfg.Spec.GitOps.Revision != "main" || cfg.Spec.DNS.Automation.EffectiveMode() != "dry-run" {
		t.Fatal("DNS fixture must retain its fixed public dry-run contract")
	}
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for name, expected := range files {
		if strings.HasPrefix(name, "components/argocd/") {
			actual, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
			if err != nil || !bytes.Equal(bytes.ReplaceAll(actual, []byte("\r\n"), []byte("\n")), expected) {
				t.Fatalf("DNS fixture changed a shared published dependency: %s", name)
			}
		}
		if strings.HasPrefix(name, "components/external-dns/") || strings.HasPrefix(name, "examples/gitops/external-dns-root/") {
			names = append(names, name)
		}
	}
	if len(names) != 6 {
		t.Fatalf("unexpected DNS fixture shape: %d files", len(names))
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(root, filepath.FromSlash(name))
		if os.Getenv("BAREPLANE_UPDATE_DNS_FIXTURE") == "1" {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, files[name], 0o644); err != nil {
				t.Fatal(err)
			}
		}
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(bytes.ReplaceAll(actual, []byte("\r\n"), []byte("\n")), files[name]) {
			t.Fatalf("DNS fixture %s differs; explicitly regenerate and review: %v", name, err)
		}
	}
}
