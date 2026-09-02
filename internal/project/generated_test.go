package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceGeneratedDirectoryCreatesManagedOutput(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, ".bareplane", "terraform")
	files := map[string][]byte{"main.tf.json": []byte("{}\n")}

	if err := ReplaceGeneratedDirectory(destination, "terraform", files); err != nil {
		t.Fatalf("ReplaceGeneratedDirectory() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "main.tf.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "{}\n" {
		t.Fatalf("unexpected generated content %q", got)
	}
	if _, err := os.Stat(filepath.Join(destination, GeneratedMarkerFilename)); err != nil {
		t.Fatalf("expected generation marker: %v", err)
	}
}

func TestReplaceGeneratedDirectoryReplacesManagedOutput(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "generated")
	if err := ReplaceGeneratedDirectory(destination, "terraform", map[string][]byte{"main.tf.json": []byte("old\n")}); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceGeneratedDirectory(destination, "terraform", map[string][]byte{"main.tf.json": []byte("new\n")}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "main.tf.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new\n" {
		t.Fatalf("unexpected replacement content %q", got)
	}
}

func TestReplaceGeneratedDirectoryRefusesUnmanagedDestination(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "generated")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "important.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := ReplaceGeneratedDirectory(destination, "terraform", map[string][]byte{"main.tf.json": []byte("{}\n")})
	if !errors.Is(err, ErrUnmanagedDestination) {
		t.Fatalf("expected ErrUnmanagedDestination, got %v", err)
	}
	got, readErr := os.ReadFile(filepath.Join(destination, "important.txt"))
	if readErr != nil || string(got) != "keep" {
		t.Fatalf("unmanaged destination was modified: %q, %v", got, readErr)
	}
}

func TestReplaceGeneratedDirectoryRejectsUnsafeNames(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "generated")
	for _, name := range []string{"../escape", "nested/file", "", GeneratedMarkerFilename} {
		t.Run(name, func(t *testing.T) {
			if err := ReplaceGeneratedDirectory(destination, "terraform", map[string][]byte{name: []byte("x")}); err == nil {
				t.Fatal("expected unsafe filename error")
			}
		})
	}
}

func TestReplaceGeneratedDirectoryRefusesSymlinkDestination(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "generated")
	if err := os.Symlink(realDir, destination); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := ReplaceGeneratedDirectory(destination, "terraform", map[string][]byte{"main.tf.json": []byte("{}\n")})
	if !errors.Is(err, ErrUnmanagedDestination) {
		t.Fatalf("expected symlink destination rejection, got %v", err)
	}
}
