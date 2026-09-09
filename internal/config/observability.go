package config

import "fmt"

// ObservabilityConfig requires an explicit acknowledgement of disposable local
// history. Persistent storage is a separate capability; never silently fall back.
type ObservabilityConfig struct {
	Storage        ObservabilityStorage `yaml:"storage"`
	RetentionHours int                  `yaml:"retentionHours,omitempty"`
}

type ObservabilityStorage struct {
	Mode string `yaml:"mode"`
}

func (c Config) validateObservability() []string {
	settings := c.Spec.Observability
	if settings == nil {
		return nil
	}
	var problems []string
	if settings.Storage.Mode != "ephemeral" {
		problems = append(problems, "spec.observability.storage.mode must explicitly be ephemeral; persistent storage is not implemented for this baseline")
	}
	if settings.RetentionHours != 0 && (settings.RetentionHours < 6 || settings.RetentionHours > 168) {
		problems = append(problems, "spec.observability.retentionHours must be between 6 and 168; default is 24")
	}
	nodes := 0
	for _, group := range c.Spec.Nodes {
		if group.Count < 1 || group.Count > 100-nodes {
			return append(problems, "spec.observability baseline supports at most 100 machines; larger topologies require reviewed sizing")
		}
		nodes += group.Count
	}
	if nodes == 0 {
		problems = append(problems, "spec.observability requires at least one machine")
	}
	return problems
}

func (c Config) RequireObservability() (ObservabilityConfig, error) {
	if c.Spec.Observability == nil {
		return ObservabilityConfig{}, fmt.Errorf("observability requires explicit spec.observability.storage.mode: ephemeral")
	}
	if problems := c.validateObservability(); len(problems) > 0 {
		return ObservabilityConfig{}, &ValidationError{Problems: problems}
	}
	settings := *c.Spec.Observability
	if settings.RetentionHours == 0 {
		settings.RetentionHours = 24
	}
	return settings, nil
}
