package config

import "fmt"

type CertificateConfig struct {
	Issuers []CertificateIssuer `yaml:"issuers,omitempty"`
}

type CertificateIssuer struct {
	Name       string                `yaml:"name"`
	Type       string                `yaml:"type"`
	SecretName string                `yaml:"secretName,omitempty"`
	ACME       *ACMEIssuerReferences `yaml:"acme,omitempty"`
}

// ACME credential references define the extension boundary. The initial core
// component refuses ACME until DNS/ingress prerequisites are implemented.
type ACMEIssuerReferences struct {
	Server                  string `yaml:"server"`
	Email                   string `yaml:"email"`
	AccountKeySecretName    string `yaml:"accountKeySecretName"`
	DNSCredentialSecretName string `yaml:"dnsCredentialSecretName,omitempty"`
	IngressClass            string `yaml:"ingressClass,omitempty"`
}

func validateCertificates(settings *CertificateConfig) []string {
	if settings == nil {
		return nil
	}
	if len(settings.Issuers) > 32 {
		return []string{"spec.certificates.issuers exceeds the limit of 32"}
	}
	var problems []string
	seen := map[string]bool{}
	for index, issuer := range settings.Issuers {
		prefix := fmt.Sprintf("spec.certificates.issuers[%d]", index)
		if !validName(issuer.Name) || seen[issuer.Name] {
			problems = append(problems, prefix+".name must be a unique lowercase DNS label")
		}
		seen[issuer.Name] = true
		if issuer.ACME != nil || issuer.Type == "acme" {
			problems = append(problems, prefix+": ACME requires separately implemented DNS/ingress and credential integration; only self-signed and ca are available")
		}
		switch issuer.Type {
		case "self-signed":
			if issuer.SecretName != "" {
				problems = append(problems, prefix+": self-signed issuers do not accept a CA secret reference")
			}
		case "ca":
			if !validName(issuer.SecretName) || issuer.SecretName == "cert-manager-webhook-ca" {
				problems = append(problems, prefix+".secretName must reference a dedicated CA Secret in cert-manager, not the webhook CA")
			}
		case "acme":
		default:
			problems = append(problems, prefix+".type must be self-signed or ca")
		}
	}
	return problems
}
