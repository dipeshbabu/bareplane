package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// SOPSConfig contains public references only. The operator delivers key bytes
// to the already-owned argocd namespace after installation, outside Git.
type SOPSConfig struct {
	AgeKey        *SOPSKeyReference `yaml:"ageKey,omitempty"`
	PGPKey        *SOPSKeyReference `yaml:"pgpKey,omitempty"`
	PGPPassphrase *SOPSKeyReference `yaml:"pgpPassphrase,omitempty"`
	Namespaces    []string          `yaml:"namespaces"`
	Revision      string            `yaml:"revision,omitempty"`
}

type SOPSKeyReference struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key"`
}

var sopsSecretKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)

func (c Config) validateSOPS() []string {
	s := c.Spec.Secrets.SOPS
	if s == nil {
		return nil
	}
	var problems []string
	if c.Spec.Secrets.Provider != "sops" {
		problems = append(problems, "spec.secrets.sops requires the sops provider")
	}
	if s.AgeKey == nil && s.PGPKey == nil {
		problems = append(problems, "spec.secrets.sops requires at least one ageKey or pgpKey reference")
	}
	if s.PGPPassphrase != nil && s.PGPKey == nil {
		problems = append(problems, "spec.secrets.sops.pgpPassphrase requires pgpKey")
	}
	for _, slot := range []struct {
		name string
		ref  *SOPSKeyReference
	}{{"ageKey", s.AgeKey}, {"pgpKey", s.PGPKey}, {"pgpPassphrase", s.PGPPassphrase}} {
		if slot.ref != nil && (!validName(slot.ref.Name) || !strings.HasPrefix(slot.ref.Name, "bareplane-sops-") || !sopsSecretKey.MatchString(slot.ref.Key)) {
			problems = append(problems, "spec.secrets.sops."+slot.name+" must reference a dedicated bareplane-sops-* Secret and a portable data key in argocd")
		}
	}
	if s.Revision != "" && !validName(s.Revision) {
		problems = append(problems, "spec.secrets.sops.revision must be a public lowercase DNS label")
	}
	if len(s.Namespaces) < 1 || len(s.Namespaces) > 32 {
		return append(problems, "spec.secrets.sops.namespaces must explicitly allow 1 to 32 application namespaces")
	}
	seen := make(map[string]bool, len(s.Namespaces))
	for _, namespace := range s.Namespaces {
		if !validName(namespace) || namespace == "argocd" || strings.HasPrefix(namespace, "kube-") || seen[namespace] {
			problems = append(problems, "spec.secrets.sops.namespaces must contain unique application DNS labels, excluding argocd and kube-* namespaces")
		}
		seen[namespace] = true
	}
	return problems
}

// RequireSOPS fails before producing a partial export. The historical provider
// default alone still does not activate a decryption integration.
func (c Config) RequireSOPS() (SOPSConfig, error) {
	if c.Spec.Secrets.SOPS == nil {
		return SOPSConfig{}, fmt.Errorf("secrets-sops requires explicit spec.secrets.sops key references and namespaces")
	}
	if problems := c.validateSOPS(); len(problems) != 0 {
		return SOPSConfig{}, &ValidationError{Problems: problems}
	}
	result := *c.Spec.Secrets.SOPS
	result.Namespaces = append([]string(nil), result.Namespaces...)
	sort.Strings(result.Namespaces)
	if result.Revision == "" {
		result.Revision = "initial"
	}
	return result, nil
}
