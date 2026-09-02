package terraformexec

import (
	"context"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/project"
)

func TestApplyRefusesConcurrentTerraformOperationBeforeRunner(t *testing.T) {
	configPath, _ := setupAttestedApply(t, "1.16.0")
	lock, err := project.AcquireTerraformOperation(configPath, "render")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	called := false
	options := validApplyOptions(configPath, runnerFunc(func(context.Context, Command) (int, error) {
		called = true
		return 0, nil
	}))
	_, err = Apply(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "operation lock") {
		t.Fatalf("expected operation lock refusal, got %v", err)
	}
	if called {
		t.Fatal("Terraform runner invoked while another project operation held the lock")
	}
}

func TestApplyReleasesOperationLockAfterPreflightError(t *testing.T) {
	configPath, workspace := setupAttestedApply(t, "1.16.0")
	if err := project.ReplaceGeneratedDirectory(workspace.GeneratedDir, "terraform", map[string][]byte{
		"main.tf.json": []byte("{\"changed\":true}\n"),
	}); err != nil {
		t.Fatal(err)
	}

	options := validApplyOptions(configPath, runnerFunc(func(context.Context, Command) (int, error) {
		t.Fatal("Terraform runner should not be reached for a stale plan")
		return 1, nil
	}))
	if _, err := Apply(context.Background(), options); err == nil {
		t.Fatal("expected stale-plan apply failure")
	}

	lock, err := project.AcquireTerraformOperation(configPath, "render")
	if err != nil {
		t.Fatalf("failed apply preflight left the operation lock behind: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}
