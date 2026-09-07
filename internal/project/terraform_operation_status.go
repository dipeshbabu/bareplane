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
	return inspectOperationStatus(filepath.Join(workspace.StateDir, terraformOperationLockDir), "Terraform")
}

type BootstrapOperationStatus = TerraformOperationStatus

func InspectBootstrapOperation(configPath string) (BootstrapOperationStatus, error) {
	stateDir, err := InspectBootstrapState(configPath)
	if err != nil {
		return BootstrapOperationStatus{}, err
	}
	return inspectOperationStatus(filepath.Join(stateDir, terraformOperationLockDir), "bootstrap")
}

func inspectOperationStatus(lockPath, kind string) (TerraformOperationStatus, error) {
	info, err := os.Lstat(lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return TerraformOperationStatus{}, nil
		}
		return TerraformOperationStatus{}, fmt.Errorf("inspect %s operation lock: %w", kind, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return TerraformOperationStatus{}, fmt.Errorf("%s operation lock %s must be a regular directory", kind, lockPath)
	}
	metadata, err := readOperationLockMetadata(lockPath)
	if err != nil {
		return TerraformOperationStatus{}, fmt.Errorf("read %s operation lock metadata: %w", kind, err)
	}
	return TerraformOperationStatus{
		Present:   true,
		Operation: metadata.Operation,
		PID:       metadata.PID,
	}, nil
}
