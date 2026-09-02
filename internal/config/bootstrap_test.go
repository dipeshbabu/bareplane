package config

import (
	"strings"
	"testing"
)

func TestValidateBootstrapAcceptsCompleteMachineMapping(t *testing.T) {
	cfg := bootstrapConfig()
	if err := cfg.ValidateBootstrap(); err != nil {
		t.Fatalf("ValidateBootstrap() error = %v", err)
	}
	if got := cfg.Spec.Bootstrap.SSH.EffectivePort(); got != DefaultSSHPort {
		t.Fatalf("EffectivePort() = %d, want %d", got, DefaultSSHPort)
	}
}

func TestValidateRemainsBackwardCompatibleWithoutBootstrap(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if err := cfg.ValidateBootstrap(); err == nil || !strings.Contains(err.Error(), "spec.bootstrap.ssh is required") {
		t.Fatalf("expected stronger bootstrap validation error, got %v", err)
	}
}

func TestValidateBootstrapRequiresEveryDesiredMachine(t *testing.T) {
	cfg := bootstrapConfig()
	delete(cfg.Spec.Bootstrap.SSH.Hosts, "bareplane-worker-2")
	err := cfg.ValidateBootstrap()
	if err == nil || !strings.Contains(err.Error(), `hosts["bareplane-worker-2"] is required`) {
		t.Fatalf("expected missing host error, got %v", err)
	}
}

func TestValidateBootstrapRejectsUnknownMachine(t *testing.T) {
	cfg := bootstrapConfig()
	cfg.Spec.Bootstrap.SSH.Hosts["bareplane-worker-99"] = "10.0.0.99"
	err := cfg.ValidateBootstrap()
	if err == nil || !strings.Contains(err.Error(), "does not match a desired Bareplane machine") {
		t.Fatalf("expected unknown machine error, got %v", err)
	}
}

func TestValidateBootstrapTracksTopologyGrowth(t *testing.T) {
	cfg := bootstrapConfig()
	cfg.Spec.Nodes[1].Count = 3
	err := cfg.ValidateBootstrap()
	if err == nil || !strings.Contains(err.Error(), `hosts["bareplane-worker-3"] is required`) {
		t.Fatalf("expected new machine host requirement, got %v", err)
	}
}

func TestValidateBootstrapAcceptsIPAndDNSHosts(t *testing.T) {
	for _, host := range []string{"10.0.0.10", "2001:db8::10", "node-1.lab.example.com"} {
		cfg := bootstrapConfig()
		cfg.Spec.Bootstrap.SSH.Hosts["bareplane-control-plane-1"] = host
		if err := cfg.ValidateBootstrap(); err != nil {
			t.Fatalf("host %q rejected: %v", host, err)
		}
	}
}

func TestValidateRejectsInvalidBootstrapFields(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Config)
		expected string
	}{
		{"port", func(c *Config) { c.Spec.Bootstrap.SSH.Port = 70000 }, "port must be between"},
		{"user", func(c *Config) { c.Spec.Bootstrap.SSH.User = "Root User" }, "valid lowercase Linux username"},
		{"key path", func(c *Config) { c.Spec.Bootstrap.SSH.PrivateKeyFile = " key" }, "single-line path"},
		{"url host", func(c *Config) { c.Spec.Bootstrap.SSH.Hosts["bareplane-control-plane-1"] = "https://host" }, "IPv4, IPv6, or DNS hostname"},
		{"host with port", func(c *Config) { c.Spec.Bootstrap.SSH.Hosts["bareplane-control-plane-1"] = "host:22" }, "IPv4, IPv6, or DNS hostname"},
		{"user info host", func(c *Config) { c.Spec.Bootstrap.SSH.Hosts["bareplane-control-plane-1"] = "root@host" }, "IPv4, IPv6, or DNS hostname"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := bootstrapConfig()
			test.mutate(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), test.expected) {
				t.Fatalf("expected %q, got %v", test.expected, err)
			}
		})
	}
}

func TestExplicitSSHPort(t *testing.T) {
	cfg := bootstrapConfig()
	cfg.Spec.Bootstrap.SSH.Port = 2222
	if err := cfg.ValidateBootstrap(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Spec.Bootstrap.SSH.EffectivePort(); got != 2222 {
		t.Fatalf("EffectivePort() = %d, want 2222", got)
	}
}

func bootstrapConfig() Config {
	cfg := Default()
	cfg.Spec.Nodes = append(cfg.Spec.Nodes, NodeGroup{
		Name:     "worker",
		Role:     "worker",
		Count:    2,
		CPU:      8,
		MemoryGB: 16,
		DiskGB:   128,
	})
	cfg.Spec.Bootstrap = &BootstrapConfig{
		SSH: &SSHBootstrap{
			User:           "debian",
			PrivateKeyFile: "~/.ssh/id_ed25519",
			Hosts: map[string]string{
				"bareplane-control-plane-1": "10.0.0.10",
				"bareplane-worker-1":        "10.0.0.11",
				"bareplane-worker-2":        "10.0.0.12",
			},
		},
	}
	return cfg
}
