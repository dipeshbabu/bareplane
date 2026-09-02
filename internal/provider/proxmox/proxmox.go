package proxmox

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/provider"
)

const Type = "proxmox"

type Provider struct {
	endpoint *url.URL
}

func New(cfg config.Provider) (provider.Provider, error) {
	parsed, err := parseEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	return &Provider{endpoint: parsed}, nil
}

func (p *Provider) Type() string {
	return Type
}

func (p *Provider) Validate(cfg config.Provider) error {
	if cfg.Type != Type {
		return fmt.Errorf("provider type must be %q", Type)
	}
	parsed, err := parseEndpoint(cfg.Endpoint)
	if err != nil {
		return err
	}
	if p.endpoint == nil || p.endpoint.String() != parsed.String() {
		return fmt.Errorf("provider endpoint does not match initialized endpoint")
	}
	return nil
}

func (p *Provider) Endpoint() string {
	if p.endpoint == nil {
		return ""
	}
	return p.endpoint.String()
}

func parseEndpoint(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("endpoint is required")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, fmt.Errorf("endpoint scheme must be http or https")
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("endpoint host is required")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("endpoint must not include credentials, query parameters, or fragments")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed, nil
}
