package project

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTerraformOperationLockMutualExclusionAndRelease(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "bareplane.yaml")
	first, err := AcquireTerraformOperation(configPath, "terraform-plan")
	if err != nil {
		t.Fatal(err)
	}

	_, err = AcquireTerraformOperation(configPath, "render")
	if !errors.Is(err, ErrTerraformOperationLocked) {
		t.Fatalf("expected operation lock conflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "terraform-plan") || !strings.Contains(err.Error(), "pid") {
		t.Fatalf("lock conflict lacks diagnostics: %v", err)
	}

	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireTerraformOperation(configPath, "render")
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("release must be idempotent: %v", err)
	}
}

func TestTerraformOperationLocksAreProjectScoped(t *testing.T) {
	firstPath := filepath.Join(t.TempDir(), "bareplane.yaml")
	secondPath := filepath.Join(t.TempDir(), "bareplane.yaml")
	first, err := AcquireTerraformOperation(firstPath, "render")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := AcquireTerraformOperation(secondPath, "render")
	if err != nil {
		t.Fatalf("different project was unexpectedly blocked: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestTerraformOperationLockMetadataIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission assertion")
	}
	configPath := filepath.Join(t.TempDir(), "bareplane.yaml")
	lock, err := AcquireTerraformOperation(configPath, "terraform-plan")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	dirInfo, err := os.Stat(lock.path)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("lock directory permissions = %o, want 700", got)
	}
	metadataInfo, err := os.Stat(filepath.Join(lock.path, terraformOperationLockMetadata))
	if err != nil {
		t.Fatal(err)
	}
	if got := metadataInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("lock metadata permissions = %o, want 600", got)
	}
}

func TestTerraformOperationLockReportsReadableStaleLock(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "bareplane.yaml")
	workspace, err := EnsureTerraformWorkspace(configPath)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(workspace.StateDir, terraformOperationLockDir)
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"version":1,"operation":"render","pid":99999,"token":"other"}`)
	metadata = []byte(strings.ReplaceAll(string(metadata), `\"`, `"`))
	if err := os.WriteFile(filepath.Join(lockPath, terraformOperationLockMetadata), metadata, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = AcquireTerraformOperation(configPath, "terraform-plan")
	if !errors.Is(err, ErrTerraformOperationLocked) {
		t.Fatalf("expected stale lock refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), `operation "render"`) || !strings.Contains(err.Error(), "99999") {
		t.Fatalf("expected readable stale-lock diagnostics, got %v", err)
	}
}

func TestTerraformOperationLockRefusesSymlinkLockPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior depends on Windows privileges")
	}
	configPath := filepath.Join(t.TempDir(), "bareplane.yaml")
	workspace, err := EnsureTerraformWorkspace(configPath)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	lockPath := filepath.Join(workspace.StateDir, terraformOperationLockDir)
	if err := os.Symlink(target, lockPath); err != nil {
		t.Fatal(err)
	}
	_, err = AcquireTerraformOperation(configPath, "render")
	if !errors.Is(err, ErrTerraformOperationLocked) {
		t.Fatalf("expected symlink lock refusal, got %v", err)
	}
}

func TestTerraformOperationLockReleaseChecksOwnership(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "bareplane.yaml")
	lock, err := AcquireTerraformOperation(configPath, "render")
	if err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(lock.path, terraformOperationLockMetadata)
	data := []byte(`{"version":1,"operation":"render","pid":1,"token":"replacement-owner"}`)
	data = []byte(strings.ReplaceAll(string(data), `\"`, `"`))
	if err := os.WriteFile(metadataPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err == nil || !strings.Contains(err.Error(), "another operation") {
		t.Fatalf("expected ownership refusal, got %v", err)
	}
	if _, err := os.Stat(lock.path); err != nil {
		t.Fatalf("unowned lock was removed: %v", err)
	}
}

func TestTerraformOperationLockRejectsInvalidOperationName(t *testing.T) {
	if _, err := AcquireTerraformOperation(filepath.Join(t.TempDir(), "bareplane.yaml"), "Terraform Apply"); err == nil {
		t.Fatal("expected invalid operation name error")
	}
}
