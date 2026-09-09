package gitops

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishedStorageFixtureMatchesRenderer(t *testing.T) {
	files, err := Render(storageFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for name, expected := range files {
		if !strings.HasPrefix(name, "components/storage/") && !strings.HasPrefix(name, "examples/gitops/storage-root/") {
			continue
		}
		count++
		path := filepath.Join("..", "..", "..", filepath.FromSlash(name))
		if os.Getenv("BAREPLANE_UPDATE_STORAGE_FIXTURE") == "1" {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, expected, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(bytes.ReplaceAll(actual, []byte("\r\n"), []byte("\n")), expected) {
			t.Fatal("storage fixture differs; explicitly regenerate", name, err)
		}
	}
	if count != 5 {
		t.Fatalf("unexpected fixture file count: %d", count)
	}
}
