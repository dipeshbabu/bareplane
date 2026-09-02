package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/project"
	terraformrender "github.com/dipeshbabu/bareplane/internal/render/terraform"
)

const maxPublicKeyBytes = 64 * 1024

func runRender(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: bareplane render [path]")
		return 2
	}

	configPath := "bareplane.yaml"
	if len(args) == 1 {
		configPath = args[0]
	}

	cfg, err := loadRenderConfig(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "render %s: %v\n", configPath, err)
		return 1
	}

	operationLock, err := project.AcquireTerraformOperation(configPath, "render")
	if err != nil {
		fmt.Fprintf(stderr, "render %s: acquire Terraform operation lock: %v\n", configPath, err)
		return 1
	}
	code := runRenderLocked(configPath, cfg, stdout, stderr)
	if err := operationLock.Release(); err != nil {
		fmt.Fprintf(stderr, "render %s: release Terraform operation lock: %v\n", configPath, err)
		return 1
	}
	return code
}

func runRenderLocked(configPath string, cfg config.Config, stdout, stderr io.Writer) int {
	publicKeyPath, err := resolveConfiguredPath(configPath, cfg.Spec.Provider.Proxmox.SSH.PublicKeyFile)
	if err != nil {
		fmt.Fprintf(stderr, "render %s: resolve SSH public key: %v\n", configPath, err)
		return 1
	}
	publicKey, err := readBoundedFile(publicKeyPath, maxPublicKeyBytes)
	if err != nil {
		fmt.Fprintf(stderr, "render %s: read SSH public key: %v\n", configPath, err)
		return 1
	}

	mainFile, err := terraformrender.RenderProxmox(cfg, string(publicKey))
	if err != nil {
		fmt.Fprintf(stderr, "render %s: %v\n", configPath, err)
		return 1
	}

	destination := filepath.Join(filepath.Dir(filepath.Clean(configPath)), ".bareplane", "terraform")
	if err := project.ReplaceGeneratedDirectory(destination, "terraform", map[string][]byte{
		terraformrender.MainFilename: mainFile,
	}); err != nil {
		if errors.Is(err, project.ErrUnmanagedDestination) {
			fmt.Fprintf(stderr, "render %s: refusing to replace unmanaged output directory %s\n", configPath, destination)
			return 1
		}
		fmt.Fprintf(stderr, "render %s: write generated Terraform: %v\n", configPath, err)
		return 1
	}

	fmt.Fprintf(stdout, "rendered Terraform to %s\n", destination)
	return 0
}

func loadRenderConfig(path string) (config.Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return config.Config{}, fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()

	cfg, err := config.Load(file)
	if err != nil {
		return config.Config{}, err
	}
	if err := cfg.ValidateProvisioning(); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func resolveConfiguredPath(configPath, value string) (string, error) {
	if strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(value, "~/"))), nil
	}
	if strings.HasPrefix(value, "~") {
		return "", fmt.Errorf("only ~/ home-relative paths are supported")
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	return filepath.Join(filepath.Dir(filepath.Clean(configPath)), filepath.FromSlash(value)), nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, limit)
	}
	return data, nil
}
