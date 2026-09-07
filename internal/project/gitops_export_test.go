package project

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func exportWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGitOpsExportReplacesOnlyUneditedOwnedTree(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")
	old := map[string][]byte{"components/a.yaml": []byte("old")}
	destination, err := WriteGitOpsExport(configPath, "lab", old)
	if err != nil {
		t.Fatal(err)
	}
	if destination != filepath.Join(root, "gitops") {
		t.Fatalf("unexpected export %s", destination)
	}
	state := filepath.Join(root, ".bareplane", "state", "bootstrap", "keep")
	exportWrite(t, state, []byte("private"))
	before, err := os.ReadFile(filepath.Join(destination, gitOpsExportManifest))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteGitOpsExport(configPath, "lab", old); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(destination, gitOpsExportManifest))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("manifest changed for identical output")
	}
	if _, err := WriteGitOpsExport(configPath, "lab", map[string][]byte{"components/b.yaml": []byte("new")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "components", "a.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("obsolete owned file survived")
	}
	if data, err := os.ReadFile(state); err != nil || string(data) != "private" {
		t.Fatal("bootstrap state changed")
	}
}

func TestGitOpsExportPreservesEditedOrUnmanagedContent(t *testing.T) {
	for _, change := range []string{"file-edit", "extra-file", "extra-directory", "git-checkout", "missing-file", "missing-manifest", "invalid-manifest", "oversized-manifest", "different-cluster", "missing-marker", "wrong-kind"} {
		t.Run(change, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "bareplane.yaml")
			files := map[string][]byte{"components/a.yaml": []byte("old")}
			destination, err := WriteGitOpsExport(configPath, "lab", files)
			if err != nil {
				t.Fatal(err)
			}
			filename := filepath.Join(destination, "components", "a.yaml")
			cluster := "lab"
			switch change {
			case "file-edit":
				exportWrite(t, filename, []byte("user edit"))
			case "extra-file":
				exportWrite(t, filepath.Join(destination, "keep.txt"), []byte("user file"))
			case "extra-directory", "git-checkout":
				name := "user-dir"
				if change == "git-checkout" {
					name = ".git"
				}
				if err := os.Mkdir(filepath.Join(destination, name), 0o700); err != nil {
					t.Fatal(err)
				}
			case "missing-file":
				if err := os.Remove(filename); err != nil {
					t.Fatal(err)
				}
			case "missing-manifest":
				if err := os.Remove(filepath.Join(destination, gitOpsExportManifest)); err != nil {
					t.Fatal(err)
				}
			case "invalid-manifest":
				exportWrite(t, filepath.Join(destination, gitOpsExportManifest), []byte(`{"version":1,"version":1}`))
			case "oversized-manifest":
				exportWrite(t, filepath.Join(destination, gitOpsExportManifest), bytes.Repeat([]byte("x"), (1<<20)+1))
			case "different-cluster":
				cluster = "other"
			case "missing-marker":
				if err := os.Remove(filepath.Join(destination, GeneratedMarkerFilename)); err != nil {
					t.Fatal(err)
				}
			case "wrong-kind":
				exportWrite(t, filepath.Join(destination, GeneratedMarkerFilename), []byte(`{"managedBy":"bareplane","kind":"bootstrap","version":1}`))
			}
			before, readErr := os.ReadFile(filename)
			if _, err := WriteGitOpsExport(configPath, cluster, map[string][]byte{"components/a.yaml": []byte("new")}); err == nil {
				t.Fatal("overwrote user changes")
			}
			after, afterErr := os.ReadFile(filename)
			if !bytes.Equal(before, after) || (readErr == nil) != (afterErr == nil) {
				t.Fatal("refusal modified old output")
			}
		})
	}
}

func TestGitOpsExportRejectsRedirectedPathsAndUnsafeNames(t *testing.T) {
	root := t.TempDir()
	for _, configPath := range []string{"", filepath.Join(root, ".bareplane", "bareplane.yaml"), filepath.Join(root, ".git", "bareplane.yaml")} {
		if _, err := WriteGitOpsExport(configPath, "lab", map[string][]byte{"safe.yaml": []byte("safe")}); err == nil {
			t.Fatal("state export accepted")
		}
	}
	for _, name := range []string{"../escape", "/absolute", "a/../escape", "a\\b", GeneratedMarkerFilename, gitOpsExportManifest} {
		if _, err := WriteGitOpsExport(filepath.Join(root, "bareplane.yaml"), "lab", map[string][]byte{name: []byte("safe")}); err == nil {
			t.Fatalf("unsafe name accepted: %s", name)
		}
	}
	for _, boundary := range []string{"ancestor", "destination", "file", "manifest"} {
		t.Run(boundary, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(root, "bareplane.yaml")
			destination := filepath.Join(root, "gitops")
			link := destination
			if boundary == "ancestor" {
				link = filepath.Join(root, "linked")
				configPath = filepath.Join(link, "bareplane.yaml")
			} else if boundary != "destination" {
				if _, err := WriteGitOpsExport(configPath, "lab", map[string][]byte{"safe.yaml": []byte("safe")}); err != nil {
					t.Fatal(err)
				}
				name := "safe.yaml"
				if boundary == "manifest" {
					name = gitOpsExportManifest
				}
				link = filepath.Join(destination, name)
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				target = filepath.Join(target, "keep")
				exportWrite(t, target, []byte("keep"))
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if _, err := WriteGitOpsExport(configPath, "lab", map[string][]byte{"safe.yaml": []byte("new")}); err == nil {
				t.Fatal("followed redirected export")
			}
		})
	}
}

func TestGitOpsExportSharesBootstrapOperationLock(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "bareplane.yaml")
	lock, err := AcquireBootstrapOperation(configPath, "apply")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err := WriteGitOpsExport(configPath, "lab", map[string][]byte{"safe.yaml": []byte("safe")}); !errors.Is(err, ErrBootstrapOperationLocked) {
		t.Fatalf("write bypassed active operation: %v", err)
	}
}

func TestRequireGitOpsExportRejectsMissingStaleOrEditedPayload(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "bareplane.yaml")
	files := map[string][]byte{"safe.yaml": []byte("safe")}
	if _, err := RequireGitOpsExport(configPath, "lab", files); err == nil {
		t.Fatal("missing export accepted")
	}
	destination, err := WriteGitOpsExport(configPath, "lab", files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RequireGitOpsExport(configPath, "lab", files); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []map[string][]byte{
		{"safe.yaml": []byte("new")},
		{"different.yaml": []byte("safe")},
		{"safe.yaml": []byte("safe"), "extra.yaml": []byte("extra")},
	} {
		if _, err := RequireGitOpsExport(configPath, "lab", expected); err == nil {
			t.Fatal("stale export accepted")
		}
	}
	exportWrite(t, filepath.Join(destination, "safe.yaml"), []byte("user edit"))
	if _, err := RequireGitOpsExport(configPath, "lab", files); err == nil {
		t.Fatal("modified export accepted")
	}
}
