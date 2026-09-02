package project

import (
	"encoding/json"
	"errors"
	"fmt"
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
		if err := validateGeneratedFilename(name); err != nil {
			return err
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
		if err := writeExclusiveFile(filepath.Join(stage, name), files[name], 0o644); err != nil {
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
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("create generated parent directory: %w", err)
	}
	info, err := os.Lstat(path)
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
