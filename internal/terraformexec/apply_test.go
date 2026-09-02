package terraformexec

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/project"
	"github.com/dipeshbabu/bareplane/internal/provider/proxmox"
)

func TestApplyUsesOnlyAttestedSavedPlan(t *testing.T) {
	configPath, workspace := setupAttestedApply(t, "1.16.0")
	var commands []Command
	runner := runnerFunc(func(_ context.Context, command Command) (int, error) {
		commands = append(commands, command)
		switch command.Args[0] {
		case "init":
			return 0, nil
		case "apply":
			if _, err := os.Stat(workspace.PlanManifestFile); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("plan manifest still existed when apply began: %v", err)
			}
			if err := os.WriteFile(workspace.StateFile, []byte("state"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(workspace.StateBackupFile, []byte("backup"), 0o644); err != nil {
				t.Fatal(err)
			}
			return 0, nil
		default:
			t.Fatalf("unexpected Terraform command %q", command.Args[0])
			return 1, nil
		}
	})

	options := validApplyOptions(configPath, runner)
	options.BaseEnvironment = []string{
		"PATH=/bin",
		"TF_CLI_ARGS=-chdir=/tmp",
		"TF_CLI_ARGS_apply=-destroy",
		"TF_WORKSPACE=other",
		"TF_VAR_injected=value",
		"PROXMOX_VE_INSECURE=true",
		"PROXMOX_VE_ENDPOINT=http://example.invalid",
		providerTokenEnv + "=stale",
	}
	result, err := Apply(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Cluster != "test" {
		t.Fatalf("applied cluster = %q", result.Cluster)
	}
	if len(commands) != 2 || commands[0].Args[0] != "init" || commands[1].Args[0] != "apply" {
		t.Fatalf("expected init and apply, got %#v", commands)
	}

	applyCommand := commands[1]
	if got, want := applyCommand.Args[len(applyCommand.Args)-1], workspace.PlanFile; got != want {
		t.Fatalf("apply plan path = %q, want %q", got, want)
	}
	for _, required := range []string{
		"-input=false",
		"-no-color",
		"-auto-approve",
		"-state=" + workspace.StateFile,
		"-state-out=" + workspace.StateFile,
		"-backup=" + workspace.StateBackupFile,
	} {
		if !containsString(applyCommand.Args, required) {
			t.Fatalf("apply arguments missing %q: %#v", required, applyCommand.Args)
		}
	}
	joined := strings.Join(applyCommand.Args, " ")
	if strings.Contains(joined, options.Credentials.TokenID) || strings.Contains(joined, options.Credentials.TokenSecret) {
		t.Fatalf("credentials leaked into apply arguments %q", joined)
	}
	for _, command := range commands {
		assertControlledTerraformEnvironment(t, command.Env, workspace.DataDir, options.Credentials)
	}

	if _, err := os.Stat(workspace.PlanFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful apply left saved plan: %v", err)
	}
	if _, err := os.Stat(workspace.PlanManifestFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful apply left plan manifest: %v", err)
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{workspace.StateFile, workspace.StateBackupFile} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Fatalf("%s permissions = %o, want 600", path, got)
			}
		}
	}
}

func TestApplyRejectsWrongApprovalBeforeTerraform(t *testing.T) {
	configPath, _ := setupAttestedApply(t, "1.16.0")
	called := false
	options := validApplyOptions(configPath, runnerFunc(func(context.Context, Command) (int, error) {
		called = true
		return 0, nil
	}))
	options.Approval = "wrong-cluster"

	_, err := Apply(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "approval must exactly match") {
		t.Fatalf("expected approval error, got %v", err)
	}
	if called {
		t.Fatal("Terraform was invoked with wrong approval")
	}
}

func TestApplyRejectsMissingCredentialsBeforeTerraform(t *testing.T) {
	configPath, _ := setupAttestedApply(t, "1.16.0")
	called := false
	options := validApplyOptions(configPath, runnerFunc(func(context.Context, Command) (int, error) {
		called = true
		return 0, nil
	}))
	options.Credentials = proxmox.Credentials{}

	_, err := Apply(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "credentials are required") {
		t.Fatalf("expected credential error, got %v", err)
	}
	if called {
		t.Fatal("Terraform was invoked without credentials")
	}
}

func TestApplyRejectsStalePlanBeforeInitOrApply(t *testing.T) {
	configPath, _ := setupAttestedApply(t, "1.16.0")
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

	called := false
	options := validApplyOptions(configPath, runnerFunc(func(context.Context, Command) (int, error) {
		called = true
		return 0, nil
	}))
	_, err = Apply(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "plan is stale") {
		t.Fatalf("expected stale plan error, got %v", err)
	}
	if called {
		t.Fatal("init/apply reached for stale plan")
	}
}

func TestApplyRejectsTerraformVersionDriftBeforeInitOrApply(t *testing.T) {
	configPath, _ := setupAttestedApply(t, "1.15.0")
	called := false
	options := validApplyOptions(configPath, runnerFunc(func(context.Context, Command) (int, error) {
		called = true
		return 0, nil
	}))
	_, err := Apply(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "Terraform version changed") {
		t.Fatalf("expected version drift error, got %v", err)
	}
	if called {
		t.Fatal("init/apply reached for Terraform version drift")
	}
}

func TestApplyInitFailurePreservesValidManifest(t *testing.T) {
	configPath, workspace := setupAttestedApply(t, "1.16.0")
	options := validApplyOptions(configPath, runnerFunc(func(_ context.Context, command Command) (int, error) {
		if command.Args[0] != "init" {
			t.Fatalf("unexpected command %q", command.Args[0])
		}
		return 1, nil
	}))
	_, err := Apply(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "terraform init failed") {
		t.Fatalf("expected init error, got %v", err)
	}
	if _, err := os.Stat(workspace.PlanManifestFile); err != nil {
		t.Fatalf("init failure invalidated an unapplied plan: %v", err)
	}
}

