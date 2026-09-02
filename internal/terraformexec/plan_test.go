package terraformexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/project"
	"github.com/dipeshbabu/bareplane/internal/provider/proxmox"
)

type runnerFunc func(context.Context, Command) (int, error)

func (f runnerFunc) Run(ctx context.Context, command Command) (int, error) {
	return f(ctx, command)
}

func TestPlanExitTwoCreatesVerifiedManifest(t *testing.T) {
	configPath, workspace := setupTerraformProject(t)
	var commands []Command
	runner := runnerFunc(func(_ context.Context, command Command) (int, error) {
		commands = append(commands, command)
		switch command.Args[0] {
		case "init":
			if err := os.WriteFile(filepath.Join(command.Dir, terraformLockFilename), []byte("lock-v1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return 0, nil
		case "plan":
			if err := os.WriteFile(workspace.PlanFile, []byte("opaque-plan"), 0o644); err != nil {
				t.Fatal(err)
			}
			return 2, nil
		default:
			t.Fatalf("unexpected Terraform command %q", command.Args[0])
			return 1, nil
		}
	})

	options := validOptions(configPath, runner)
	options.TerraformBinary = "terraform-test"
	options.Credentials = proxmox.Credentials{TokenID: "user@pve!bareplane", TokenSecret: "top-secret"}
	options.BaseEnvironment = []string{"PATH=/bin", providerTokenEnv + "=stale", "TF_DATA_DIR=stale"}

	result, err := Plan(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changes {
		t.Fatal("expected detailed exit code 2 to report changes")
	}
	if len(commands) != 2 || commands[0].Args[0] != "init" || commands[1].Args[0] != "plan" {
		t.Fatalf("expected only init and plan, got %#v", commands)
	}
	for _, command := range commands {
		if command.Binary != "terraform-test" {
			t.Fatalf("unexpected binary %q", command.Binary)
		}
		joined := strings.Join(command.Args, " ")
		if strings.Contains(joined, "top-secret") || strings.Contains(joined, "user@pve!bareplane") {
			t.Fatalf("credentials leaked into arguments: %q", joined)
		}
		for _, forbidden := range []string{"apply", "destroy", "import"} {
			if slices.Contains(command.Args, forbidden) {
				t.Fatalf("mutation command present: %#v", command.Args)
			}
		}
		if got := envValue(command.Env, providerTokenEnv); got != "user@pve!bareplane=top-secret" {
			t.Fatalf("provider token env = %q", got)
		}
		if got := envValue(command.Env, "TF_DATA_DIR"); got != workspace.DataDir {
			t.Fatalf("TF_DATA_DIR = %q, want %q", got, workspace.DataDir)
		}
	}

	manifest, err := VerifyPlanManifest(configPath, workspace, "1.16.0")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ClusterName != "test" || manifest.TerraformVersion != "1.16.0" {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{workspace.PlanFile, workspace.PlanManifestFile, workspace.LockFile} {
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

func TestPlanRehydratesPersistentLockReadonly(t *testing.T) {
	configPath, workspace := setupTerraformProject(t)
	if err := os.WriteFile(workspace.LockFile, []byte("pinned-lock\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var initCommand Command
	runner := runnerFunc(func(_ context.Context, command Command) (int, error) {
		switch command.Args[0] {
		case "init":
			initCommand = command
			got, err := os.ReadFile(filepath.Join(command.Dir, terraformLockFilename))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "pinned-lock\n" {
				t.Fatalf("generated lock = %q", got)
			}
			return 0, nil
		case "plan":
			if err := os.WriteFile(workspace.PlanFile, []byte("no-change-plan"), 0o600); err != nil {
				t.Fatal(err)
			}
			return 0, nil
		default:
			t.Fatalf("unexpected command %q", command.Args[0])
			return 1, nil
		}
	})

	result, err := Plan(context.Background(), validOptions(configPath, runner))
	if err != nil {
		t.Fatal(err)
	}
	if result.Changes {
		t.Fatal("exit code 0 should report no changes")
	}
	if !slices.Contains(initCommand.Args, "-lockfile=readonly") {
		t.Fatalf("existing lock did not make init readonly: %#v", initCommand.Args)
	}
}

func TestPlanInvalidatesPreviousManifestBeforeFailedReplan(t *testing.T) {
	configPath, workspace := setupTerraformProject(t)
	if err := os.WriteFile(workspace.PlanManifestFile, []byte("old-manifest"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := runnerFunc(func(_ context.Context, command Command) (int, error) {
		if command.Args[0] == "init" {
			return 1, nil
		}
		t.Fatalf("unexpected command after failed init: %q", command.Args[0])
		return 1, nil
	})

	if _, err := Plan(context.Background(), validOptions(configPath, runner)); err == nil {
		t.Fatal("expected failed replan")
	}
	if _, err := os.Stat(workspace.PlanManifestFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale manifest survived failed replan: %v", err)
	}
}

func TestPlanRejectsSymlinkedPersistentLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior depends on Windows privileges")
	}
	configPath, workspace := setupTerraformProject(t)
	target := filepath.Join(t.TempDir(), "lock")
	if err := os.WriteFile(target, []byte("lock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, workspace.LockFile); err != nil {
		t.Fatal(err)
	}

	called := false
	_, err := Plan(context.Background(), validOptions(configPath, runnerFunc(func(context.Context, Command) (int, error) {
		called = true
		return 0, nil
	})))
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("expected symlink lock rejection, got %v", err)
	}
	if called {
		t.Fatal("Terraform init/plan runner called with symlinked dependency lock")
	}
}

func TestPlanRejectsMissingManagedRender(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")
	writeProvisioningConfig(t, configPath)
	workspace, err := project.EnsureTerraformWorkspace(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace.GeneratedDir, 0o755); err != nil {
		t.Fatal(err)
	}

	called := false
	_, err = Plan(context.Background(), validOptions(configPath, runnerFunc(func(context.Context, Command) (int, error) {
		called = true
		return 0, nil
	})))
	if err == nil || !strings.Contains(err.Error(), "generated Terraform is not ready") {
		t.Fatalf("expected managed-render error, got %v", err)
	}
	if called {
		t.Fatal("Terraform runner called for unmanaged generated directory")
	}
}

func TestPlanFailuresDoNotCreateManifest(t *testing.T) {
	tests := []struct {
		name   string
		runner Runner
		want   string
	}{
		{
			name: "init exit",
			runner: runnerFunc(func(_ context.Context, command Command) (int, error) {
				if command.Args[0] != "init" {
					t.Fatalf("unexpected command %q", command.Args[0])
				}
				return 1, nil
			}),
			want: "terraform init failed",
		},
		{
			name: "plan exit",
			runner: runnerFunc(func(_ context.Context, command Command) (int, error) {
				if command.Args[0] == "init" {
					if err := os.WriteFile(filepath.Join(command.Dir, terraformLockFilename), []byte("lock\n"), 0o644); err != nil {
						t.Fatal(err)
					}
					return 0, nil
				}
				return 1, nil
			}),
			want: "terraform plan exited with code 1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath, workspace := setupTerraformProject(t)
			_, err := Plan(context.Background(), validOptions(configPath, test.runner))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
			if _, statErr := os.Stat(workspace.PlanManifestFile); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed plan left a manifest: %v", statErr)
			}
		})
	}
}

func TestPlanRequiresSavedPlanArtifact(t *testing.T) {
	configPath, _ := setupTerraformProject(t)
	runner := runnerFunc(func(_ context.Context, command Command) (int, error) {
		if command.Args[0] == "init" {
			if err := os.WriteFile(filepath.Join(command.Dir, terraformLockFilename), []byte("lock\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return 0, nil
		}
		return 0, nil
	})
	_, err := Plan(context.Background(), validOptions(configPath, runner))
	if err == nil || !strings.Contains(err.Error(), "did not create the plan artifact") {
		t.Fatalf("expected missing plan artifact error, got %v", err)
	}
}

func TestPlanPropagatesRunnerStartError(t *testing.T) {
	configPath, _ := setupTerraformProject(t)
	boom := errors.New("binary unavailable")
	_, err := Plan(context.Background(), validOptions(configPath, runnerFunc(func(context.Context, Command) (int, error) {
		return -1, boom
	})))
	if !errors.Is(err, boom) {
		t.Fatalf("expected runner error, got %v", err)
	}
}

func TestPlanRejectsMissingCredentialsBeforeExecution(t *testing.T) {
	configPath, _ := setupTerraformProject(t)
	called := false
	_, err := Plan(context.Background(), PlanOptions{
		ConfigPath: configPath,
		Runner: runnerFunc(func(context.Context, Command) (int, error) {
			called = true
			return 0, nil
		}),
	})
	if err == nil {
		t.Fatal("expected credential validation error")
	}
	if called {
		t.Fatal("runner called without credentials")
	}
}

func TestTerraformEnvironmentReplacesSensitiveOverrides(t *testing.T) {
	environment := terraformEnvironment(
		[]string{"PATH=/bin", providerTokenEnv + "=old", "TF_DATA_DIR=old"},
		"/private/data",
		proxmox.Credentials{TokenID: "id", TokenSecret: "secret"},
	)
	if got := envValue(environment, providerTokenEnv); got != "id=secret" {
		t.Fatalf("token env = %q", got)
	}
	if got := envValue(environment, "TF_DATA_DIR"); got != "/private/data" {
		t.Fatalf("data env = %q", got)
	}
	if got := envValue(environment, "PATH"); got != "/bin" {
		t.Fatalf("PATH = %q", got)
	}
}

func setupTerraformProject(t *testing.T) (string, project.TerraformWorkspace) {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(root, "bareplane.yaml")
	writeProvisioningConfig(t, configPath)
	workspace, err := project.EnsureTerraformWorkspace(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.ReplaceGeneratedDirectory(workspace.GeneratedDir, "terraform", map[string][]byte{
		"main.tf.json": []byte("{}\n"),
	}); err != nil {
		t.Fatal(err)
	}
	return configPath, workspace
}

func writeProvisioningConfig(t *testing.T, path string) {
	t.Helper()
	input := `apiVersion: bareplane.io/v1alpha1
kind: BareplaneCluster
metadata:
  name: test
spec:
  domain: test.example.com
  provider:
    type: proxmox
    endpoint: https://proxmox.example.com:8006
    targets:
      - pve1
    proxmox:
      bridge: vmbr0
      systemDatastore: local-lvm
      cloudImageFileID: local:import/debian.qcow2
      ssh:
        user: debian
        publicKeyFile: id_ed25519.pub
  nodes:
    - name: control
      role: control-plane
      count: 1
      cpu: 4
      memoryGB: 8
      diskGB: 64
  features:
    observability: true
    gpu: false
  profiles:
    - minimal
  dns:
    provider: manual
  secrets:
    provider: sops
`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
}

func validOptions(configPath string, runner Runner) PlanOptions {
	return PlanOptions{
		ConfigPath: configPath,
		Credentials: proxmox.Credentials{
			TokenID:     "user@pve!token",
			TokenSecret: "secret",
		},
		BaseEnvironment: []string{"PATH=/bin"},
		Runner:          withTerraformVersion(runner),
		Stdout:          &bytes.Buffer{},
		Stderr:          &bytes.Buffer{},
	}
}

func withTerraformVersion(runner Runner) Runner {
	return runnerFunc(func(ctx context.Context, command Command) (int, error) {
		if len(command.Args) > 0 && command.Args[0] == "version" {
			if got := envValue(command.Env, providerTokenEnv); got != "" {
				return 1, fmt.Errorf("provider token leaked into terraform version environment")
			}
			if command.Stdout == nil {
				return 1, errors.New("terraform version stdout is nil")
			}
			_, err := fmt.Fprint(command.Stdout, "{\"terraform_version\":\"1.16.0\",\"platform\":\"test\",\"provider_selections\":{},\"terraform_outdated\":false}")
			return 0, err
		}
		return runner.Run(ctx, command)
	})
}

func envValue(environment []string, key string) string {
	prefix := key + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}