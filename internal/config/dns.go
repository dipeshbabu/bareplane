package config

import (
	"errors"
	"net"
	"regexp"
	"strings"
)

// DNSAutomation is an explicit, single-zone/subdomain contract. Credential
// values and arbitrary provider endpoints/flags are deliberately unsupported.
type DNSAutomation struct {
	ZoneID          string             `yaml:"zoneID"`
	ZoneName        string             `yaml:"zoneName"`
	Domain          string             `yaml:"domain"`
	SourceNamespace string             `yaml:"sourceNamespace"`
	OwnerID         string             `yaml:"ownerID"`
	Mode            string             `yaml:"mode,omitempty"`
	TokenSecret     DNSSecretReference `yaml:"tokenSecret"`
}

type DNSSecretReference struct {
	Name     string `yaml:"name"`
	Key      string `yaml:"key"`
	Revision string `yaml:"revision,omitempty"`
}

var dnsIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var secretDataKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)

func (d DNSAutomation) EffectiveMode() string {
	if d.Mode == "" {
		return "dry-run"
	}
	return d.Mode
}

func (d DNSSecretReference) EffectiveRevision() string {
	if d.Revision == "" {
		return "initial"
	}
	return d.Revision
}

func validateDNSAutomation(dns DNS) []string {
	automation := dns.Automation
	if automation == nil {
		// Backend selection remains usable in legacy/bootstrap-only configs.
		// Rendering the controller requires the full contract below.
		return nil
	}
	var problems []string
	if dns.Provider != "cloudflare" {
		problems = append(problems, "spec.dns.automation requires the cloudflare provider; manual DNS never runs a controller")
	}
	if !dnsIDPattern.MatchString(automation.ZoneID) {
		problems = append(problems, "spec.dns.automation.zoneID must be one explicit lowercase 32-digit hexadecimal Cloudflare zone ID")
	}
	if !dnsIDPattern.MatchString(automation.OwnerID) {
		problems = append(problems, "spec.dns.automation.ownerID must be a unique lowercase 32-digit hexadecimal ownership ID; do not reuse another controller's ID")
	}
	if !validDomain(automation.ZoneName) || !strings.Contains(automation.ZoneName, ".") || net.ParseIP(automation.ZoneName) != nil {
		problems = append(problems, "spec.dns.automation.zoneName must be a canonical DNS zone, not an address or wildcard")
	}
	if !validDomain(automation.Domain) || net.ParseIP(automation.Domain) != nil || !strings.HasSuffix(automation.Domain, "."+automation.ZoneName) {
		problems = append(problems, "spec.dns.automation.domain must be one explicit subdomain strictly below zoneName; zone-apex automation is not supported")
	}
	namespace := automation.SourceNamespace
	if !validName(namespace) || strings.HasPrefix(namespace, "kube-") || namespace == "argocd" || namespace == "external-dns" {
		problems = append(problems, "spec.dns.automation.sourceNamespace must be an explicit application namespace, not bootstrap or controller state")
	}
	if automation.EffectiveMode() != "dry-run" && automation.EffectiveMode() != "apply" {
		problems = append(problems, "spec.dns.automation.mode must be dry-run or apply")
	}
	if !validName(automation.TokenSecret.Name) || !secretDataKeyPattern.MatchString(automation.TokenSecret.Key) {
		problems = append(problems, "spec.dns.automation.tokenSecret requires a valid Secret name and data key in external-dns; never provide token values")
	}
	if !validName(automation.TokenSecret.EffectiveRevision()) {
		problems = append(problems, "spec.dns.automation.tokenSecret.revision must be a public rollout identifier, not credential content")
	}
	return problems
}

func (c Config) RequireDNSAutomation() (*DNSAutomation, error) {
	if c.Spec.DNS.Provider != "cloudflare" || c.Spec.DNS.Automation == nil {
		return nil, errors.New("external-dns rendering requires cloudflare and an explicit spec.dns.automation zone, subdomain, ownership ID and credential Secret reference")
	}
	if problems := validateDNSAutomation(c.Spec.DNS); len(problems) != 0 {
		return nil, &ValidationError{Problems: problems}
	}
	copy := *c.Spec.DNS.Automation
	return &copy, nil
}
