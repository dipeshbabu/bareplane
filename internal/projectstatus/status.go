package projectstatus

import (
	"errors"
	"fmt"
	"os"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/project"
)

type Report struct {
	Cluster            string
	ProvisioningReady  bool
	Rendered           bool
	StatePresent       bool
	StateBackupPresent bool
	LockPresent        bool
	PlanPresent        bool
	ManifestPresent    bool
	Operation          project.TerraformOperationStatus
	Next               string
}

func Inspect(configPath string) (Report, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return Report{}, fmt.Errorf("open configuration: %w", err)
	}
	cfg, err := config.Load(file)
	closeErr := file.Close()
	if err != nil {
		return Report{}, fmt.Errorf("load configuration: %w", err)
	}
	if closeErr != nil {
		return Report{}, fmt.Errorf("close configuration: %w", closeErr)
	}

	report := Report{Cluster: cfg.Metadata.Name}
	report.ProvisioningReady = cfg.ValidateProvisioning() == nil

	workspace, err := project.TerraformWorkspaceFor(configPath)
	if err != nil {
		return Report{}, fmt.Errorf("resolve Terraform workspace: %w", err)
	}

	rendered, err := inspectGenerated(workspace.GeneratedDir)
	if err != nil {
		return Report{}, err
	}
	report.Rendered = rendered

	for _, item := range []struct {
		path string
		set  *bool
		name string
	}{
		{workspace.StateFile, &report.StatePresent, "Terraform state"},
		{workspace.StateBackupFile, &report.StateBackupPresent, "Terraform state backup"},
		{workspace.LockFile, &report.LockPresent, "Terraform dependency lock"},
		{workspace.PlanFile, &report.PlanPresent, "Terraform saved plan"},
		{workspace.PlanManifestFile, &report.ManifestPresent, "Terraform plan manifest"},
	} {
		present, err := inspectRegularFile(item.path)
		if err != nil {
			return Report{}, fmt.Errorf("inspect %s: %w", item.name, err)
		}
		*item.set = present
	}

	report.Operation, err = project.InspectTerraformOperation(configPath)
	if err != nil {
		return Report{}, err
	}
	report.Next = nextStep(report)
	return report, nil
}

func inspectGenerated(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect generated Terraform: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("generated Terraform path %s must be a regular directory", path)
	}
	if err := project.RequireGeneratedDirectory(path, "terraform"); err != nil {
		return false, fmt.Errorf("generated Terraform is not a valid Bareplane render: %w", err)
	}
	return true, nil
}

func inspectRegularFile(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s must be a regular file", path)
	}
	return true, nil
}

func nextStep(report Report) string {
	if report.Operation.Present {
		return fmt.Sprintf("wait for or inspect %s operation (pid %d)", report.Operation.Operation, report.Operation.PID)
	}
	if !report.ProvisioningReady {
		return "complete Proxmox provisioning settings, then run bareplane validate"
	}
	if !report.Rendered {
		return "run bareplane render"
	}
	if report.PlanPresent && report.ManifestPresent {
		return fmt.Sprintf("review the saved plan, then run bareplane terraform apply --approve %s", report.Cluster)
	}
	if report.PlanPresent && !report.ManifestPresent {
		return "saved plan is diagnostic-only; run bareplane terraform plan again before apply"
	}
	return "run bareplane terraform plan"
}
