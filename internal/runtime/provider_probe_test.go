package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/doctor"
	"github.com/dipeshbabu/bareplane/internal/provider/proxmox"
)

func TestProviderProbeReportsReachableProxmoxVersion(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":{"version":"8.3.1","release":"8.3"}}`)
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Spec.Provider.Endpoint = server.URL
	probe := ProviderProbe(ProbeDependencies{
		LookupEnv:  testLookupEnv("root@pam!bareplane", "token-secret"),
		HTTPClient: server.Client(),
	})

	result := probe(context.Background(), cfg)
	if result.Status != doctor.StatusPass {
		t.Fatalf("expected pass, got %#v", result)
	}
	if !strings.Contains(result.Message, "8.3.1") {
		t.Fatalf("expected version in message, got %q", result.Message)
	}
}

func TestProviderProbeReportsMissingCredentials(t *testing.T) {
	cfg := config.Default()
	probe := ProviderProbe(ProbeDependencies{
		LookupEnv: func(string) (string, bool) { return "", false },
	})

	result := probe(context.Background(), cfg)
	if result.Status != doctor.StatusFail {
		t.Fatalf("expected failure, got %#v", result)
	}
	if !strings.Contains(result.Message, proxmox.EnvTokenID) {
		t.Fatalf("expected missing environment variable name, got %q", result.Message)
	}
}

func TestProviderProbeDoesNotLeakTokenSecret(t *testing.T) {
	const secret = "sensitive-token-secret"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintf(w, `{"message":"bad token %s"}`, secret)
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Spec.Provider.Endpoint = server.URL
	probe := ProviderProbe(ProbeDependencies{
		LookupEnv:  testLookupEnv("root@pam!bareplane", secret),
		HTTPClient: server.Client(),
	})

	result := probe(context.Background(), cfg)
	if result.Status != doctor.StatusFail {
		t.Fatalf("expected failure, got %#v", result)
	}
	if strings.Contains(result.Message, secret) {
		t.Fatalf("secret leaked in diagnostic message: %q", result.Message)
	}
	if !strings.Contains(result.Message, "[REDACTED]") {
		t.Fatalf("expected redaction marker, got %q", result.Message)
	}
}

func testLookupEnv(tokenID, tokenSecret string) proxmox.LookupEnvFunc {
	return func(key string) (string, bool) {
		switch key {
		case proxmox.EnvTokenID:
			return tokenID, tokenID != ""
		case proxmox.EnvTokenSecret:
			return tokenSecret, tokenSecret != ""
		default:
			return "", false
		}
	}
}
