package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dipeshbabu/bareplane/internal/config"
)

var ErrAlreadyExists = errors.New("configuration already exists")

// Init creates a new Bareplane configuration at path without overwriting existing data.
func Init(path string) error {
	if path == "" {
		return errors.New("configuration path is empty")
	}

	dir := filepath.Dir(path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create parent directory: %w", err)
		}
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %s", ErrAlreadyExists, path)
		}
		return fmt.Errorf("create configuration: %w", err)
	}

	removeOnError := true
	defer func() {
		_ = file.Close()
		if removeOnError {
			_ = os.Remove(path)
		}
	}()

	if err := config.Encode(file, config.Default()); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync configuration: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close configuration: %w", err)
	}

	removeOnError = false
	return nil
}
