package terraformexec

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/project"
)

func TestPlanManifestRoundTripIsDeterministic(t *testing.T) {
	configPath, workspace := setupManifestContext(t)
	first, err := CreatePlanManifest(configPath, workspace, "1.16.0")
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, err := os.ReadFile(workspace.PlanManifestFile)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreatePlanManifest(configPath, workspace, "1.16.0")
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(workspace.PlanManifestFile)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || string(firstBytes) != string(secondBytes) {
		t.Fatal("identical plan context produced a different manifest")
	}
	verified, err := VerifyPlanManifest(configPath, workspace, "1.16.0")
	if err != nil {
		t.Fatal(err)
	}
	if verified != first {
		t.Fatalf("verified manifest = %#v, want %#v", verified, first)
	}
	for _, digest := range []string{first.ConfigSHA256, first.GeneratedSHA256, first.ProviderLockSHA256, first.PlanSHA256} {
		if !validDigest(digest) {
			t.Fatalf("invalid SHA-256 digest %q", digest)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(workspace.PlanManifestFile)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("manifest permissions = %o, want 600", got)
		}
	}
}

func TestVerifyPlanManifestDetectsChangedConfig(t *testing.T) {
	configPath, workspace := setupManifestContext(t)
	if _, err := CreatePlanManifest(configPath, workspace, "1.16.0"); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	assertManifestStale(t, configPath, workspace, "1.16.0", "Bareplane configuration")
}

func TestVerifyPlanManifestDetectsRerenderedTerraform(t *testing.T) {
	configPath, workspace := setupManifestContext(t)
	if _, err := CreatePlanManifest(configPath, workspace, "1.16.0"); err != nil {
		t.Fatal(err)
	}
	if err := replaceGeneratedForManifestTest(workspace.GeneratedDir, "{\"changed\":true}\n"); err != nil {
		t.Fatal(err)
	}
	assertManifestStale(t, configPath, workspace, "1.16.0", "generated Terraform")
}

func TestVerifyPlanManifestDetectsChangedLock(t *testing.T) {
	configPath, workspace := setupManifestContext(t)
	if _, err := CreatePlanManifest(configPath, workspace, "1.16.0"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspace.LockFile, []byte("changed-lock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertManifestStale(t, configPath, workspace, "1.16.0", "dependency lock")
}

func TestVerifyPlanManifestDetectsChangedSavedPlan(t *testing.T) {
	configPath, workspace := setupManifestContext(t)
	if _, err := CreatePlanManifest(configPath, workspace, "1.16.0"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspace.PlanFile, []byte("changed-plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertManifestStale(t, configPath, workspace, "1.16.0", "saved plan")
}

func TestVerifyPlanManifestDetectsTerraformVersionChange(t *testing.T) {
	configPath, workspace := setupManifestContext(t)
	if _, err := CreatePlanManifest(configPath, workspace, "1.16.0"); err != nil {
		t.Fatal(err)
	}
	assertManifestStale(t, configPath, workspace, "1.17.0", "Terraform version changed")
}

func TestVerifyPlanManifestRejectsMissingManifest(t *testing.T) {
	configPath, workspace := setupManifestContext(t)
	_, err := VerifyPlanManifest(configPath, workspace, "1.16.0")
	if err == nil || !strings.Contains(err.Error(), "manifest is missing") {
		t.Fatalf("expected missing manifest error, got %v", err)
	}
}

func TestVerifyPlanManifestRejectsSymlinkManifest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior depends on Windows privileges")
	}
	configPath, workspace := setupManifestContext(t)
	target := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, workspace.PlanManifestFile); err != nil {
		t.Fatal(err)
	}
	_, err := VerifyPlanManifest(configPath, workspace, "1.16.0")
	if err == nil || !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("expected symlink manifest rejection, got %v", err)
	}
	if err := RemovePlanManifest(workspace.PlanManifestFile); err == nil {
		t.Fatal("expected symlink manifest removal refusal")
	}
}

func TestVerifyPlanManifestRejectsSymlinkGeneratedEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior depends on Windows privileges")
	}
	configPath, workspace := setupManifestContext(t)
	if _, err := CreatePlanManifest(configPath, workspace, "1.16.0"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(workspace.GeneratedDir, "extra.tf.json")); err != nil {
		t.Fatal(err)
	}
	_, err := VerifyPlanManifest(configPath, workspace, "1.16.0")
	if err == nil || !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("expected generated symlink rejection, got %v", err)
	}
}

func setupManifestContext(t *testing.T) (string, project.TerraformWorkspace) {
	t.Helper()
	configPath, workspace := setupTerraformProject(t)
	if err := os.WriteFile(workspace.LockFile, []byte("lock-v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspace.PlanFile, []byte("saved-plan-v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, workspace
}

func replaceGeneratedForManifestTest(destination, content string) error {
	return project.ReplaceGeneratedDirectory(destination, "terraform", map[string][]byte{
		"main.tf.json": []byte(content),
	})
}

func assertManifestStale(t *testing.T, configPath string, workspace project.TerraformWorkspace, version, contains string) {
	t.Helper()
	_, err := VerifyPlanManifest(configPath, workspace, version)
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Fatalf("expected stale plan error containing %q, got %v", contains, err)
	}
}
