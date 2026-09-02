package project

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRequireGeneratedDirectoryAcceptsManagedOutput(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "terraform")
	if err := ReplaceGeneratedDirectory(destination, "terraform", map[string][]byte{"main.tf.json": []byte("{}\n")}); err != nil {
		t.Fatal(err)
	}
	if err := RequireGeneratedDirectory(destination, "terraform"); err != nil {
		t.Fatalf("RequireGeneratedDirectory() error = %v", err)
	}
}

func TestRequireGeneratedDirectoryRejectsMissingOutput(t *testing.T) {
	err := RequireGeneratedDirectory(filepath.Join(t.TempDir(), "missing"), "terraform")
	if !errors.Is(err, ErrUnmanagedDestination) {
		t.Fatalf("expected ErrUnmanagedDestination, got %v", err)
	}
}

func TestRequireGeneratedDirectoryRejectsUnmanagedOutput(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "terraform")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	err := RequireGeneratedDirectory(destination, "terraform")
	if !errors.Is(err, ErrUnmanagedDestination) {
		t.Fatalf("expected ErrUnmanagedDestination, got %v", err)
	}
}

func TestRequireGeneratedDirectoryRejectsWrongKind(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "terraform")
	if err := ReplaceGeneratedDirectory(destination, "terraform", map[string][]byte{"main.tf.json": []byte("{}\n")}); err != nil {
		t.Fatal(err)
	}
	err := RequireGeneratedDirectory(destination, "gitops")
	if !errors.Is(err, ErrUnmanagedDestination) {
		t.Fatalf("expected wrong-kind rejection, got %v", err)
	}
}

func TestRequireGeneratedDirectoryRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior depends on Windows privileges")
	}
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := ReplaceGeneratedDirectory(realDir, "terraform", map[string][]byte{"main.tf.json": []byte("{}\n")}); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "terraform")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	err := RequireGeneratedDirectory(link, "terraform")
	if !errors.Is(err, ErrUnmanagedDestination) {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}
