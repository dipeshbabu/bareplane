package proxmox

import (
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
)

func TestNewValidatesEndpoint(t *testing.T) {
	resolved, err := New(config.Provider{Type: Type, Endpoint: "https://proxmox.example.com:8006/"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	p, ok := resolved.(*Provider)
	if !ok {
		t.Fatalf("unexpected provider type %T", resolved)
	}
	if got := p.Endpoint(); got != "https://proxmox.example.com:8006" {
		t.Fatalf("unexpected normalized endpoint %q", got)
	}
}

func TestNewRejectsUnsafeEndpointForms(t *testing.T) {
	for _, endpoint := range []string{
		"",
		"proxmox.example.com:8006",
		"ftp://proxmox.example.com",
		"https://user:pass@proxmox.example.com:8006",
		"https://proxmox.example.com:8006?token=secret",
	} {
		t.Run(endpoint, func(t *testing.T) {
			_, err := New(config.Provider{Type: Type, Endpoint: endpoint})
			if err == nil {
				t.Fatal("expected endpoint validation error")
			}
		})
	}
}

func TestValidateRejectsTypeMismatch(t *testing.T) {
	resolved, err := New(config.Provider{Type: Type, Endpoint: "https://proxmox.example.com:8006"})
	if err != nil {
		t.Fatal(err)
	}

	err = resolved.Validate(config.Provider{Type: "other", Endpoint: "https://proxmox.example.com:8006"})
	if err == nil || !strings.Contains(err.Error(), "provider type") {
		t.Fatalf("expected type validation error, got %v", err)
	}
}
