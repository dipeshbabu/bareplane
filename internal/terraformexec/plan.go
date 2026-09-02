package terraformexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/project"
	"github.com/dipeshbabu/bareplane/internal/provider/proxmox"
)

const (
	providerTokenEnv = "PROXMOX_VE_API_TOKEN"
	maxLockFileBytes = 4 * 1024 * 1024
)

type Command struct {
	Binary string
	Args   []string
	Dir    string
	Env    []string
	Stdout io.Writer
	Stderr io.Writer
}

type Runner interface {
	Run(context.Context, Command) (int, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, command Command) (int, error) {
	cmd := exec.CommandContext(ctx, command.Binary, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = command.Env
	cmd.Stdout = command.Stdout
	cmd.Stderr = command.Stderr

	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return -1, err
}

type PlanOptions struct {
	ConfigPath      string
	TerraformBinary string
	Credentials     proxmox.Credentials
	BaseEnvironment []string
	Stdout          io.Writer
	Stderr          io.Writer
	Runner          Runner
}

type PlanResult struct {
	Changes bool
}

func Plan(ctx context.Context, options PlanOptions) (PlanResult, error) {
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
		return PlanResult{}, errors.New("Proxmox API token credentials are required")
	}
	if err := validateProvisioningConfig(options.ConfigPath); err != nil {
		return PlanResult{}, err
	}

	workspace, err := project.EnsureTerraformWorkspace(options.ConfigPath)
	if err != nil {
		return PlanResult{}, fmt.Errorf("prepare Terraform workspace: %w", err)
	}
	if err := project.RequireGeneratedDirectory(workspace.GeneratedDir, "terraform"); err != nil {
		return PlanResult{}, fmt.Errorf("generated Terraform is not ready: %w", err)
	}
	if err := requireRegularFileOrMissing(workspace.StateFile, true); err != nil {
		return PlanResult{}, fmt.Errorf("validate Terraform state path: %w", err)
	}
	if err := removeRegularFileIfPresent(workspace.PlanFile); err != nil {
		return PlanResult{}, fmt.Errorf("prepare Terraform plan path: %w", err)
	}

	generatedLock := filepath.Join(workspace.GeneratedDir, ".terraform.lock.hcl")
	lockExists, err := copyIfExists(workspace.LockFile, generatedLock, 0o644)
	if err != nil {
		return PlanResult{}, fmt.Errorf("restore Terraform dependency lock: %w", err)
	}

	environment := terraformEnvironment(options.BaseEnvironment, workspace.DataDir, options.Credentials)
	initArgs := []string{"init", "-backend=false", "-input=false", "-no-color"}
	if lockExists {
		initArgs = append(initArgs, "-lockfile=readonly")
	}
	if err := runExpected(ctx, options.Runner, Command{
		Binary: options.TerraformBinary,
		Args:   initArgs,
		Dir:    workspace.GeneratedDir,
		Env:    environment,
		Stdout: options.Stdout,
		Stderr: options.Stderr,
	}, 0); err != nil {
		return PlanResult{}, fmt.Errorf("terraform init failed: %w", err)
	}

	if _, err := copyIfExists(generatedLock, workspace.LockFile, 0o600); err != nil {
		return PlanResult{}, fmt.Errorf("persist Terraform dependency lock: %w", err)
	}

	planArgs := []string{
		"plan",
		"-input=false",
		"-no-color",
		"-detailed-exitcode",
		"-state=" + workspace.StateFile,
		"-out=" + workspace.PlanFile,
	}
	exitCode, err := options.Runner.Run(ctx, Command{
		Binary: options.TerraformBinary,
		Args:   planArgs,
		Dir:    workspace.GeneratedDir,
		Env:    environment,
		Stdout: options.Stdout,
		Stderr: options.Stderr,
	})
	if err != nil {
		return PlanResult{}, fmt.Errorf("run terraform plan: %w", err)
	}
	switch exitCode {
	case 0:
		if err := securePlanIfPresent(workspace.PlanFile); err != nil {
			return PlanResult{}, err
		}
		return PlanResult{Changes: false}, nil
	case 2:
		if err := securePlanIfPresent(workspace.PlanFile); err != nil {
			return PlanResult{}, err
		}
		return PlanResult{Changes: true}, nil
	default:
		return PlanResult{}, fmt.Errorf("terraform plan exited with code %d", exitCode)
	}
}

func validateProvisioningConfig(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()

	cfg, err := config.Load(file)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if err := cfg.ValidateProvisioning(); err != nil {
		return fmt.Errorf("configuration is not provisioning-ready: %w", err)
	}
	return nil
}

func runExpected(ctx context.Context, runner Runner, command Command, expected int) error {
	exitCode, err := runner.Run(ctx, command)
	if err != nil {
		return err
	}
	if exitCode != expected {
		return fmt.Errorf("command exited with code %d", exitCode)
	}
	return nil
}

func terraformEnvironment(base []string, dataDir string, credentials proxmox.Credentials) []string {
	if base == nil {
		base = os.Environ()
	}
	filtered := make([]string, 0, len(base)+2)
	for _, entry := range base {
		if environmentKey(entry) == providerTokenEnv || environmentKey(entry) == "TF_DATA_DIR" {
			continue
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered,
		"TF_DATA_DIR="+dataDir,
		providerTokenEnv+"="+strings.TrimSpace(credentials.TokenID)+"="+strings.TrimSpace(credentials.TokenSecret),
	)
	return filtered
}

func environmentKey(entry string) string {
	if index := strings.IndexByte(entry, '='); index >= 0 {
		return entry[:index]
	}
	return entry
}

func copyIfExists(source, destination string, mode os.FileMode) (bool, error) {
	info, err := os.Lstat(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s must be a regular file", source)
	}
	if info.Size() > maxLockFileBytes {
		return false, fmt.Errorf("%s exceeds %d bytes", source, maxLockFileBytes)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return false, err
	}
	if err := writeAtomic(destination, data, mode); err != nil {
		return false, err
	}
	return true, nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := requireRegularFileOrMissing(path, false); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".bareplane-write-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return err
	}
	remove = false
	return nil
}

func replaceFile(source, destination string) error {
	if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
		return os.Rename(source, destination)
	} else if err != nil {
		return err
	}

	backup := destination + ".bareplane-backup"
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(destination, backup); err != nil {
		return err
	}
	if err := os.Rename(source, destination); err != nil {
		_ = os.Rename(backup, destination)
		return err
	}
	if err := os.Remove(backup); err != nil {
		return err
	}
	return nil
}

func requireRegularFileOrMissing(path string, secure bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
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

func removeRegularFileIfPresent(path string) error {
	if err := requireRegularFileOrMissing(path, true); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func securePlanIfPresent(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect Terraform plan artifact: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("Terraform plan artifact %s must be a regular file", path)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure Terraform plan artifact: %w", err)
	}
	return nil
}
