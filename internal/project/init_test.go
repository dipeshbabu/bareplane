package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
)

func TestInitCreatesValidConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "bareplane.yaml")
	if err := Init(path); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	cfg, err := config.Load(file)
	if err != nil {
		t.Fatalf("generated configuration is invalid: %v", err)
	}
	if cfg.Metadata.Name != "bareplane" {
		t.Fatalf("unexpected cluster name %q", cfg.Metadata.Name)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("expected mode 0644, got %04o", got)
	}
}

func TestInitRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bareplane.yaml")
	const original = "do-not-replace\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Init(path)
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("existing file changed: %q", got)
	}
}

func TestInitRejectsEmptyPath(t *testing.T) {
	if err := Init(""); err == nil {
		t.Fatal("expected empty path error")
	}
}
