package gitops

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishedObservabilityFixtureMatchesRenderer(t *testing.T) {
	files, err := Render(observabilityConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for name, expected := range files {
		if strings.HasPrefix(name, "components/argocd/") {
			actual, err := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(name)))
			if err != nil || !bytes.Equal(bytes.ReplaceAll(actual, []byte("\r\n"), []byte("\n")), expected) {
				t.Fatal("observability changed the shared Argo fixture", name)
			}
		}
		if !strings.HasPrefix(name, "components/observability/") && !strings.HasPrefix(name, "examples/gitops/observability-root/") {
			continue
		}
		count++
		path := filepath.Join("..", "..", "..", filepath.FromSlash(name))
		if os.Getenv("BAREPLANE_UPDATE_OBSERVABILITY_FIXTURE") == "1" {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, expected, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(bytes.ReplaceAll(actual, []byte("\r\n"), []byte("\n")), expected) {
			t.Fatal("observability fixture differs; explicitly regenerate", name, err)
		}
	}
	if count != 10 {
		t.Fatalf("unexpected fixture file count: %d", count)
	}
}
