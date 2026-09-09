package config

import (
	"strings"
	"testing"
)

func sopsFixture() Config {
	return Config{Spec: Spec{Secrets: Secrets{Provider: "sops", SOPS: &SOPSConfig{
		AgeKey: &SOPSKeyReference{Name: "bareplane-sops-age", Key: "keys.txt"}, Namespaces: []string{"workloads"},
	}}}}
}

func TestSOPSReferencesAreExplicitBoundedAndPublic(t *testing.T) {
	for name, mutate := range map[string]func(*SOPSConfig){
		"no keys":              func(s *SOPSConfig) { s.AgeKey = nil },
		"existing Argo secret": func(s *SOPSConfig) { s.AgeKey.Name = "argocd-secret" },
		"path key":             func(s *SOPSConfig) { s.AgeKey.Key = "../key" },
		"oversized key":        func(s *SOPSConfig) { s.AgeKey.Key = strings.Repeat("x", 254) },
		"empty allowlist":      func(s *SOPSConfig) { s.Namespaces = nil },
		"unbounded allowlist":  func(s *SOPSConfig) { s.Namespaces = make([]string, 33) },
		"key namespace":        func(s *SOPSConfig) { s.Namespaces = []string{"argocd"} },
		"bootstrap namespace":  func(s *SOPSConfig) { s.Namespaces = []string{"kube-system"} },
		"duplicate namespace":  func(s *SOPSConfig) { s.Namespaces = []string{"workloads", "workloads"} },
		"wildcard namespace":   func(s *SOPSConfig) { s.Namespaces = []string{"*"} },
		"invalid revision":     func(s *SOPSConfig) { s.Revision = "private/key" },
		"orphan passphrase":    func(s *SOPSConfig) { s.PGPPassphrase = &SOPSKeyReference{Name: "bareplane-sops-pass", Key: "pass"} },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := sopsFixture()
			mutate(cfg.Spec.Secrets.SOPS)
			if _, err := cfg.RequireSOPS(); err == nil {
				t.Fatal("unsafe SOPS configuration accepted")
			}
		})
	}
	cfg := sopsFixture()
	settings, err := cfg.RequireSOPS()
	if err != nil || settings.Revision != "initial" {
		t.Fatal(settings, err)
	}
	cfg.Spec.Secrets.Provider = "vault"
	if _, err := cfg.RequireSOPS(); err == nil {
		t.Fatal("SOPS accepted with Vault provider")
	}
	cfg.Spec.Secrets.SOPS = nil
	if _, err := cfg.RequireSOPS(); err == nil {
		t.Fatal("implicit SOPS activation")
	}
}

func TestSOPSCanonicalizationDoesNotMutateCaller(t *testing.T) {
	cfg := sopsFixture()
	cfg.Spec.Secrets.SOPS.Namespaces = []string{"z", "a"}
	cfg.Spec.Secrets.SOPS.PGPKey = &SOPSKeyReference{Name: "bareplane-sops-pgp", Key: "private.asc"}
	cfg.Spec.Secrets.SOPS.PGPPassphrase = &SOPSKeyReference{Name: "bareplane-sops-pass", Key: "passphrase"}
	s, err := cfg.RequireSOPS()
	if err != nil || s.Namespaces[0] != "a" || cfg.Spec.Secrets.SOPS.Namespaces[0] != "z" {
		t.Fatal(s, err)
	}
}
