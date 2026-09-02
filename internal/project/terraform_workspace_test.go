package project

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTerraformWorkspaceForIsDeterministicAndSeparated(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")

	first, err := TerraformWorkspaceFor(configPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := TerraformWorkspaceFor(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("identical paths produced different workspaces: %#v != %#v", first, second)
	}

	if first.GeneratedDir != filepath.Join(root, ".bareplane", "terraform") {
		t.Fatalf("unexpected generated directory %q", first.GeneratedDir)
	}
	wantState := filepath.Join(root, ".bareplane", "state", "terraform")
	if first.StateDir != wantState {
		t.Fatalf("unexpected state directory %q", first.StateDir)
	}
	if first.PlanManifestFile != filepath.Join(wantState, "terraform.tfplan.json") {
		t.Fatalf("unexpected plan manifest path %q", first.PlanManifestFile)
	}
	if filepath.Dir(first.StateDir) == first.GeneratedDir || first.StateDir == first.GeneratedDir {
		t.Fatal("persistent state overlaps generated configuration")
	}
	for _, path := range []string{
		first.DataDir,
		first.StateFile,
		first.StateBackupFile,
		first.LockFile,
		first.PlanFile,
		first.PlanManifestFile,
	} {
		if !isWithin(first.StateDir, path) {
			t.Fatalf("persistent artifact %q escaped state directory", path)
		}
	}
}

func TestEnsureTerraformWorkspaceIsIdempotentAndPrivate(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "bareplane.yaml")
	first, err := EnsureTerraformWorkspace(configPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureTerraformWorkspace(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("workspace changed between calls: %#v != %#v", first, second)
	}

	if runtime.GOOS != "windows" {
		for _, path := range []string{first.StateDir, first.DataDir} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != privateDirectoryMode {
				t.Fatalf("%s permissions = %o, want %o", path, got, privateDirectoryMode)
			}
		}
	}
}

func TestEnsureTerraformWorkspaceRefusesSymlinkBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior depends on Windows privileges")
	}
	root := t.TempDir()
	realState := filepath.Join(root, "real-state")
	if err := os.Mkdir(realState, 0o700); err != nil {
		t.Fatal(err)
	}
	bareplaneRoot := filepath.Join(root, ".bareplane")
	if err := os.Mkdir(bareplaneRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realState, filepath.Join(bareplaneRoot, "state")); err != nil {
		t.Fatal(err)
	}

	_, err := EnsureTerraformWorkspace(filepath.Join(root, "bareplane.yaml"))
	if err == nil {
		t.Fatal("expected symlink workspace rejection")
	}
}

func TestEnsureTerraformWorkspaceRefusesNonDirectoryBoundary(t *testing.T) {
	root := t.TempDir()
	bareplaneRoot := filepath.Join(root, ".bareplane")
	if err := os.Mkdir(bareplaneRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bareplaneRoot, "state"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := EnsureTerraformWorkspace(filepath.Join(root, "bareplane.yaml"))
	if err == nil {
		t.Fatal("expected non-directory workspace rejection")
	}
}

func TestPersistentStateSurvivesGeneratedDirectoryReplacement(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")
	workspace, err := EnsureTerraformWorkspace(configPath)
	if err != nil {
		t.Fatal(err)
	}

	state := []byte(`{"version":4}`)
	if err := os.WriteFile(workspace.StateFile, state, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceGeneratedDirectory(workspace.GeneratedDir, "terraform", map[string][]byte{"main.tf.json": []byte("{\"first\":true}\n")}); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceGeneratedDirectory(workspace.GeneratedDir, "terraform", map[string][]byte{"main.tf.json": []byte("{\"second\":true}\n")}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(workspace.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(state) {
		t.Fatalf("state changed across renders: %q", got)
	}
}

func TestTerraformWorkspaceForRejectsEmptyPath(t *testing.T) {
	_, err := TerraformWorkspaceFor("")
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected configuration path validation error, got %v", err)
	}
}

func isWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && rel != "." && !filepath.IsAbs(rel)
}
