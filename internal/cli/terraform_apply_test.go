package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/terraformexec"
)

func TestRunTerraformApplyRequiresExactApprovalSyntax(t *testing.T) {
	tests := [][]string{
		{"apply"},
		{"apply", "yes"},
		{"apply", "--approve"},
		{"apply", "--approve", ""},
		{"apply", "--approve", "cluster", "config.yaml", "extra"},
		{"apply", "--auto-approve", "cluster"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			called := false
			code := runTerraformWith(
				args,
				&stdout,
				&stderr,
				terraformCredentialLookup("id", "secret"),
				func(string) (string, error) { return "/bin/terraform", nil },
				nil,
				func(context.Context, terraformexec.ApplyOptions) (terraformexec.ApplyResult, error) {
					called = true
					return terraformexec.ApplyResult{}, nil
				},
			)
			if code != 2 {
				t.Fatalf("expected usage exit code 2, got %d", code)
			}
			if called {
				t.Fatal("apply invoked for invalid CLI syntax")
			}
			if !strings.Contains(stderr.String(), "--approve <cluster-name>") {
				t.Fatalf("unexpected stderr %q", stderr.String())
			}
		})
	}
}

func TestRunTerraformApplyPassesOnlyStructuredOptions(t *testing.T) {
	lookup := terraformCredentialLookup("user@pve!bareplane", "secret")
	lookPath := func(name string) (string, error) {
		if name != "terraform" {
			t.Fatalf("unexpected executable lookup %q", name)
		}
		return "/usr/local/bin/terraform", nil
	}
	var got terraformexec.ApplyOptions
	applier := func(_ context.Context, options terraformexec.ApplyOptions) (terraformexec.ApplyResult, error) {
		got = options
		return terraformexec.ApplyResult{Cluster: "prod"}, nil
	}

	var stdout, stderr bytes.Buffer
	code := runTerraformWith(
		[]string{"apply", "--approve", "prod", "custom.yaml"},
		&stdout,
		&stderr,
		lookup,
		lookPath,
		nil,
		applier,
	)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}
	if got.ConfigPath != "custom.yaml" || got.Approval != "prod" || got.TerraformBinary != "/usr/local/bin/terraform" {
		t.Fatalf("unexpected apply options %#v", got)
	}
	if got.Credentials.TokenID != "user@pve!bareplane" || got.Credentials.TokenSecret != "secret" {
		t.Fatalf("unexpected credentials %#v", got.Credentials)
	}
	if !strings.Contains(stdout.String(), `cluster "prod"`) || !strings.Contains(stdout.String(), "new plan") {
		t.Fatalf("unexpected stdout %q", stdout.String())
	}
}

func TestRunTerraformApplyStopsOnMissingCredentials(t *testing.T) {
	lookedUp := false
	applied := false
	var stdout, stderr bytes.Buffer
	code := runTerraformWith(
		[]string{"apply", "--approve", "prod"},
		&stdout,
		&stderr,
		func(string) (string, bool) { return "", false },
		func(string) (string, error) {
			lookedUp = true
			return "/bin/terraform", nil
		},
		nil,
		func(context.Context, terraformexec.ApplyOptions) (terraformexec.ApplyResult, error) {
			applied = true
			return terraformexec.ApplyResult{}, nil
		},
	)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if lookedUp || applied {
		t.Fatalf("dependencies used after missing credentials: lookup=%v apply=%v", lookedUp, applied)
	}
}

func TestRunTerraformApplyStopsWhenTerraformMissing(t *testing.T) {
	applied := false
	var stdout, stderr bytes.Buffer
	code := runTerraformWith(
		[]string{"apply", "--approve", "prod"},
		&stdout,
		&stderr,
		terraformCredentialLookup("id", "secret"),
		func(string) (string, error) { return "", errors.New("not found") },
		nil,
		func(context.Context, terraformexec.ApplyOptions) (terraformexec.ApplyResult, error) {
			applied = true
			return terraformexec.ApplyResult{}, nil
		},
	)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if applied {
		t.Fatal("apply invoked without Terraform executable")
	}
	if !strings.Contains(stderr.String(), "terraform executable not found") {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}
}

func TestTerraformHelpIncludesAttestedApplySyntax(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runTerraformWith(nil, &stdout, &stderr, nil, nil, nil)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "terraform apply --approve <cluster-name>") {
		t.Fatalf("apply syntax missing from help: %q", stdout.String())
	}
}
