package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// VaultConfig references an externally managed, TLS-authenticated Vault. The
// operator obtains short-lived, audience-bound Kubernetes tokens at runtime;
// neither those tokens nor Vault secret values are configuration inputs.
type VaultConfig struct {
	Address           string            `yaml:"address"`
	WorkloadNamespace string            `yaml:"workloadNamespace"`
	AuthMount         string            `yaml:"authMount"`
	Role              string            `yaml:"role"`
	Audience          string            `yaml:"audience"`
	KVMount           string            `yaml:"kvMount"`
	VaultNamespace    string            `yaml:"vaultNamespace,omitempty"`
	CASecret          *VaultCAReference `yaml:"caSecret,omitempty"`
	RefreshInterval   string            `yaml:"refreshInterval,omitempty"`
	Secrets           []VaultSecret     `yaml:"secrets"`
}

type VaultCAReference struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key"`
}

type VaultSecret struct {
	Name string           `yaml:"name"`
	Path string           `yaml:"path"`
	Keys []VaultSecretKey `yaml:"keys"`
}

type VaultSecretKey struct {
	SecretKey string `yaml:"secretKey"`
	Property  string `yaml:"property"`
}

var (
	vaultReference = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,61}[A-Za-z0-9])?$`)
	vaultAudience  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	vaultDataKey   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,253}$`)
	vaultProperty  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
)

func validVaultAddress(value string) bool {
	if len(value) > 512 {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(value, "#") || (u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return false
	}
	host := u.Hostname()
	if ip, err := netip.ParseAddr(host); err == nil {
		if !ip.IsGlobalUnicast() || ip.Is4In6() || ip.Zone() != "" {
			return false
		}
	} else if !validDomain(host) || !strings.Contains(host, ".") {
		return false
	}
	if port := u.Port(); port != "" {
		parsed, err := strconv.ParseUint(port, 10, 16)
		if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != port {
			return false
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return false
	}
	return true
}

func validVaultPath(value string) bool {
	if len(value) == 0 || len(value) > 255 {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) > 16 {
		return false
	}
	for _, part := range parts {
		if !vaultReference.MatchString(part) || strings.Contains(part, "..") {
			return false
		}
	}
	return true
}

func (c Config) validateVault() []string {
	v := c.Spec.Secrets.Vault
	if v == nil {
		return nil
	}
	var problems []string
	if c.Spec.Secrets.Provider != "vault" {
		problems = append(problems, "spec.secrets.vault requires the vault provider")
	}
	if !validVaultAddress(v.Address) {
		problems = append(problems, "spec.secrets.vault.address must be an HTTPS origin with a canonical DNS name or unicast IP, without credentials, paths or query parameters")
	}
	if !validName(v.WorkloadNamespace) || v.WorkloadNamespace == "argocd" || v.WorkloadNamespace == "vault-secrets" || strings.HasPrefix(v.WorkloadNamespace, "kube-") {
		problems = append(problems, "spec.secrets.vault.workloadNamespace must identify one existing workload namespace, excluding argocd, kube-* and vault-secrets")
	}
	for _, field := range []struct{ name, value string }{{"authMount", v.AuthMount}, {"role", v.Role}, {"kvMount", v.KVMount}} {
		if !vaultReference.MatchString(field.value) || strings.Contains(field.value, "..") {
			problems = append(problems, "spec.secrets.vault."+field.name+" must be a portable single-segment Vault reference")
		}
	}
	if !vaultAudience.MatchString(v.Audience) {
		problems = append(problems, "spec.secrets.vault.audience must explicitly match the Vault role's bounded token audience")
	}
	if v.VaultNamespace != "" && !validVaultPath(v.VaultNamespace) {
		problems = append(problems, "spec.secrets.vault.vaultNamespace must be a bounded relative Vault namespace")
	}
	if v.CASecret != nil && (!validName(v.CASecret.Name) || !vaultDataKey.MatchString(v.CASecret.Key)) {
		problems = append(problems, "spec.secrets.vault.caSecret must reference a CA data key in the workload namespace")
	}
	if v.RefreshInterval != "" {
		duration, err := time.ParseDuration(v.RefreshInterval)
		if err != nil || duration < 30*time.Second || duration > 24*time.Hour || duration%time.Second != 0 {
			problems = append(problems, "spec.secrets.vault.refreshInterval must be whole seconds between 30s and 24h")
		}
	}
	if len(v.Secrets) < 1 || len(v.Secrets) > 32 {
		return append(problems, "spec.secrets.vault.secrets must explicitly select 1 to 32 target Secrets")
	}
	seen := make(map[string]bool, len(v.Secrets))
	for index, secret := range v.Secrets {
		prefix := fmt.Sprintf("spec.secrets.vault.secrets[%d]", index)
		if !validName(secret.Name) || seen[secret.Name] || (v.CASecret != nil && secret.Name == v.CASecret.Name) {
			problems = append(problems, prefix+".name must be a unique workload Secret name, not the CA trust reference")
		}
		seen[secret.Name] = true
		if !validVaultPath(secret.Path) {
			problems = append(problems, prefix+".path must be a bounded relative KV-v2 secret path, without wildcards or traversal")
		}
		if len(secret.Keys) < 1 || len(secret.Keys) > 64 {
			problems = append(problems, prefix+".keys must explicitly select 1 to 64 data properties")
			continue
		}
		keys := make(map[string]bool, len(secret.Keys))
		for _, key := range secret.Keys {
			if !vaultDataKey.MatchString(key.SecretKey) || keys[key.SecretKey] || !vaultProperty.MatchString(key.Property) {
				problems = append(problems, prefix+".keys must map unique Kubernetes data keys to portable Vault properties")
			}
			keys[key.SecretKey] = true
		}
	}
	return problems
}

func (c Config) RequireVault() (VaultConfig, error) {
	if c.Spec.Secrets.Vault == nil {
		return VaultConfig{}, fmt.Errorf("vault requires explicit spec.secrets.vault TLS, Kubernetes auth and secret references")
	}
	if problems := c.validateVault(); len(problems) > 0 {
		return VaultConfig{}, &ValidationError{Problems: problems}
	}
	v := *c.Spec.Secrets.Vault
	v.Address = strings.TrimSuffix(v.Address, "/")
	if v.RefreshInterval == "" {
		v.RefreshInterval = "1m"
	}
	v.Secrets = append([]VaultSecret(nil), v.Secrets...)
	for index := range v.Secrets {
		v.Secrets[index].Keys = append([]VaultSecretKey(nil), v.Secrets[index].Keys...)
		sort.Slice(v.Secrets[index].Keys, func(i, j int) bool { return v.Secrets[index].Keys[i].SecretKey < v.Secrets[index].Keys[j].SecretKey })
	}
	sort.Slice(v.Secrets, func(i, j int) bool { return v.Secrets[i].Name < v.Secrets[j].Name })
	return v, nil
}
