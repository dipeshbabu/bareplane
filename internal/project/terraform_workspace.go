package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const privateDirectoryMode os.FileMode = 0o700

type TerraformWorkspace struct {
	ProjectRoot      string
	GeneratedDir     string
	StateDir         string
	DataDir          string
	StateFile        string
	StateBackupFile  string
	LockFile         string
	PlanFile         string
	PlanManifestFile string
}

func TerraformWorkspaceFor(configPath string) (TerraformWorkspace, error) {
	if configPath == "" {
		return TerraformWorkspace{}, errors.New("configuration path is empty")
	}

	absoluteConfig, err := filepath.Abs(configPath)
	if err != nil {
		return TerraformWorkspace{}, fmt.Errorf("resolve configuration path: %w", err)
	}
	root := filepath.Dir(filepath.Clean(absoluteConfig))
	bareplaneRoot := filepath.Join(root, ".bareplane")
	stateDir := filepath.Join(bareplaneRoot, "state", "terraform")

	return TerraformWorkspace{
		ProjectRoot:      root,
		GeneratedDir:     filepath.Join(bareplaneRoot, "terraform"),
		StateDir:         stateDir,
		DataDir:          filepath.Join(stateDir, "data"),
		StateFile:        filepath.Join(stateDir, "terraform.tfstate"),
		StateBackupFile:  filepath.Join(stateDir, "terraform.tfstate.backup"),
		LockFile:         filepath.Join(stateDir, ".terraform.lock.hcl"),
		PlanFile:         filepath.Join(stateDir, "terraform.tfplan"),
		PlanManifestFile: filepath.Join(stateDir, "terraform.tfplan.json"),
	}, nil
}

func EnsureTerraformWorkspace(configPath string) (TerraformWorkspace, error) {
	workspace, err := TerraformWorkspaceFor(configPath)
	if err != nil {
		return TerraformWorkspace{}, err
	}

	bareplaneRoot := filepath.Join(workspace.ProjectRoot, ".bareplane")
	stateRoot := filepath.Join(bareplaneRoot, "state")
	for _, directory := range []struct {
		path string
		mode os.FileMode
	}{
		{path: bareplaneRoot, mode: 0o755},
		{path: stateRoot, mode: privateDirectoryMode},
		{path: workspace.StateDir, mode: privateDirectoryMode},
		{path: workspace.DataDir, mode: privateDirectoryMode},
	} {
		if err := ensureWorkspaceDirectory(directory.path, directory.mode); err != nil {
			return TerraformWorkspace{}, err
		}
	}

	return workspace, nil
}

func ensureWorkspaceDirectory(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("terraform workspace path %s must not be a symlink", path)
		}
		if !info.IsDir() {
			return fmt.Errorf("terraform workspace path %s must be a directory", path)
		}
		if mode == privateDirectoryMode {
			if err := os.Chmod(path, privateDirectoryMode); err != nil {
				return fmt.Errorf("secure terraform workspace directory %s: %w", path, err)
			}
		}
		return nil
	case errors.Is(err, os.ErrNotExist):
		if err := os.Mkdir(path, mode); err != nil {
			return fmt.Errorf("create terraform workspace directory %s: %w", path, err)
		}
		return nil
	default:
		return fmt.Errorf("inspect terraform workspace directory %s: %w", path, err)
	}
}
