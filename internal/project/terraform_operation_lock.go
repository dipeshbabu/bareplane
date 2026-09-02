package project

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	terraformOperationLockDir      = ".operation.lock"
	terraformOperationLockMetadata = "owner.json"
	maxOperationLockMetadataBytes  = 4096
)

var (
	ErrTerraformOperationLocked = errors.New("another Bareplane Terraform operation holds the project lock")
	operationNamePattern        = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

type operationLockMetadata struct {
	Version   int    `json:"version"`
	Operation string `json:"operation"`
	PID       int    `json:"pid"`
	Token     string `json:"token"`
}

type TerraformOperationLock struct {
	path     string
	token    string
	released bool
}

func AcquireTerraformOperation(configPath, operation string) (*TerraformOperationLock, error) {
	if !operationNamePattern.MatchString(operation) {
		return nil, fmt.Errorf("invalid Terraform operation name %q", operation)
	}
	workspace, err := EnsureTerraformWorkspace(configPath)
	if err != nil {
		return nil, err
	}

	token, err := randomOperationToken()
	if err != nil {
		return nil, fmt.Errorf("create Terraform operation token: %w", err)
	}
	lockPath := filepath.Join(workspace.StateDir, terraformOperationLockDir)
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, describeExistingOperationLock(lockPath)
		}
		return nil, fmt.Errorf("create Terraform operation lock: %w", err)
	}

	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(lockPath)
		}
	}()

	metadata := operationLockMetadata{
		Version:   1,
		Operation: operation,
		PID:       os.Getpid(),
		Token:     token,
	}
	encoded, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode Terraform operation lock metadata: %w", err)
	}
	encoded = append(encoded, '\n')
	metadataPath := filepath.Join(lockPath, terraformOperationLockMetadata)
	file, err := os.OpenFile(metadataPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create Terraform operation lock metadata: %w", err)
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write Terraform operation lock metadata: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync Terraform operation lock metadata: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close Terraform operation lock metadata: %w", err)
	}

	cleanup = false
	return &TerraformOperationLock{path: lockPath, token: token}, nil
}

func (l *TerraformOperationLock) Release() error {
	if l == nil || l.released {
		return nil
	}
	metadata, err := readOperationLockMetadata(l.path)
	if err != nil {
		return fmt.Errorf("verify Terraform operation lock ownership: %w", err)
	}
	if metadata.Token != l.token {
		return errors.New("refusing to release Terraform operation lock owned by another operation")
	}

	metadataPath := filepath.Join(l.path, terraformOperationLockMetadata)
	if err := os.Remove(metadataPath); err != nil {
		return fmt.Errorf("remove Terraform operation lock metadata: %w", err)
	}
	if err := os.Remove(l.path); err != nil {
		return fmt.Errorf("remove Terraform operation lock: %w", err)
	}
	l.released = true
	return nil
}

func describeExistingOperationLock(lockPath string) error {
	info, err := os.Lstat(lockPath)
	if err != nil {
		return fmt.Errorf("inspect existing Terraform operation lock: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: lock path %s is not a regular directory", ErrTerraformOperationLocked, lockPath)
	}
	metadata, metadataErr := readOperationLockMetadata(lockPath)
	if metadataErr == nil {
		return fmt.Errorf("%w: operation %q (pid %d) at %s; if that process is no longer running, remove the lock directory manually", ErrTerraformOperationLocked, metadata.Operation, metadata.PID, lockPath)
	}
	return fmt.Errorf("%w: %s exists but its metadata is unreadable; if no Bareplane operation is running, inspect and remove the lock directory manually", ErrTerraformOperationLocked, lockPath)
}

func readOperationLockMetadata(lockPath string) (operationLockMetadata, error) {
	metadataPath := filepath.Join(lockPath, terraformOperationLockMetadata)
	info, err := os.Lstat(metadataPath)
	if err != nil {
		return operationLockMetadata{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return operationLockMetadata{}, errors.New("Terraform operation lock metadata must be a regular file")
	}
	if info.Size() > maxOperationLockMetadataBytes {
		return operationLockMetadata{}, fmt.Errorf("Terraform operation lock metadata exceeds %d bytes", maxOperationLockMetadataBytes)
	}
	file, err := os.Open(metadataPath)
	if err != nil {
		return operationLockMetadata{}, err
	}
	defer file.Close()

	decoder := json.NewDecoder(io.LimitReader(file, maxOperationLockMetadataBytes+1))
	decoder.DisallowUnknownFields()
	var metadata operationLockMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return operationLockMetadata{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return operationLockMetadata{}, errors.New("Terraform operation lock metadata contains multiple JSON values")
		}
		return operationLockMetadata{}, err
	}
	if metadata.Version != 1 || !operationNamePattern.MatchString(metadata.Operation) || metadata.PID <= 0 || strings.TrimSpace(metadata.Token) == "" {
		return operationLockMetadata{}, errors.New("Terraform operation lock metadata is invalid")
	}
	return metadata, nil
}

func randomOperationToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
