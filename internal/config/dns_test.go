package config

import (
	"strings"
	"testing"
)

func dnsAutomationFixture() DNS {
	return DNS{Provider: "cloudflare", Automation: &DNSAutomation{ZoneID: strings.Repeat("a", 32), ZoneName: "example.test",
		Domain: "apps.example.test", SourceNamespace: "apps", OwnerID: strings.Repeat("b", 32),
		TokenSecret: DNSSecretReference{Name: "cloudflare-api", Key: "apiToken"}}}
}

func TestDNSAutomationIsExplicitAndDefaultsToDryRun(t *testing.T) {
	dns := dnsAutomationFixture()
	if problems := validateDNSAutomation(dns); len(problems) != 0 {
		t.Fatal(problems)
	}
	if dns.Automation.EffectiveMode() != "dry-run" || dns.Automation.TokenSecret.EffectiveRevision() != "initial" {
		t.Fatal("DNS automation defaults permit unapproved writes")
	}
	for _, provider := range []string{"manual", "cloudflare"} {
		cfg := Config{Spec: Spec{DNS: DNS{Provider: provider}}}
		if _, err := cfg.RequireDNSAutomation(); err == nil {
			t.Fatal("unconfigured DNS provider produced controller credentials")
		}
	}
	cfg := Config{Spec: Spec{DNS: dns}}
	copy, err := cfg.RequireDNSAutomation()
	if err != nil {
		t.Fatal(err)
	}
	copy.Domain = "changed"
	if cfg.Spec.DNS.Automation.Domain != "apps.example.test" {
		t.Fatal("caller mutated DNS intent")
	}
}

func TestDNSSelectionRemainsExplicitAndCannotBypassDisable(t *testing.T) {
	cfg := gitOpsFixture(t)
	cfg.Spec.Features = Features{}
	cfg.Spec.Profiles = []string{"minimal"}
	cfg.Spec.Secrets.Provider = "sops"
	cfg.Spec.DNS = dnsAutomationFixture()
	resolved, err := cfg.ResolvePlatform()
	if err != nil || resolved.RequireAvailable(false) != nil {
		t.Fatalf("configured DNS capability is unavailable: %v", err)
	}
	if len(resolved.GitOpsComponents()) != 2 {
		t.Fatal("DNS selection enabled unrelated controllers")
	}
	cfg.Spec.Components = &ComponentSelection{Disabled: []string{"external-dns"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Cloudflare provider bypassed the explicit component disable")
	}
}

func TestDNSAutomationRejectsBroadAmbiguousAndSecretBearingInputs(t *testing.T) {
	for _, mutate := range []func(*DNS){
		func(d *DNS) { d.Provider = "manual" },
		func(d *DNS) { d.Automation.ZoneID = "PRIVATE-SENTINEL" },
		func(d *DNS) { d.Automation.OwnerID = "shared-default" },
		func(d *DNS) { d.Automation.ZoneName = "com" },
		func(d *DNS) { d.Automation.ZoneName = "192.0.2.10" },
		func(d *DNS) { d.Automation.Domain = "example.test" },
		func(d *DNS) { d.Automation.Domain = "otherexample.test" },
		func(d *DNS) { d.Automation.Domain = "*.example.test" },
		func(d *DNS) { d.Automation.Domain = "Apps.example.test" },
		func(d *DNS) { d.Automation.Domain = "apps.example.test." },
		func(d *DNS) { d.Automation.SourceNamespace = "kube-system" },
		func(d *DNS) { d.Automation.SourceNamespace = "argocd" },
		func(d *DNS) { d.Automation.SourceNamespace = "external-dns" },
		func(d *DNS) { d.Automation.Mode = "sync" },
		func(d *DNS) { d.Automation.TokenSecret.Key = "../PRIVATE-SENTINEL" },
		func(d *DNS) { d.Automation.TokenSecret.Name = "" },
		func(d *DNS) { d.Automation.TokenSecret.Revision = "PRIVATE-SENTINEL" },
	} {
		dns := dnsAutomationFixture()
		mutate(&dns)
		problems := validateDNSAutomation(dns)
		if len(problems) == 0 || strings.Contains(strings.Join(problems, " "), "PRIVATE-SENTINEL") {
			t.Fatalf("unsafe DNS automation accepted/exposed: %v", problems)
		}
	}
}

func TestDNSSchemaNeverAcceptsTokenValuesOrCustomAPIEndpoints(t *testing.T) {
	for _, field := range []string{"apiToken", "apiKey", "password", "baseURL"} {
		input := strings.Replace(validConfig, "  dns:\n", "  dns:\n    automation:\n      "+field+": PRIVATE-SENTINEL\n", 1)
		if _, err := Load(strings.NewReader(input)); err == nil || strings.Contains(err.Error(), "PRIVATE-SENTINEL") {
			t.Fatalf("DNS plaintext/provider endpoint accepted/exposed: %v", err)
		}
	}
}
