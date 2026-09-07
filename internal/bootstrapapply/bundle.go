package bootstrapapply

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/project"
	ansible "github.com/dipeshbabu/bareplane/internal/render/ansible"
)

func loadConfiguration(path string) (config.Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return config.Config{}, errors.New("bootstrap configuration is unavailable")
	}
	cfg, err := config.Load(file)
	closeErr := file.Close()
	if err != nil || closeErr != nil || cfg.ValidateKubernetesBootstrap() != nil {
		return config.Config{}, errors.New("bootstrap configuration is invalid; run bareplane validate and bootstrap doctor")
	}
	return cfg, nil
}

func safeControllerPath(path string) bool {
	return !strings.Contains(path, "${") && strings.IndexFunc(path, func(value rune) bool { return !unicode.IsPrint(value) }) < 0
}

// verifyBundle checks exact current embedded bytes, not just a management
// marker. Extra files/directories, stale renders, and redirected paths fail.
func verifyBundle(configPath string, cfg config.Config) (string, string, error) {
	bundle := filepath.Join(filepath.Dir(configPath), ".bareplane", "bootstrap")
	if !safeControllerPath(bundle) {
		return "", "", errors.New("controller paths must not contain control characters or OpenSSH environment expansions")
	}
	if _, err := project.InspectBootstrapState(configPath); err != nil {
		return "", "", err
	}
	if err := project.RequireGeneratedDirectory(bundle, "bootstrap"); err != nil {
		return "", "", errors.New("a managed bootstrap bundle is required; run bareplane bootstrap render")
	}
	files, err := ansible.RenderBundle(cfg)
	if err != nil {
		return "", "", errors.New("cannot render the approved bootstrap contract")
	}
	directories := map[string]bool{".": true}
	for name := range files {
		for parent := filepath.Dir(name); parent != "."; parent = filepath.Dir(parent) {
			directories[filepath.ToSlash(parent)] = true
		}
	}
	seen := 0
	err = filepath.WalkDir(bundle, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot inspect the managed bootstrap bundle")
		}
		relative, err := filepath.Rel(bundle, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("bootstrap bundle paths must not be symlinks")
		}
		if entry.IsDir() {
			if !directories[relative] {
				return errors.New("bootstrap bundle contains an unexpected directory; rerender it")
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("bootstrap bundle entries must be regular files")
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
			return errors.New("bootstrap bundle files must not be group- or world-writable")
		}
		if relative == project.GeneratedMarkerFilename {
			return nil
		}
		expected, ok := files[relative]
		if !ok || info.Size() != int64(len(expected)) {
			return errors.New("bootstrap bundle differs from the owned render; rerender it")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		actual, readErr := io.ReadAll(io.LimitReader(file, int64(len(expected))+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || !bytes.Equal(actual, expected) {
			return errors.New("bootstrap bundle differs from the owned render; rerender it")
		}
		seen++
		return nil
	})
	if err != nil {
		return "", "", err
	}
	if seen != len(files) {
		return "", "", errors.New("bootstrap bundle is incomplete; rerender it")
	}
	// Bind semantic bootstrap inputs, not implementation bytes: a reviewed new
	// Bareplane binary can fix a phase after rerendering without adopting a new
	// cluster/network/topology. Private key contents are never read or hashed.
	hash := sha256.New()
	for _, name := range []string{ansible.InventoryFilename, "group_vars/all/cluster.yaml"} {
		fmt.Fprintf(hash, "%d:", len(files[name]))
		hash.Write(files[name])
	}
	key := cfg.Spec.Bootstrap.SSH.PrivateKeyFile
	fmt.Fprintf(hash, "%d:%s", len(key), key)
	return bundle, hex.EncodeToString(hash.Sum(nil)), nil
}

func fileFingerprint(path string, limit int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("trusted SSH host state is unavailable")
	}
	defer file.Close()
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(file, limit+1))
	if err != nil || count > limit {
		return "", errors.New("trusted SSH host state could not be read safely")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