func TestApplyFailureInvalidatesManifestAndPreservesPlan(t *testing.T) {
	configPath, workspace := setupAttestedApply(t, "1.16.0")
	options := validApplyOptions(configPath, runnerFunc(func(_ context.Context, command Command) (int, error) {
		switch command.Args[0] {
		case "init":
			return 0, nil
		case "apply":
			if _, err := os.Stat(workspace.PlanManifestFile); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("manifest exists at failed apply start: %v", err)
			}
			if err := os.WriteFile(workspace.StateFile, []byte("partial-state"), 0o644); err != nil {
				t.Fatal(err)
			}
			return 1, nil
		default:
			t.Fatalf("unexpected command %q", command.Args[0])
			return 1, nil
		}
	}))

	_, err := Apply(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "new plan is required") {
		t.Fatalf("expected failed apply invalidation error, got %v", err)
	}
	if _, err := os.Stat(workspace.PlanManifestFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed apply left valid manifest: %v", err)
	}
	if _, err := os.Stat(workspace.PlanFile); err != nil {
		t.Fatalf("failed apply removed diagnostic plan: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(workspace.StateFile)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("partial state permissions = %o, want 600", got)
		}
	}
}

func TestApplyRunnerErrorIsRedactedAndInvalidatesManifest(t *testing.T) {
	configPath, workspace := setupAttestedApply(t, "1.16.0")
	secret := "never-print-this"
	options := validApplyOptions(configPath, runnerFunc(func(_ context.Context, command Command) (int, error) {
		if command.Args[0] == "init" {
			return 0, nil
		}
		return -1, errors.New("failed with " + secret + " for user@pve!token")
	}))
	options.Credentials.TokenSecret = secret

	_, err := Apply(context.Background(), options)
	if err == nil {
		t.Fatal("expected apply runner error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), options.Credentials.TokenID) {
		t.Fatalf("credential leaked through error: %v", err)
	}
	if _, err := os.Stat(workspace.PlanManifestFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runner failure left valid manifest: %v", err)
	}
}

func TestApplyRejectsSymlinkedPlanBeforeTerraform(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior depends on Windows privileges")
	}
	configPath, workspace := setupAttestedApply(t, "1.16.0")
	if err := os.Remove(workspace.PlanFile); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "plan")
	if err := os.WriteFile(target, []byte("plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, workspace.PlanFile); err != nil {
		t.Fatal(err)
	}

	called := false
	options := validApplyOptions(configPath, runnerFunc(func(context.Context, Command) (int, error) {
		called = true
		return 0, nil
	}))
	_, err := Apply(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("expected symlink plan rejection, got %v", err)
	}
	if called {
		t.Fatal("Terraform invoked with symlinked plan")
	}
}

func TestControlledTerraformEnvironmentBlocksExecutionOverrides(t *testing.T) {
	credentials := proxmox.Credentials{TokenID: "id", TokenSecret: "secret"}
	environment := terraformEnvironment([]string{
		"PATH=/bin",
		"tf_cli_args_apply=-destroy",
		"TF_CLI_ARGS=-input=true",
		"TF_WORKSPACE=evil",
		"TF_VAR_x=y",
		"PROXMOX_VE_INSECURE=true",
		"PROXMOX_VE_API_TOKEN=stale",
		"PROXMOX_VE_ENDPOINT=http://example.invalid",
		"TF_DATA_DIR=stale",
	}, "/private/data", credentials)
	assertControlledTerraformEnvironment(t, environment, "/private/data", credentials)
}

func setupAttestedApply(t *testing.T, terraformVersion string) (string, project.TerraformWorkspace) {
	t.Helper()
	configPath, workspace := setupTerraformProject(t)
	if err := os.WriteFile(workspace.LockFile, []byte("lock-v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspace.PlanFile, []byte("saved-plan-v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreatePlanManifest(configPath, workspace, terraformVersion); err != nil {
		t.Fatal(err)
	}
	return configPath, workspace
}

func validApplyOptions(configPath string, runner Runner) ApplyOptions {
	return ApplyOptions{
		ConfigPath:      configPath,
		Approval:        "test",
		TerraformBinary: "terraform-test",
		Credentials: proxmox.Credentials{
			TokenID:     "user@pve!token",
			TokenSecret: "secret",
		},
		BaseEnvironment: []string{"PATH=/bin"},
		Stdout:          &bytes.Buffer{},
		Stderr:          &bytes.Buffer{},
		Runner:          withTerraformVersion(runner),
	}
}

func assertControlledTerraformEnvironment(t *testing.T, environment []string, dataDir string, credentials proxmox.Credentials) {
	t.Helper()
	if got := envValue(environment, "TF_DATA_DIR"); got != dataDir {
		t.Fatalf("TF_DATA_DIR = %q, want %q", got, dataDir)
	}
	if got := envValue(environment, providerTokenEnv); got != credentials.TokenID+"="+credentials.TokenSecret {
		t.Fatalf("provider token env = %q", got)
	}
	for _, entry := range environment {
		key := environmentKey(entry)
		upper := strings.ToUpper(key)
		if upper == "TF_WORKSPACE" || upper == "TF_CLI_ARGS" || strings.HasPrefix(upper, "TF_CLI_ARGS_") || strings.HasPrefix(upper, "TF_VAR_") {
			t.Fatalf("blocked Terraform environment variable survived: %q", key)
		}
		if strings.HasPrefix(upper, "PROXMOX_VE_") && upper != providerTokenEnv {
			t.Fatalf("uncontrolled Proxmox environment variable survived: %q", key)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
