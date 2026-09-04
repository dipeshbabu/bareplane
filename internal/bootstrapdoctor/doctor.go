package bootstrapdoctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/doctor"
	"github.com/dipeshbabu/bareplane/internal/project"
	ansiblerender "github.com/dipeshbabu/bareplane/internal/render/ansible"
)

type LookPathFunc func(string) (string, error)
type UserHomeDirFunc func() (string, error)

type Options struct {
	ConfigPath  string
	LookPath    LookPathFunc
	UserHomeDir UserHomeDirFunc
}

func Inspect(options Options) doctor.Report {
	if strings.TrimSpace(options.ConfigPath) == "" {
		options.ConfigPath = "bareplane.yaml"
	}
	if options.LookPath == nil {
		options.LookPath = func(name string) (string, error) { return "", fmt.Errorf("lookup for %s is not configured", name) }
	}
	if options.UserHomeDir == nil {
		options.UserHomeDir = os.UserHomeDir
	}

	cfg, configErr := loadBootstrapConfig(options.ConfigPath)
	results := []doctor.Result{configResult(configErr)}
	if configErr != nil {
		return doctor.Report{Results: results}
	}

	results = append(results,
		inventoryResult(options.ConfigPath),
		privateKeyResult(options.ConfigPath, cfg.Spec.Bootstrap.SSH.PrivateKeyFile, options.UserHomeDir),
		toolResult("ssh", options.LookPath),
		toolResult("ssh-keyscan", options.LookPath),
		toolResult("ansible-playbook", options.LookPath),
	)
	return doctor.Report{Results: results}
}

func loadBootstrapConfig(path string) (config.Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return config.Config{}, fmt.Errorf("open configuration: %w", err)
	}
	cfg, loadErr := config.Load(file)
	closeErr := file.Close()
	if loadErr != nil {
		return config.Config{}, loadErr
	}
	if closeErr != nil {
		return config.Config{}, fmt.Errorf("close configuration: %w", closeErr)
	}
	if err := cfg.ValidateBootstrap(); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func configResult(err error) doctor.Result {
	if err != nil {
		return doctor.Result{Name: "bootstrap-config", Status: doctor.StatusFail, Message: err.Error()}
	}
	return doctor.Result{Name: "bootstrap-config", Status: doctor.StatusPass, Message: "bootstrap configuration is valid"}
}

func inventoryResult(configPath string) doctor.Result {
	destination := filepath.Join(filepath.Dir(filepath.Clean(configPath)), ".bareplane", "bootstrap")
	if err := project.RequireGeneratedDirectory(destination, "bootstrap"); err != nil {
		return doctor.Result{Name: "inventory", Status: doctor.StatusFail, Message: "run bareplane bootstrap render: " + err.Error()}
	}
	path := filepath.Join(destination, ansiblerender.InventoryFilename)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return doctor.Result{Name: "inventory", Status: doctor.StatusFail, Message: "generated inventory is missing; run bareplane bootstrap render"}
		}
		return doctor.Result{Name: "inventory", Status: doctor.StatusFail, Message: "inspect generated inventory: " + err.Error()}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return doctor.Result{Name: "inventory", Status: doctor.StatusFail, Message: "generated inventory must be a regular file"}
	}
	return doctor.Result{Name: "inventory", Status: doctor.StatusPass, Message: "managed bootstrap inventory is ready"}
}

func privateKeyResult(configPath, configured string, homeDir UserHomeDirFunc) doctor.Result {
	path, err := resolvePath(configPath, configured, homeDir)
	if err != nil {
		return doctor.Result{Name: "ssh-private-key", Status: doctor.StatusFail, Message: "resolve configured private key: " + err.Error()}
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return doctor.Result{Name: "ssh-private-key", Status: doctor.StatusFail, Message: "configured private key file does not exist"}
		}
		return doctor.Result{Name: "ssh-private-key", Status: doctor.StatusFail, Message: "inspect configured private key: " + err.Error()}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return doctor.Result{Name: "ssh-private-key", Status: doctor.StatusFail, Message: "configured private key must be a regular file and not a symlink"}
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return doctor.Result{Name: "ssh-private-key", Status: doctor.StatusFail, Message: fmt.Sprintf("configured private key permissions are %04o; restrict them to 0600 or stricter", info.Mode().Perm())}
	}
	return doctor.Result{Name: "ssh-private-key", Status: doctor.StatusPass, Message: "configured private key file is locally usable"}
}

func toolResult(name string, lookPath LookPathFunc) doctor.Result {
	if _, err := lookPath(name); err != nil {
		return doctor.Result{Name: name, Status: doctor.StatusFail, Message: name + " executable was not found"}
	}
	return doctor.Result{Name: name, Status: doctor.StatusPass, Message: name + " executable is available"}
}

func resolvePath(configPath, value string, homeDir UserHomeDirFunc) (string, error) {
	if strings.HasPrefix(value, "~/") {
		home, err := homeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(value, "~/"))), nil
	}
	if strings.HasPrefix(value, "~") {
		return "", errors.New("only ~/ home-relative paths are supported")
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	return filepath.Join(filepath.Dir(filepath.Clean(configPath)), filepath.FromSlash(value)), nil
}
