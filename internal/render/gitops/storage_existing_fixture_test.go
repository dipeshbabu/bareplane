package gitops

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
)

func TestPublishedExistingStorageFixtureMatchesRenderer(t *testing.T) {
	data, err := os.ReadFile("../../../examples/existing-storage-fixture.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	files, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for name, expected := range files {
		if !strings.HasPrefix(name, "components/storage-existing/") && !strings.HasPrefix(name, "examples/gitops/existing-storage-root/") {
			continue
		}
		count++
		actual, err := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(bytes.ReplaceAll(actual, []byte("\r\n"), []byte("\n")), expected) {
			t.Fatal("existing storage fixture differs from rendered configuration", name, err)
		}
	}
	if count != 5 {
		t.Fatalf("unexpected fixture file count: %d", count)
	}
}
