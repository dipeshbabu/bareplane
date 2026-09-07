package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// BootstrapStateDirFor returns persistent state separate from generated assets
// and the Terraform backend/operation lock.
func BootstrapStateDirFor(configPath string) (string, error) {
	if strings.TrimSpace(configPath) == "" {
		return "", errors.New("configuration path is empty")
	}
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(absolute), ".bareplane", "state", "bootstrap"), nil
}

// InspectBootstrapState refuses redirected paths and insecure existing state.
// Missing directories are valid for an initial read-only inspection.
func InspectBootstrapState(configPath string) (string, error) {
	path, err := BootstrapStateDirFor(configPath)
	if err != nil {
		return "", err
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect bootstrap state: %w", err)
		}
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", errors.New("bootstrap state ancestors must be real directories")
			}
			if runtime.GOOS != "windows" && (current == path || current == filepath.Dir(path)) && info.Mode().Perm()&0o077 != 0 {
				return "", errors.New("bootstrap state directories must be owner-only")
			}
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return path, nil
}

func ensureBootstrapState(configPath string) (string, error) {
	path, err := InspectBootstrapState(configPath)
	if err != nil {
		return "", err
	}
	for _, directory := range []struct {
		path string
		mode os.FileMode
	}{
		{filepath.Dir(filepath.Dir(path)), 0o755},
		{filepath.Dir(path), 0o700},
		{path, 0o700},
	} {
		if err := os.Mkdir(directory.path, directory.mode); err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("create bootstrap state directory: %w", err)
		}
	}
	return InspectBootstrapState(configPath)
}
