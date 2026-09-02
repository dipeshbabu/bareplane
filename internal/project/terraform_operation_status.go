package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type TerraformOperationStatus struct {
	Present   bool
	Operation string
	PID       int
}

func InspectTerraformOperation(configPath string) (TerraformOperationStatus, error) {
	workspace, err := TerraformWorkspaceFor(configPath)
	if err != nil {
		return TerraformOperationStatus{}, err
	}
	lockPath := filepath.Join(workspace.StateDir, terraformOperationLockDir)
	info, err := os.Lstat(lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return TerraformOperationStatus{}, nil
		}
		return TerraformOperationStatus{}, fmt.Errorf("inspect Terraform operation lock: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return TerraformOperationStatus{}, fmt.Errorf("Terraform operation lock %s must be a regular directory", lockPath)
	}
	metadata, err := readOperationLockMetadata(lockPath)
	if err != nil {
		return TerraformOperationStatus{}, fmt.Errorf("read Terraform operation lock metadata: %w", err)
	}
	return TerraformOperationStatus{
		Present:   true,
		Operation: metadata.Operation,
		PID:       metadata.PID,
	}, nil
}
