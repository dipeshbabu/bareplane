package terraformexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/project"
	"github.com/dipeshbabu/bareplane/internal/provider/proxmox"
)

type ApplyOptions struct {
	ConfigPath      string
	Approval        string
	TerraformBinary string
	Credentials     proxmox.Credentials
	BaseEnvironment []string
	Stdout          io.Writer
	Stderr          io.Writer
	Runner          Runner
}

type ApplyResult struct {
	Cluster string
}

func Apply(ctx context.Context, options ApplyOptions) (ApplyResult, error) {
	if options.Runner == nil {
		options.Runner = ExecRunner{}
	}
	if strings.TrimSpace(options.TerraformBinary) == "" {
		options.TerraformBinary = "terraform"
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.Stderr == nil {
		options.Stderr = io.Discard
	}
	if strings.TrimSpace(options.Credentials.TokenID) == "" || strings.TrimSpace(options.Credentials.TokenSecret) == "" {
		return ApplyResult{}, errors.New("Proxmox API token credentials are required")
	}

	cfg, err := loadProvisioningConfig(options.ConfigPath)
	if err != nil {
		return ApplyResult{}, err
	}
	if options.Approval != cfg.Metadata.Name {
		return ApplyResult{}, fmt.Errorf("approval must exactly match cluster name %q", cfg.Metadata.Name)
	}

	workspace, err := project.EnsureTerraformWorkspace(options.ConfigPath)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("prepare Terraform workspace: %w", err)
	}
	if err := project.RequireGeneratedDirectory(workspace.GeneratedDir, "terraform"); err != nil {
		return ApplyResult{}, fmt.Errorf("generated Terraform is not ready: %w", err)
	}
	if err := requireRegularFileOrMissing(workspace.StateFile, true); err != nil {
		return ApplyResult{}, fmt.Errorf("validate Terraform state path: %w", err)
	}
	if err := requireExistingRegularFile(workspace.LockFile, true); err != nil {
		return ApplyResult{}, fmt.Errorf("validate Terraform dependency lock: %w", err)
	}
	if err := requireExistingRegularFile(workspace.PlanFile, true); err != nil {
		return ApplyResult{}, fmt.Errorf("validate Terraform saved plan: %w", err)
	}
	if err := requireExistingRegularFile(workspace.PlanManifestFile, true); err != nil {
		return ApplyResult{}, fmt.Errorf("validate Terraform plan manifest: %w", err)
	}

	terraformVersion, err := readTerraformVersion(
		ctx,
		options.Runner,
		options.TerraformBinary,
		workspace.GeneratedDir,
		options.BaseEnvironment,
		workspace.DataDir,
	)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("read Terraform version: %w", err)
	}
	if _, err := VerifyPlanManifest(options.ConfigPath, workspace, terraformVersion); err != nil {
		return ApplyResult{}, fmt.Errorf("verify Terraform saved plan: %w", err)
	}

	generatedLock := filepath.Join(workspace.GeneratedDir, terraformLockFilename)
	copied, err := copyIfExists(workspace.LockFile, generatedLock, 0o644)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("restore Terraform dependency lock: %w", err)
	}
	if !copied {
		return ApplyResult{}, errors.New("Terraform dependency lock is missing; run terraform plan again")
	}

	environment := terraformEnvironment(options.BaseEnvironment, workspace.DataDir, options.Credentials)
	if err := runExpected(ctx, options.Runner, Command{
		Binary: options.TerraformBinary,
		Args:   []string{"init", "-backend=false", "-input=false", "-no-color", "-lockfile=readonly"},
		Dir:    workspace.GeneratedDir,
		Env:    environment,
		Stdout: options.Stdout,
		Stderr: options.Stderr,
	}, 0); err != nil {
		return ApplyResult{}, fmt.Errorf("terraform init failed: %w", err)
	}

	// Re-check all attested inputs after initialization and immediately before
	// invalidating the manifest and beginning the mutation attempt.
	if _, err := VerifyPlanManifest(options.ConfigPath, workspace, terraformVersion); err != nil {
		return ApplyResult{}, fmt.Errorf("verify Terraform saved plan after init: %w", err)
	}
	if err := RemovePlanManifest(workspace.PlanManifestFile); err != nil {
		return ApplyResult{}, fmt.Errorf("invalidate Terraform plan manifest before apply: %w", err)
	}

	applyArgs := []string{
		"apply",
		"-input=false",
		"-no-color",
		"-auto-approve",
		"-state=" + workspace.StateFile,
		"-state-out=" + workspace.StateFile,
		"-backup=" + workspace.StateBackupFile,
		workspace.PlanFile,
	}
	exitCode, err := options.Runner.Run(ctx, Command{
		Binary: options.TerraformBinary,
		Args:   applyArgs,
		Dir:    workspace.GeneratedDir,
		Env:    environment,
		Stdout: options.Stdout,
		Stderr: options.Stderr,
	})
	if err != nil {
		return ApplyResult{}, fmt.Errorf("run terraform apply: %w", err)
	}
	if exitCode != 0 {
		return ApplyResult{}, fmt.Errorf("terraform apply exited with code %d; the plan was invalidated and a new plan is required before retry", exitCode)
	}

	if err := removeRegularFileIfPresent(workspace.PlanFile); err != nil {
		return ApplyResult{}, fmt.Errorf("remove applied Terraform plan: %w", err)
	}
	return ApplyResult{Cluster: cfg.Metadata.Name}, nil
}

func loadProvisioningConfig(path string) (config.Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return config.Config{}, fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()

	cfg, err := config.Load(file)
	if err != nil {
		return config.Config{}, fmt.Errorf("load configuration: %w", err)
	}
	if err := cfg.ValidateProvisioning(); err != nil {
		return config.Config{}, fmt.Errorf("configuration is not provisioning-ready: %w", err)
	}
	return cfg, nil
}

func requireExistingRegularFile(path string, secure bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s is missing", path)
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular file", path)
	}
	if secure {
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
	}
	return nil
}
