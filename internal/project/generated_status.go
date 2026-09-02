package project

import (
	"errors"
	"fmt"
	"os"
)

func RequireGeneratedDirectory(destination, kind string) error {
	info, err := os.Lstat(destination)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s does not exist", ErrUnmanagedDestination, destination)
		}
		return fmt.Errorf("inspect generated destination: %w", err)
	}
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
	return nil
}
