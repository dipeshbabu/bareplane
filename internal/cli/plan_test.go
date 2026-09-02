package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/provider/proxmox"
	"github.com/dipeshbabu/bareplane/internal/runtime"
)

func TestRunPlanCreatesMissingMachineWithoutMutation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("plan attempted mutating method %s", r.Method)
		}
		_, _ = fmt.Fprint(w, `{"data":[]}`)
	}))
	defer server.Close()

	path := writePlanConfig(t, server.URL)
	var stdout, stderr bytes.Buffer
	code := runPlan([]string{path}, &stdout, &stderr, planDeps(server))
	if code != 0 {
		t.Fatalf("plan exit code = %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "CREATE") || !strings.Contains(stdout.String(), "bareplane-control-plane-1") {
		t.Fatalf("unexpected plan output %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Summary: 1 create, 0 update, 0 noop, 0 conflict") {
		t.Fatalf("unexpected summary %q", stdout.String())
	}
}

func TestRunPlanNoopsExplicitlyOwnedMachine(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":[{"vmid":101,"type":"qemu","name":"bareplane-control-plane-1","node":"pve1","status":"running","maxcpu":4,"maxmem":8589934592,"maxdisk":68719476736,"tags":"bareplane;bareplane-cluster-bareplane"}]}`)
	}))
	defer server.Close()

	path := writePlanConfig(t, server.URL)
	var stdout, stderr bytes.Buffer
	code := runPlan([]string{path}, &stdout, &stderr, planDeps(server))
	if code != 0 {
		t.Fatalf("plan exit code = %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "NOOP") || !strings.Contains(stdout.String(), "qemu/101") {
		t.Fatalf("unexpected plan output %q", stdout.String())
	}
}

func TestRunPlanConflictsWithUnmanagedNameCollision(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":[{"vmid":101,"type":"qemu","name":"bareplane-control-plane-1","node":"pve1","status":"running","maxcpu":4,"maxmem":8589934592,"maxdisk":68719476736}]}`)
	}))
	defer server.Close()

	path := writePlanConfig(t, server.URL)
	var stdout, stderr bytes.Buffer
	code := runPlan([]string{path}, &stdout, &stderr, planDeps(server))
	if code != planConflictExitCode {
		t.Fatalf("plan exit code = %d, want %d: %s", code, planConflictExitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "CONFLICT") || !strings.Contains(stdout.String(), "not explicitly owned") {
		t.Fatalf("unexpected conflict output %q", stdout.String())
	}
}

func TestRunPlanReportsMissingCredentials(t *testing.T) {
	path := writePlanConfig(t, "https://proxmox.example.com:8006")
	var stdout, stderr bytes.Buffer
	code := runPlan([]string{path}, &stdout, &stderr, runtime.ProviderDependencies{
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if code != 1 {
		t.Fatalf("plan exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), proxmox.EnvTokenID) {
		t.Fatalf("expected missing credential name, got %q", stderr.String())
	}
}

func writePlanConfig(t *testing.T, endpoint string) string {
	t.Helper()
	cfg := config.Default()
	cfg.Spec.Provider.Endpoint = endpoint
	var buf bytes.Buffer
	if err := config.Encode(&buf, cfg); err != nil {
		t.Fatalf("config.Encode() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "bareplane.yaml")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func planDeps(server *httptest.Server) runtime.ProviderDependencies {
	return runtime.ProviderDependencies{
		LookupEnv: func(key string) (string, bool) {
			switch key {
			case proxmox.EnvTokenID:
				return "root@pam!bareplane", true
			case proxmox.EnvTokenSecret:
				return "token-secret", true
			default:
				return "", false
			}
		},
		HTTPClient: server.Client(),
	}
}
