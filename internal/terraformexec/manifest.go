package terraformexec

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/project"
)

const (
	planManifestVersion   = 1
	maxMetadataHashBytes  = 64 * 1024 * 1024
	maxPlanManifestBytes  = 64 * 1024
	terraformLockFilename = ".terraform.lock.hcl"
)

type PlanManifest struct {
	Version            int    `json:"version"`
	ClusterName        string `json:"clusterName"`
	TerraformVersion   string `json:"terraformVersion"`
	ConfigSHA256       string `json:"configSHA256"`
	GeneratedSHA256    string `json:"generatedSHA256"`
	ProviderLockSHA256 string `json:"providerLockSHA256"`
	PlanSHA256         string `json:"planSHA256"`
}

func CreatePlanManifest(configPath string, workspace project.TerraformWorkspace, terraformVersion string) (PlanManifest, error) {
	clusterName, err := manifestClusterName(configPath)
	if err != nil {
		return PlanManifest{}, err
	}
	terraformVersion = strings.TrimSpace(terraformVersion)
	if terraformVersion == "" {
		return PlanManifest{}, errors.New("Terraform version is empty")
	}

	configDigest, err := hashRegularFile(configPath, maxMetadataHashBytes)
	if err != nil {
		return PlanManifest{}, fmt.Errorf("hash Bareplane configuration: %w", err)
	}
	generatedDigest, err := hashGeneratedDirectory(workspace.GeneratedDir)
	if err != nil {
		return PlanManifest{}, fmt.Errorf("hash generated Terraform: %w", err)
	}
	lockDigest, err := hashRegularFile(workspace.LockFile, maxMetadataHashBytes)
	if err != nil {
		return PlanManifest{}, fmt.Errorf("hash Terraform dependency lock: %w", err)
	}
	planDigest, err := hashRegularFile(workspace.PlanFile, 0)
	if err != nil {
		return PlanManifest{}, fmt.Errorf("hash Terraform saved plan: %w", err)
	}

	manifest := PlanManifest{
		Version:            planManifestVersion,
		ClusterName:        clusterName,
		TerraformVersion:   terraformVersion,
		ConfigSHA256:       configDigest,
		GeneratedSHA256:    generatedDigest,
		ProviderLockSHA256: lockDigest,
		PlanSHA256:         planDigest,
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return PlanManifest{}, fmt.Errorf("encode Terraform plan manifest: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := writeAtomic(workspace.PlanManifestFile, encoded, 0o600); err != nil {
		return PlanManifest{}, fmt.Errorf("write Terraform plan manifest: %w", err)
	}
	return manifest, nil
}

func VerifyPlanManifest(configPath string, workspace project.TerraformWorkspace) (PlanManifest, error) {
	manifest, err := readPlanManifest(workspace.PlanManifestFile)
	if err != nil {
		return PlanManifest{}, err
	}
	if manifest.Version != planManifestVersion {
		return PlanManifest{}, fmt.Errorf("unsupported Terraform plan manifest version %d", manifest.Version)
	}

	clusterName, err := manifestClusterName(configPath)
	if err != nil {
		return PlanManifest{}, err
	}
	if manifest.ClusterName != clusterName {
		return PlanManifest{}, fmt.Errorf("Terraform plan manifest cluster mismatch: planned %q, current %q", manifest.ClusterName, clusterName)
	}

	checks := []struct {
		name string
		want string
		hash func() (string, error)
	}{
		{name: "Bareplane configuration", want: manifest.ConfigSHA256, hash: func() (string, error) {
			return hashRegularFile(configPath, maxMetadataHashBytes)
		}},
		{name: "generated Terraform", want: manifest.GeneratedSHA256, hash: func() (string, error) {
			return hashGeneratedDirectory(workspace.GeneratedDir)
		}},
		{name: "Terraform dependency lock", want: manifest.ProviderLockSHA256, hash: func() (string, error) {
			return hashRegularFile(workspace.LockFile, maxMetadataHashBytes)
		}},
		{name: "Terraform saved plan", want: manifest.PlanSHA256, hash: func() (string, error) {
			return hashRegularFile(workspace.PlanFile, 0)
		}},
	}
	for _, check := range checks {
		got, err := check.hash()
		if err != nil {
			return PlanManifest{}, fmt.Errorf("verify %s: %w", check.name, err)
		}
		if !validDigest(check.want) || got != check.want {
			return PlanManifest{}, fmt.Errorf("Terraform plan is stale: %s changed since planning", check.name)
		}
	}
	return manifest, nil
}

func RemovePlanManifest(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect Terraform plan manifest: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("Terraform plan manifest %s must be a regular file", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove Terraform plan manifest: %w", err)
	}
	return nil
}

func readPlanManifest(path string) (PlanManifest, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PlanManifest{}, errors.New("Terraform plan manifest is missing; run terraform plan again")
		}
		return PlanManifest{}, fmt.Errorf("inspect Terraform plan manifest: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return PlanManifest{}, fmt.Errorf("Terraform plan manifest %s must be a regular file", path)
	}
	if info.Size() > maxPlanManifestBytes {
		return PlanManifest{}, fmt.Errorf("Terraform plan manifest exceeds %d bytes", maxPlanManifestBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return PlanManifest{}, fmt.Errorf("read Terraform plan manifest: %w", err)
	}
	var manifest PlanManifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return PlanManifest{}, fmt.Errorf("decode Terraform plan manifest: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return PlanManifest{}, err
	}
	return manifest, nil
}

func manifestClusterName(configPath string) (string, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return "", fmt.Errorf("open Bareplane configuration: %w", err)
	}
	defer file.Close()
	cfg, err := config.Load(file)
	if err != nil {
		return "", fmt.Errorf("load Bareplane configuration: %w", err)
	}
	return cfg.Metadata.Name, nil
}

func hashGeneratedDirectory(directory string) (string, error) {
	if err := project.RequireGeneratedDirectory(directory, "terraform"); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == terraformLockFilename {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("generated Terraform entry %q must be a regular file", entry.Name())
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", errors.New("generated Terraform file set is empty")
	}

	hasher := sha256.New()
	writer := bufio.NewWriter(hasher)
	for _, name := range names {
		digest, err := hashRegularFile(filepath.Join(directory, name), maxMetadataHashBytes)
		if err != nil {
			return "", fmt.Errorf("hash generated file %q: %w", name, err)
		}
		if _, err := fmt.Fprintf(writer, "%d:%s\x00%s\n", len(name), name, digest); err != nil {
			return "", err
		}
	}
	if err := writer.Flush(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func hashRegularFile(path string, maxBytes int64) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s must be a regular file", path)
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return "", fmt.Errorf("%s exceeds %d bytes", path, maxBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("Terraform plan manifest contains multiple JSON values")
		}
		return fmt.Errorf("decode trailing Terraform plan manifest data: %w", err)
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
