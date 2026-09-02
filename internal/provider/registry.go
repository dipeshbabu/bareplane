package provider

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
)

type Factory func(config.Provider) (Provider, error)

type Registry struct {
	factories map[string]Factory
}

func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

func (r *Registry) Register(providerType string, factory Factory) error {
	providerType = strings.TrimSpace(providerType)
	if providerType == "" {
		return fmt.Errorf("provider type is empty")
	}
	if factory == nil {
		return fmt.Errorf("provider %q factory is nil", providerType)
	}
	if _, exists := r.factories[providerType]; exists {
		return fmt.Errorf("provider %q is already registered", providerType)
	}
	r.factories[providerType] = factory
	return nil
}

func (r *Registry) Resolve(cfg config.Provider) (Provider, error) {
	factory, ok := r.factories[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("unsupported provider %q; available providers: %s", cfg.Type, strings.Join(r.Types(), ", "))
	}

	resolved, err := factory(cfg)
	if err != nil {
		return nil, fmt.Errorf("initialize provider %q: %w", cfg.Type, err)
	}
	if resolved == nil {
		return nil, fmt.Errorf("initialize provider %q: factory returned nil", cfg.Type)
	}
	if resolved.Type() != cfg.Type {
		return nil, fmt.Errorf("initialize provider %q: factory returned provider type %q", cfg.Type, resolved.Type())
	}
	if err := resolved.Validate(cfg); err != nil {
		return nil, fmt.Errorf("validate provider %q: %w", cfg.Type, err)
	}
	return resolved, nil
}

func (r *Registry) Types() []string {
	types := make([]string, 0, len(r.factories))
	for providerType := range r.factories {
		types = append(types, providerType)
	}
	sort.Strings(types)
	return types
}
