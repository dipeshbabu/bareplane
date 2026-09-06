package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const GeneratedMarkerFilename = ".bareplane-generated.json"

var ErrUnmanagedDestination = errors.New("destination is not managed by Bareplane")

type generatedMarker struct {
	ManagedBy string `json:"managedBy"`
	Kind      string `json:"kind"`
	Version   int    `json:"version"`
}

func ReplaceGeneratedDirectory(destination, kind string, files map[string][]byte) error {
	return replaceGenerated(destination, kind, files, false)
}

// ReplaceGeneratedTree installs a staged tree of portable, relative slash paths.
func ReplaceGeneratedTree(destination, kind string, files map[string][]byte) error {
	return replaceGenerated(destination, kind, files, true)
}

func replaceGenerated(destination, kind string, files map[string][]byte, nested bool) error {
	if strings.TrimSpace(destination) == "" {
		return errors.New("generated destination is empty")
	}
	if strings.TrimSpace(kind) == "" {
		return errors.New("generated kind is empty")
	}
	if len(files) == 0 {
		return errors.New("generated file set is empty")
	}
	if _, exists := files[GeneratedMarkerFilename]; exists {
		return fmt.Errorf("generated file set must not contain reserved marker %q", GeneratedMarkerFilename)
	}
	for name := range files {
		if err := validateGeneratedPath(name, nested); err != nil {
			return err
		}
		for parent := filepath.ToSlash(filepath.Dir(name)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if _, exists := files[parent]; exists {
				return fmt.Errorf("generated path %q is both a file and a directory", parent)
			}
		}
	}

	destination = filepath.Clean(destination)
	parent := filepath.Dir(destination)
	if err := ensureRealDirectory(parent); err != nil {
		return err
	}

	destinationExists := false
	if info, err := os.Lstat(destination); err == nil {
		destinationExists = true
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: %s is not a regular directory", ErrUnmanagedDestination, destination)
		}
		managed, err := generatedDirectoryMatches(destination, kind)
		if err != nil {
			return err
		}
		if !managed {
			return fmt.Errorf("%w: %s", ErrUnmanagedDestination, destination)
		}
		if err := filepath.WalkDir(destination, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
				return fmt.Errorf("%w: generated path %s must not be a symlink or special file", ErrUnmanagedDestination, path)
			}
			return nil
		}); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect generated destination: %w", err)
	}

	stage, err := os.MkdirTemp(parent, ".bareplane-stage-")
	if err != nil {
		return fmt.Errorf("create generated staging directory: %w", err)
	}
	stageExists := true
	defer func() {
		if stageExists {
			_ = os.RemoveAll(stage)
		}
	}()

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create generated subdirectory: %w", err)
		}
		if err := writeExclusiveFile(path, files[name], 0o644); err != nil {
			return fmt.Errorf("write generated file %q: %w", name, err)
		}
	}

	marker, err := json.MarshalIndent(generatedMarker{
		ManagedBy: "bareplane",
		Kind:      kind,
		Version:   1,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode generated marker: %w", err)
	}
	marker = append(marker, '\n')
	if err := writeExclusiveFile(filepath.Join(stage, GeneratedMarkerFilename), marker, 0o644); err != nil {
		return fmt.Errorf("write generated marker: %w", err)
	}

	if !destinationExists {
		if err := os.Rename(stage, destination); err != nil {
			return fmt.Errorf("install generated directory: %w", err)
		}
		stageExists = false
		return nil
	}

	backup, err := reserveBackupPath(parent)
	if err != nil {
		return err
	}
	if err := os.Rename(destination, backup); err != nil {
		return fmt.Errorf("stage previous generated directory: %w", err)
	}
	if err := os.Rename(stage, destination); err != nil {
		rollbackErr := os.Rename(backup, destination)
		if rollbackErr != nil {
			return fmt.Errorf("install generated directory: %v; rollback failed: %w", err, rollbackErr)
		}
		return fmt.Errorf("install generated directory: %w", err)
	}
	stageExists = false

	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("remove previous generated directory backup: %w", err)
	}
	return nil
}

func generatedDirectoryMatches(destination, kind string) (bool, error) {
	markerPath := filepath.Join(destination, GeneratedMarkerFilename)
	info, err := os.Lstat(markerPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect generated marker: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, nil
	}
	if info.Size() > 4096 {
		return false, nil
	}

	data, err := os.ReadFile(markerPath)
	if err != nil {
		return false, fmt.Errorf("read generated marker: %w", err)
	}
	var marker generatedMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return false, nil
	}
	return marker.ManagedBy == "bareplane" && marker.Kind == kind && marker.Version == 1, nil
}

func ensureRealDirectory(path string) error {
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	if parent != path {
		if err := ensureRealDirectory(parent); err != nil {
			return err
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0o755); err != nil {
			return fmt.Errorf("create generated parent directory: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect generated parent directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("generated parent %s must be a regular directory", path)
	}
	return nil
}

func reserveBackupPath(parent string) (string, error) {
	path, err := os.MkdirTemp(parent, ".bareplane-backup-")
	if err != nil {
		return "", fmt.Errorf("reserve generated backup path: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("prepare generated backup path: %w", err)
	}
	return path, nil
}

func writeExclusiveFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func validateGeneratedFilename(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return fmt.Errorf("generated filename %q must be a plain filename", name)
	}
	if strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("generated filename %q contains a path separator or NUL", name)
	}
	return nil
}

func validateGeneratedPath(name string, nested bool) error {
	if !nested {
		return validateGeneratedFilename(name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.EqualFold(part, GeneratedMarkerFilename) {
			return fmt.Errorf("unsafe generated path %q", name)
		}
		for _, ch := range part {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' || ch == '.') {
				return fmt.Errorf("generated path %q must use lowercase portable path components", name)
			}
		}
		stem := strings.SplitN(part, ".", 2)[0]
		if stem == "con" || stem == "prn" || stem == "aux" || stem == "nul" || (len(stem) == 4 && (strings.HasPrefix(stem, "com") || strings.HasPrefix(stem, "lpt")) && stem[3] >= '0' && stem[3] <= '9') {
			return fmt.Errorf("generated path %q uses a reserved device name", name)
		}
	}
	return nil
}
