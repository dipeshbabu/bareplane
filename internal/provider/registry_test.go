package provider

import (
	"errors"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/config"
)

type testProvider struct {
	providerType string
	validateErr  error
}

func (p testProvider) Type() string { return p.providerType }
func (p testProvider) Validate(config.Provider) error { return p.validateErr }

func TestRegistryResolve(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("test", func(config.Provider) (Provider, error) {
		return testProvider{providerType: "test"}, nil
	}); err != nil {
		t.Fatal(err)
	}

	resolved, err := registry.Resolve(config.Provider{Type: "test"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Type() != "test" {
		t.Fatalf("unexpected provider type %q", resolved.Type())
	}
}

func TestRegistryRejectsDuplicateRegistration(t *testing.T) {
	registry := NewRegistry()
	factory := func(config.Provider) (Provider, error) { return testProvider{providerType: "test"}, nil }
	if err := registry.Register("test", factory); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("test", factory); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

func TestRegistryReportsAvailableProviders(t *testing.T) {
	registry := NewRegistry()
	for _, name := range []string{"zeta", "alpha"} {
		name := name
		if err := registry.Register(name, func(config.Provider) (Provider, error) {
			return testProvider{providerType: name}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	_, err := registry.Resolve(config.Provider{Type: "missing"})
	if err == nil || !strings.Contains(err.Error(), "alpha, zeta") {
		t.Fatalf("expected sorted provider list, got %v", err)
	}
}

func TestRegistryPropagatesValidationError(t *testing.T) {
	registry := NewRegistry()
	expected := errors.New("invalid")
	if err := registry.Register("test", func(config.Provider) (Provider, error) {
		return testProvider{providerType: "test", validateErr: expected}, nil
	}); err != nil {
		t.Fatal(err)
	}

	_, err := registry.Resolve(config.Provider{Type: "test"})
	if !errors.Is(err, expected) {
		t.Fatalf("expected validation error, got %v", err)
	}
}
