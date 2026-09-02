package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/project"
)

func TestRunRenderRefusesConcurrentTerraformOperation(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")
	keyPath := filepath.Join(root, "id_ed25519.pub")
	writeRenderFixture(t, configPath, keyPath)

	lock, err := project.AcquireTerraformOperation(configPath, "terraform-plan")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	var stdout, stderr bytes.Buffer
	code := runRender([]string{configPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected lock refusal exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "operation lock") {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".bareplane", "terraform")); !os.IsNotExist(err) {
		t.Fatalf("render changed generated output while another operation held the lock: %v", err)
	}
}

func TestRunRenderReleasesLockAfterRenderError(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")
	keyPath := filepath.Join(root, "id_ed25519.pub")
	writeRenderFixture(t, configPath, keyPath)
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := runRender([]string{configPath}, &stdout, &stderr); code != 1 {
		t.Fatalf("expected render error, got %d", code)
	}

	lock, err := project.AcquireTerraformOperation(configPath, "terraform-plan")
	if err != nil {
		t.Fatalf("render error left operation lock behind: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}
