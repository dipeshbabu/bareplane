package terraformexec

import (
	"context"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/project"
)

func TestPlanRefusesConcurrentTerraformOperationBeforeRunner(t *testing.T) {
	configPath, _ := setupTerraformProject(t)
	lock, err := project.AcquireTerraformOperation(configPath, "render")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	called := false
	_, err = Plan(context.Background(), validOptions(configPath, runnerFunc(func(context.Context, Command) (int, error) {
		called = true
		return 0, nil
	})))
	if err == nil || !strings.Contains(err.Error(), "operation lock") {
		t.Fatalf("expected operation lock refusal, got %v", err)
	}
	if called {
		t.Fatal("Terraform runner invoked while another operation held the lock")
	}
}

func TestPlanReleasesOperationLockAfterError(t *testing.T) {
	configPath, _ := setupTerraformProject(t)
	runner := runnerFunc(func(_ context.Context, command Command) (int, error) {
		if command.Args[0] == "init" {
			return 1, nil
		}
		t.Fatalf("unexpected command after failed init: %q", command.Args[0])
		return 1, nil
	})

	if _, err := Plan(context.Background(), validOptions(configPath, runner)); err == nil {
		t.Fatal("expected plan error")
	}
	lock, err := project.AcquireTerraformOperation(configPath, "render")
	if err != nil {
		t.Fatalf("failed plan left operation lock behind: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}
