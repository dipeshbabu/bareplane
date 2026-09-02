package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/provider/builtin"
)

func TestProviderRuntimeProbeSkipsInvalidConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bareplane.yaml")
	if err := os.WriteFile(path, []byte("invalid: yaml: value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := builtin.Registry()
	if err != nil {
		t.Fatal(err)
	}

	called := false
	checks, err := Checks(Options{
		ConfigPath: path,
		Registry:   registry,
		LookPath: func(name string) (string, error) {
			return filepath.Join("/usr/bin", name), nil
		},
		ProviderProbe: func(context.Context, config.Config) Result {
			called = true
			return Result{Name: "runtime", Status: StatusPass, Message: "unexpected"}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	report := Run(context.Background(), checks)
	if called {
		t.Fatal("provider runtime probe ran for invalid configuration")
	}
	result := findResult(t, report, "provider runtime")
	if result.Status != StatusWarn {
		t.Fatalf("provider runtime status = %q", result.Status)
	}
}
