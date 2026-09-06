package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedTreeReplacesNestedFilesAndPreservesState(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, ".bareplane", "bootstrap")
	state := filepath.Join(root, ".bareplane", "state", "bootstrap", "known_hosts")
	if err := os.MkdirAll(filepath.Dir(state), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("trusted"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, files := range []map[string][]byte{
		{"roles/old/tasks/main.yaml": []byte("old")},
		{"roles/new/tasks/main.yaml": []byte("new"), "group_vars/all/cluster.yaml": []byte("vars")},
	} {
		if err := ReplaceGeneratedTree(destination, "bootstrap", files); err != nil {
			t.Fatal(err)
		}
		for name, want := range files {
			data, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(name)))
			if err != nil || string(data) != string(want) {
				t.Fatalf("%s: %q %v", name, data, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "roles", "old")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("obsolete file survived: %v", err)
	}
	data, err := os.ReadFile(state)
	if err != nil || string(data) != "trusted" {
		t.Fatalf("trust changed: %q %v", data, err)
	}
}

func TestGeneratedTreeRejectsUnsafePathsBeforeWriting(t *testing.T) {
	for _, name := range []string{"", "/absolute", "../escape", "a/../../escape", "a//b", "a/./b", "a/../b", "a/", "a\\b", "c:/escape", "a:stream", "a/..", "nul", "a/con.yaml", "a/lpt1.txt", "a/b.", "a/b ", "a/\x00b", GeneratedMarkerFilename, "a/" + GeneratedMarkerFilename} {
		t.Run(name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "output")
			if err := ReplaceGeneratedTree(destination, "bootstrap", map[string][]byte{name: []byte("bad")}); err == nil {
				t.Fatal("accepted unsafe name")
			}
			if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("wrote destination: %v", err)
			}
		})
	}
	if err := ReplaceGeneratedTree(filepath.Join(t.TempDir(), "out"), "bootstrap", map[string][]byte{"roles": []byte("file"), "roles/x/tasks/main.yaml": []byte("task")}); err == nil {
		t.Fatal("accepted file/directory collision")
	}
}

func TestGeneratedTreeRefusesSymlinks(t *testing.T) {
	for _, boundary := range []string{"ancestor", "nested-file", "nested-dir"} {
		t.Run(boundary, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(root, "output")
			files := map[string][]byte{"roles/test/tasks/main.yaml": []byte("safe")}
			if boundary == "ancestor" {
				link := filepath.Join(root, "link")
				if err := os.Symlink(target, link); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				destination = filepath.Join(link, "missing", "output")
			} else {
				if err := ReplaceGeneratedTree(destination, "bootstrap", files); err != nil {
					t.Fatal(err)
				}
				linkTarget := target
				if boundary == "nested-file" {
					linkTarget = filepath.Join(target, "keep")
					if err := os.WriteFile(linkTarget, []byte("keep"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(linkTarget, filepath.Join(destination, "roles", "link")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			if err := ReplaceGeneratedTree(destination, "bootstrap", files); err == nil {
				t.Fatal("followed a symlink")
			}
			if _, err := os.Stat(filepath.Join(target, "missing")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("created directories through symlink")
			}
		})
	}
}
