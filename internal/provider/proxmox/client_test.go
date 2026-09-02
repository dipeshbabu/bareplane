package proxmox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientVersionUsesTokenAuthAndDecodesEnvelope(t *testing.T) {
	credentials := Credentials{TokenID: "root@pam!bareplane", TokenSecret: "token-secret"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api2/json/version" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		wantAuth := "PVEAPIToken=" + credentials.TokenID + "=" + credentials.TokenSecret
		if got := r.Header.Get("Authorization"); got != wantAuth {
			t.Fatalf("authorization header = %q, want %q", got, wantAuth)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("accept header = %q", got)
		}
		_, _ = fmt.Fprint(w, `{"data":{"version":"8.3.1","release":"8.3","repoid":"abc123"}}`)
	}))
	defer server.Close()

	provided := server.Client()
	provided.Timeout = 0
	client, err := NewClient(server.URL, credentials, provided)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if provided.Timeout != 0 {
		t.Fatalf("NewClient mutated caller HTTP client timeout to %s", provided.Timeout)
	}

	version, err := client.Version(context.Background())
	if err != nil {
		t.Fatalf("Version() error = %v", err)
	}
	if version.Version != "8.3.1" || version.Release != "8.3" || version.RepoID != "abc123" {
		t.Fatalf("unexpected version: %#v", version)
	}
}

func TestClientNodesDecodesTypedNodes(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"data":[{"node":"pve1","status":"online","cpu":0.25,"maxcpu":16,"mem":1024,"maxmem":65536,"uptime":1234}]}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, testCredentials(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := client.Nodes(context.Background())
	if err != nil {
		t.Fatalf("Nodes() error = %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "pve1" || nodes[0].MaxCPU != 16 || nodes[0].Uptime != 1234 {
		t.Fatalf("unexpected nodes: %#v", nodes)
	}
}

func TestClientReturnsTypedAPIErrorAndRedactsSecret(t *testing.T) {
	credentials := Credentials{TokenID: "root@pam!bareplane", TokenSecret: "super-secret"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintf(w, `{"message":"permission denied for %s"}`, credentials.TokenSecret)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, credentials, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Version(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %v", err)
	}
	if apiErr.StatusCode != http.StatusForbidden {
		t.Fatalf("status code = %d", apiErr.StatusCode)
	}
	if strings.Contains(err.Error(), credentials.TokenSecret) {
		t.Fatalf("token secret leaked in error: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("expected redaction marker, got %v", err)
	}
}

func TestClientRejectsMalformedEnvelope(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, testCredentials(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Version(context.Background())
	if err == nil || !strings.Contains(err.Error(), "data is missing") {
		t.Fatalf("expected missing data error, got %v", err)
	}
}

func TestClientHonorsContextCancellation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	client, err := NewClient(server.URL, testCredentials(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Version(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestClientRequiresHTTPS(t *testing.T) {
	_, err := NewClient("http://proxmox.example.com:8006", testCredentials(), nil)
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("expected HTTPS validation error, got %v", err)
	}
}

func testCredentials() Credentials {
	return Credentials{TokenID: "root@pam!bareplane", TokenSecret: "token-secret"}
}
