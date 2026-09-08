package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestCertificateIssuersAreExplicitAndActivateOnlyTheirController(t *testing.T) {
	cfg := gitOpsFixture(t)
	cfg.Spec.Features = Features{}
	cfg.Spec.Profiles = []string{"minimal"}
	cfg.Spec.DNS.Provider = "manual"
	cfg.Spec.Certificates = &CertificateConfig{Issuers: []CertificateIssuer{{Name: "lab", Type: "self-signed"}, {Name: "internal", Type: "ca", SecretName: "dedicated-ca"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	result, err := cfg.ResolvePlatform()
	if err != nil || result.RequireAvailable(false) != nil {
		t.Fatalf("certificate capability unavailable: %v", err)
	}
	if len(result.GitOpsComponents()) != 2 {
		t.Fatal("unexpected controller selection")
	}
	cfg.Spec.Components = &ComponentSelection{Disabled: []string{"cert-manager"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("issuer contract bypassed controller disable")
	}
}

func TestCertificateConfigurationRejectsUnsafeOrUnavailableIssuerModes(t *testing.T) {
	for _, issuer := range []CertificateIssuer{
		{Name: "../unsafe", Type: "self-signed"}, {Name: "lab", Type: "unknown"},
		{Name: "lab", Type: "self-signed", SecretName: "unexpected"}, {Name: "lab", Type: "ca"},
		{Name: "lab", Type: "ca", SecretName: "cert-manager-webhook-ca"},
		{Name: "lab", Type: "acme", ACME: &ACMEIssuerReferences{Server: "https://example.com", AccountKeySecretName: "account"}},
		{Name: "lab", Type: "self-signed", ACME: &ACMEIssuerReferences{}},
		{Name: "lab", Type: "ca", SecretName: "CA-PRIVATE-SENTINEL"},
	} {
		cfg := gitOpsFixture(t)
		cfg.Spec.Certificates = &CertificateConfig{Issuers: []CertificateIssuer{issuer}}
		if err := cfg.Validate(); err == nil || strings.Contains(err.Error(), "PRIVATE-SENTINEL") {
			t.Fatalf("unsafe issuer accepted/exposed: %v", err)
		}
	}
	cfg := gitOpsFixture(t)
	cfg.Spec.Certificates = &CertificateConfig{Issuers: []CertificateIssuer{{Name: "same", Type: "self-signed"}, {Name: "same", Type: "self-signed"}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("duplicate issuers accepted")
	}
	cfg.Spec.Certificates.Issuers = make([]CertificateIssuer, 33)
	if err := cfg.Validate(); err == nil {
		t.Fatal("unbounded issuers accepted")
	}
}

func TestCertificateSchemaAcceptsBoundsAndRefusesPlaintextCredentials(t *testing.T) {
	settings := &CertificateConfig{}
	for index := 0; index < 32; index++ {
		settings.Issuers = append(settings.Issuers, CertificateIssuer{Name: fmt.Sprintf("issuer-%d", index), Type: "self-signed"})
	}
	if problems := validateCertificates(settings); len(problems) != 0 {
		t.Fatalf("valid issuer boundary rejected: %v", problems)
	}
	for _, field := range []string{"privateKey", "token", "password", "secretData"} {
		input := strings.Replace(validConfig, "spec:\n", "spec:\n  certificates:\n    issuers:\n      - name: lab\n        type: self-signed\n        "+field+": PRIVATE-SENTINEL\n", 1)
		if _, err := Load(strings.NewReader(input)); err == nil || strings.Contains(err.Error(), "PRIVATE-SENTINEL") {
			t.Fatalf("plaintext issuer field accepted/exposed: %v", err)
		}
	}
}
