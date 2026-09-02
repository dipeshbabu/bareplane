package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/provider/proxmox"
	"github.com/dipeshbabu/bareplane/internal/terraformexec"
)

func TestRunTerraformShowsHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runTerraformWith(nil, &stdout, &stderr, nil, nil, nil)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "bareplane terraform plan") {
		t.Fatalf("unexpected help output %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}
}

func TestRunTerraformRejectsUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runTerraformWith([]string{"apply"}, &stdout, &stderr, nil, nil, nil)
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), `unknown terraform command "apply"`) {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}
}

func TestRunTerraformPlanRejectsExtraArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runTerraformWith([]string{"plan", "a", "b"}, &stdout, &stderr, nil, nil, nil)
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "usage: bareplane terraform plan [path]") {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}
}

func TestRunTerraformPlanStopsOnMissingCredentials(t *testing.T) {
	lookedUp := false
	planned := false
	lookup := func(key string) (string, bool) { return "", false }
	lookPath := func(string) (string, error) {
		lookedUp = true
		return "/bin/terraform", nil
	}
	planner := func(context.Context, terraformexec.PlanOptions) (terraformexec.PlanResult, error) {
		planned = true
		return terraformexec.PlanResult{}, nil
	}

	var stdout, stderr bytes.Buffer
	code := runTerraformWith([]string{"plan"}, &stdout, &stderr, lookup, lookPath, planner)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if lookedUp || planned {
		t.Fatalf("dependencies used after missing credentials: lookPath=%v planned=%v", lookedUp, planned)
	}
	if !strings.Contains(stderr.String(), proxmox.EnvTokenID) {
		t.Fatalf("expected credential guidance, got %q", stderr.String())
	}
}

func TestRunTerraformPlanStopsWhenTerraformMissing(t *testing.T) {
	lookup := terraformCredentialLookup("user@pve!bareplane", "secret")
	planned := false
	lookPath := func(string) (string, error) { return "", errors.New("not found") }
	planner := func(context.Context, terraformexec.PlanOptions) (terraformexec.PlanResult, error) {
		planned = true
		return terraformexec.PlanResult{}, nil
	}

	var stdout, stderr bytes.Buffer
	code := runTerraformWith([]string{"plan"}, &stdout, &stderr, lookup, lookPath, planner)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if planned {
		t.Fatal("planner called without Terraform executable")
	}
	if !strings.Contains(stderr.String(), "terraform executable not found") {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}
}

func TestRunTerraformPlanPassesDependenciesAndReportsChanges(t *testing.T) {
	lookup := terraformCredentialLookup("user@pve!bareplane", "secret")
	lookPath := func(name string) (string, error) {
		if name != "terraform" {
			t.Fatalf("unexpected executable lookup %q", name)
		}
		return "/usr/local/bin/terraform", nil
	}
	var got terraformexec.PlanOptions
	planner := func(_ context.Context, options terraformexec.PlanOptions) (terraformexec.PlanResult, error) {
		got = options
		return terraformexec.PlanResult{Changes: true}, nil
	}

	var stdout, stderr bytes.Buffer
	code := runTerraformWith([]string{"plan", "custom.yaml"}, &stdout, &stderr, lookup, lookPath, planner)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}
	if got.ConfigPath != "custom.yaml" || got.TerraformBinary != "/usr/local/bin/terraform" {
		t.Fatalf("unexpected plan options %#v", got)
	}
	if got.Credentials.TokenID != "user@pve!bareplane" || got.Credentials.TokenSecret != "secret" {
		t.Fatalf("unexpected credentials in plan options %#v", got.Credentials)
	}
	if !strings.Contains(stdout.String(), "changes present") {
		t.Fatalf("unexpected stdout %q", stdout.String())
	}
}

func TestRunTerraformPlanReportsNoChanges(t *testing.T) {
	planner := func(context.Context, terraformexec.PlanOptions) (terraformexec.PlanResult, error) {
		return terraformexec.PlanResult{Changes: false}, nil
	}
	var stdout, stderr bytes.Buffer
	code := runTerraformWith(
		[]string{"plan"},
		&stdout,
		&stderr,
		terraformCredentialLookup("id", "secret"),
		func(string) (string, error) { return "/bin/terraform", nil },
		planner,
	)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "no changes") {
		t.Fatalf("unexpected stdout %q", stdout.String())
	}
}

func TestRunTerraformPlanReturnsPlannerErrorWithoutCredentialLeak(t *testing.T) {
	secret := "never-print-this"
	planner := func(context.Context, terraformexec.PlanOptions) (terraformexec.PlanResult, error) {
		return terraformexec.PlanResult{}, errors.New("provider plan failed")
	}
	var stdout, stderr bytes.Buffer
	code := runTerraformWith(
		[]string{"plan"},
		&stdout,
		&stderr,
		terraformCredentialLookup("id", secret),
		func(string) (string, error) { return "/bin/terraform", nil },
		planner,
	)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "provider plan failed") {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}
	if strings.Contains(stderr.String(), secret) {
		t.Fatalf("secret leaked into stderr %q", stderr.String())
	}
}

func terraformCredentialLookup(id, secret string) proxmox.LookupEnvFunc {
	return func(key string) (string, bool) {
		switch key {
		case proxmox.EnvTokenID:
			return id, true
		case proxmox.EnvTokenSecret:
			return secret, true
		default:
			return "", false
		}
	}
}
