package config

import (
	"strings"
	"testing"
)

func vaultFixture() Config {
	return Config{Spec: Spec{Secrets: Secrets{Provider: "vault", Vault: &VaultConfig{
		Address: "https://vault.example.com:8200", WorkloadNamespace: "workloads", AuthMount: "kubernetes",
		Role: "workloads-reader", Audience: "vault", KVMount: "kv",
		Secrets: []VaultSecret{{Name: "database", Path: "apps/database", Keys: []VaultSecretKey{{SecretKey: "password", Property: "password"}}}},
	}}}}
}

func TestVaultTLSOriginRejectsCredentialsTraversalAndInsecureOverrides(t *testing.T) {
	for _, value := range []string{"http://vault.example.com", "https://user:PRIVATE@vault.example.com", "https://vault.example.com/path",
		"https://vault.example.com?token=PRIVATE", "https://vault.example.com?", "https://vault.example.com#", "https://vault.example.com/%2f",
		"https://vault.example.com:0", "https://vault.example.com:65536", "https://vault.example.com:0443", "https://vault.example.com:",
		"https://localhost", "https://127.0.0.1:8200", "https://0.0.0.0", "https://[::1]", "https://[fe80::1%25eth0]", "https://[::ffff:192.0.2.1]",
		"https://vault.example.com.", "https://Vault.example.com", "https://vault.example.com/../", "https://@vault.example.com"} {
		t.Run(value, func(t *testing.T) {
			cfg := vaultFixture()
			cfg.Spec.Secrets.Vault.Address = value
			if _, err := cfg.RequireVault(); err == nil || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("unsafe Vault URL accepted or exposed")
			}
		})
	}
	for _, value := range []string{"https://vault.example.com", "https://vault.example.com:443/", "https://192.0.2.1:8200", "https://[2001:db8::1]:8200"} {
		if !validVaultAddress(value) {
			t.Fatalf("valid TLS origin refused: %s", value)
		}
	}
}

func TestVaultReferencesAndOutputsAreBoundedAndCannotReadTheirOwnTrust(t *testing.T) {
	for name, mutate := range map[string]func(*VaultConfig){
		"no namespace":              func(v *VaultConfig) { v.WorkloadNamespace = "" },
		"Argo keys namespace":       func(v *VaultConfig) { v.WorkloadNamespace = "argocd" },
		"bootstrap namespace":       func(v *VaultConfig) { v.WorkloadNamespace = "kube-system" },
		"operator namespace":        func(v *VaultConfig) { v.WorkloadNamespace = "vault-secrets" },
		"missing role":              func(v *VaultConfig) { v.Role = "" },
		"auth traversal":            func(v *VaultConfig) { v.AuthMount = "../root" },
		"engine wildcard":           func(v *VaultConfig) { v.KVMount = "*" },
		"missing audience":          func(v *VaultConfig) { v.Audience = "" },
		"audience newline":          func(v *VaultConfig) { v.Audience = "vault\ninjected" },
		"Vault namespace traversal": func(v *VaultConfig) { v.VaultNamespace = "team/../admin" },
		"CA secret key path":        func(v *VaultConfig) { v.CASecret = &VaultCAReference{Name: "vault-ca", Key: "../ca"} },
		"self-referential CA":       func(v *VaultConfig) { v.CASecret = &VaultCAReference{Name: "database", Key: "ca.crt"} },
		"too fast":                  func(v *VaultConfig) { v.RefreshInterval = "29s" },
		"too slow":                  func(v *VaultConfig) { v.RefreshInterval = "25h" },
		"fractional seconds":        func(v *VaultConfig) { v.RefreshInterval = "30.5s" },
		"overflow duration":         func(v *VaultConfig) { v.RefreshInterval = strings.Repeat("9", 30) + "h" },
		"no secrets":                func(v *VaultConfig) { v.Secrets = nil },
		"unbounded secrets":         func(v *VaultConfig) { v.Secrets = make([]VaultSecret, 33) },
		"duplicate secrets":         func(v *VaultConfig) { v.Secrets = append(v.Secrets, v.Secrets[0]) },
		"invalid secret name":       func(v *VaultConfig) { v.Secrets[0].Name = "../target" },
		"no source path":            func(v *VaultConfig) { v.Secrets[0].Path = "" },
		"absolute source path":      func(v *VaultConfig) { v.Secrets[0].Path = "/apps/key" },
		"wildcard source":           func(v *VaultConfig) { v.Secrets[0].Path = "apps/*" },
		"path traversal":            func(v *VaultConfig) { v.Secrets[0].Path = "apps/../admin" },
		"encoded traversal":         func(v *VaultConfig) { v.Secrets[0].Path = "apps/%2e%2e/admin" },
		"no keys":                   func(v *VaultConfig) { v.Secrets[0].Keys = nil },
		"too many keys":             func(v *VaultConfig) { v.Secrets[0].Keys = make([]VaultSecretKey, 65) },
		"duplicate key":             func(v *VaultConfig) { v.Secrets[0].Keys = append(v.Secrets[0].Keys, v.Secrets[0].Keys[0]) },
		"key path":                  func(v *VaultConfig) { v.Secrets[0].Keys[0].SecretKey = "path/key" },
		"property query":            func(v *VaultConfig) { v.Secrets[0].Keys[0].Property = "data.#(key==value)" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := vaultFixture()
			mutate(cfg.Spec.Secrets.Vault)
			if _, err := cfg.RequireVault(); err == nil {
				t.Fatal("unsafe Vault configuration accepted")
			}
		})
	}
}

func TestVaultCanonicalizationCopiesNestedSlicesAndKeepsLegacyProviderDiagnostic(t *testing.T) {
	cfg := vaultFixture()
	cfg.Spec.Secrets.Vault.Address += "/"
	cfg.Spec.Secrets.Vault.Secrets[0].Keys = []VaultSecretKey{{SecretKey: "z", Property: "last"}, {SecretKey: "a", Property: "first"}}
	cfg.Spec.Secrets.Vault.CASecret = &VaultCAReference{Name: "vault-ca", Key: "ca.crt"}
	value, err := cfg.RequireVault()
	if err != nil || value.Address != "https://vault.example.com:8200" || value.RefreshInterval != "1m" {
		t.Fatal(value, err)
	}
	if value.Secrets[0].Keys[0].SecretKey != "a" || cfg.Spec.Secrets.Vault.Secrets[0].Keys[0].SecretKey != "z" {
		t.Fatal("caller configuration was mutated")
	}
	cfg.Spec.Secrets.Provider = "sops"
	if _, err := cfg.RequireVault(); err == nil {
		t.Fatal("Vault settings accepted with another provider")
	}
	cfg.Spec.Secrets.Vault = nil
	if _, err := cfg.RequireVault(); err == nil {
		t.Fatal("implicit Vault integration accepted")
	}
	if len(cfg.validateVault()) != 0 {
		t.Fatal("legacy configuration acquired implicit Vault requirements")
	}
}
