package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/doctor"
)

func TestRunDoctorIncludesProviderRuntimeProbe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bareplane.yaml")
	var initOut, initErr bytes.Buffer
	if code := Run([]string{"init", path}, &initOut, &initErr); code != 0 {
		t.Fatalf("init exit code = %d: %s", code, initErr.String())
	}

	lookPath := func(name string) (string, error) {
		return filepath.Join("/usr/bin", name), nil
	}
	probe := func(context.Context, config.Config) doctor.Result {
		return doctor.Result{
			Name:    "proxmox runtime",
			Status:  doctor.StatusPass,
			Message: "reachable; Proxmox test",
		}
	}

	var stdout, stderr bytes.Buffer
	code := runDoctor([]string{path}, &stdout, &stderr, lookPath, probe)
	if code != 0 {
		t.Fatalf("doctor exit code = %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "PASS  proxmox runtime") {
		t.Fatalf("provider runtime result missing from output: %q", stdout.String())
	}
}
