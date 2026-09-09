package config

import (
	"fmt"

	"github.com/dipeshbabu/bareplane/internal/platform"
)

// ComponentSelection changes optional defaults without changing ownership or
// bypassing required profile/dependency and availability checks.
type ComponentSelection struct {
	Enabled  []string `yaml:"enabled,omitempty"`
	Disabled []string `yaml:"disabled,omitempty"`
}

func (c Config) PlatformSelection() platform.Selection {
	selection := platform.Selection{Profiles: append([]string(nil), c.Spec.Profiles...)}
	if c.Spec.Components != nil {
		selection.Enabled = append(selection.Enabled, c.Spec.Components.Enabled...)
		selection.Disabled = append(selection.Disabled, c.Spec.Components.Disabled...)
	}
	if c.Spec.Features.GPU {
		selection.Enabled = append(selection.Enabled, "gpu-scheduling")
	}
	if c.Spec.Certificates != nil {
		selection.Enabled = append(selection.Enabled, "cert-manager")
	}
	if c.Spec.Features.Observability || c.Spec.Observability != nil {
		selection.Enabled = append(selection.Enabled, "observability")
	}
	if c.Spec.DNS.Provider == "cloudflare" {
		selection.Enabled = append(selection.Enabled, "external-dns")
	}
	if c.Spec.Secrets.Provider == "vault" {
		selection.Enabled = append(selection.Enabled, "vault")
	}
	if c.Spec.Secrets.SOPS != nil {
		selection.Enabled = append(selection.Enabled, "secrets-sops")
	}
	return selection
}

// ResolvePlatform is diagnostic: a structurally valid unavailable capability is
// still a valid config, but execution/rendering must call RequireAvailable.
func (c Config) ResolvePlatform() (platform.Resolution, error) {
	registry, err := platform.Builtin()
	if err != nil {
		return platform.Resolution{}, err
	}
	return registry.Resolve(c.PlatformSelection())
}

func (c Config) validateComponents() []string {
	if c.Spec.Components == nil {
		return nil
	}
	var problems []string
	for _, selection := range []struct {
		name   string
		values []string
	}{{"enabled", c.Spec.Components.Enabled}, {"disabled", c.Spec.Components.Disabled}} {
		if len(selection.values) > 512 {
			problems = append(problems, "spec.components."+selection.name+" exceeds the component limit")
			continue
		}
		seen := map[string]bool{}
		for index, id := range selection.values {
			if !platform.ValidIdentifier(id) {
				problems = append(problems, fmt.Sprintf("spec.components.%s[%d] must be a portable component identifier", selection.name, index))
			}
			if seen[id] {
				problems = append(problems, "spec.components."+selection.name+" contains duplicate identifiers")
			}
			seen[id] = true
		}
	}
	if len(problems) == 0 {
		if _, err := c.ResolvePlatform(); err != nil {
			problems = append(problems, "spec.components: "+err.Error())
		}
	}
	return problems
}
