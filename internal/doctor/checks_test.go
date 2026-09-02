package doctor

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/provider/builtin"
)

func TestChecksReportPassWarningAndFailure(t *testing.T) {
	path := writeConfig(t, config.Default())
	registry, err := builtin.Registry()
	if err != nil {
		t.Fatal(err)
	}

	lookPath := func(name string) (string, error) {
		if name == "helm" {
			return "", fmt.Errorf("not found")
		}
		return filepath.Join("/usr/bin", name), nil
	}

	checks, err := Checks(Options{ConfigPath: path, LookPath: lookPath, Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	report := Run(t.Context(), checks)

	if report.HasFailures() {
		t.Fatalf("unexpected failure report: %#v", report.Results)
	}
	if got := report.Results[len(report.Results)-1].Status; got != StatusWarn {
		t.Fatalf("expected helm warning, got %q", got)
	}
}

func TestChecksFailWhenRequiredExecutableIsMissing(t *testing.T) {
	path := writeConfig(t, config.Default())
	registry, err := builtin.Registry()
	if err != nil {
		t.Fatal(err)
	}

	lookPath := func(name string) (string, error) {
		if name == "terraform" {
			return "", fmt.Errorf("not found")
		}
		return filepath.Join("/usr/bin", name), nil
	}

	checks, err := Checks(Options{ConfigPath: path, LookPath: lookPath, Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	report := Run(t.Context(), checks)
	if !report.HasFailures() {
		t.Fatal("expected missing required executable to fail")
	}
	if result := findResult(t, report, "terraform"); result.Status != StatusFail {
		t.Fatalf("terraform status = %q", result.Status)
	}
}

func TestChecksSkipProviderWhenConfigurationIsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bareplane.yaml")
	if err := os.WriteFile(path, []byte("not: valid: yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := builtin.Registry()
	if err != nil {
		t.Fatal(err)
	}

	checks, err := Checks(Options{
		ConfigPath: path,
		Registry:   registry,
		LookPath: func(name string) (string, error) {
			return filepath.Join("/usr/bin", name), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	report := Run(t.Context(), checks)

	if result := findResult(t, report, "configuration"); result.Status != StatusFail {
		t.Fatalf("configuration status = %q", result.Status)
	}
	if result := findResult(t, report, "provider"); result.Status != StatusWarn {
		t.Fatalf("provider status = %q", result.Status)
	}
}

func TestChecksUseProviderValidation(t *testing.T) {
	cfg := config.Default()
	cfg.Spec.Provider.Endpoint = "proxmox.example.com:8006"
	path := writeConfig(t, cfg)
	registry, err := builtin.Registry()
	if err != nil {
		t.Fatal(err)
	}

	checks, err := Checks(Options{
		ConfigPath: path,
		Registry:   registry,
		LookPath: func(name string) (string, error) {
			return filepath.Join("/usr/bin", name), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	report := Run(t.Context(), checks)

	if result := findResult(t, report, "configuration"); result.Status != StatusPass {
		t.Fatalf("configuration should pass, got %q", result.Status)
	}
	if result := findResult(t, report, "provider"); result.Status != StatusFail {
		t.Fatalf("provider should fail, got %q", result.Status)
	}
}

func writeConfig(t *testing.T, cfg config.Config) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bareplane.yaml")
	var buf bytes.Buffer
	if err := config.Encode(&buf, cfg); err != nil {
		t.Fatalf("encode config: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func findResult(t *testing.T, report Report, name string) Result {
	t.Helper()
	for _, result := range report.Results {
		if result.Name == name {
			return result
		}
	}
	t.Fatalf("result %q not found in %#v", name, report.Results)
	return Result{}
}
