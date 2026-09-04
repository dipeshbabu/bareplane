package config

import (
	"bytes"
	"errors"
	"sort"
	"strings"
	"testing"
)

func TestValidateKubernetesBootstrapAcceptsSupportedConfiguration(t *testing.T) {
	cfg := completeKubernetesConfig()
	if err := cfg.ValidateKubernetesBootstrap(); err != nil {
		t.Fatalf("ValidateKubernetesBootstrap() error = %v", err)
	}
}

func TestValidateKubernetesBootstrapAcceptsIPv6Configuration(t *testing.T) {
	cfg := completeKubernetesConfig()
	cfg.Spec.Kubernetes.APIVIP = "2001:db8::100"
	cfg.Spec.Kubernetes.PodCIDR = "fd00:10::/64"
	cfg.Spec.Kubernetes.ServiceCIDR = "fd00:20::/112"
	if err := cfg.ValidateKubernetesBootstrap(); err != nil {
		t.Fatalf("ValidateKubernetesBootstrap() error = %v", err)
	}
}

func TestValidateRemainsBackwardCompatibleWithoutKubernetes(t *testing.T) {
	cfg := bootstrapConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	err := cfg.ValidateKubernetesBootstrap()
	if err == nil || !strings.Contains(err.Error(), "spec.kubernetes is required for Kubernetes bootstrap") {
		t.Fatalf("expected stronger Kubernetes bootstrap validation error, got %v", err)
	}
}

func TestValidateKubernetesBootstrapBuildsOnSSHBootstrapValidation(t *testing.T) {
	cfg := completeKubernetesConfig()
	cfg.Spec.Bootstrap = nil
	err := cfg.ValidateKubernetesBootstrap()
	if err == nil || !strings.Contains(err.Error(), "spec.bootstrap.ssh is required for bootstrap") {
		t.Fatalf("expected SSH bootstrap validation error, got %v", err)
	}
}

func TestValidateKubernetesBootstrapRequiresEveryField(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		clear func(*KubernetesConfig)
	}{
		{name: "version", path: "version", clear: func(k *KubernetesConfig) { k.Version = "" }},
		{name: "API VIP", path: "apiVIP", clear: func(k *KubernetesConfig) { k.APIVIP = "" }},
		{name: "pod CIDR", path: "podCIDR", clear: func(k *KubernetesConfig) { k.PodCIDR = "" }},
		{name: "service CIDR", path: "serviceCIDR", clear: func(k *KubernetesConfig) { k.ServiceCIDR = "" }},
		{name: "kube-vip version", path: "kubeVIPVersion", clear: func(k *KubernetesConfig) { k.KubeVIPVersion = "" }},
		{name: "Cilium version", path: "ciliumVersion", clear: func(k *KubernetesConfig) { k.CiliumVersion = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := completeKubernetesConfig()
			test.clear(cfg.Spec.Kubernetes)
			err := cfg.ValidateKubernetesBootstrap()
			expected := "spec.kubernetes." + test.path + " is required for Kubernetes bootstrap"
			if err == nil || !strings.Contains(err.Error(), expected) {
				t.Fatalf("expected %q, got %v", expected, err)
			}
		})
	}
}

func TestValidateKubernetesBootstrapRequiresKubeProxyReplacement(t *testing.T) {
	cfg := completeKubernetesConfig()
	cfg.Spec.Kubernetes.KubeProxyReplacement = false
	err := cfg.ValidateKubernetesBootstrap()
	if err == nil || !strings.Contains(err.Error(), "kubeProxyReplacement must be true") {
		t.Fatalf("expected kube-proxy replacement error, got %v", err)
	}
}

func TestValidateKubernetesBootstrapRequiresControlPlane(t *testing.T) {
	cfg := completeKubernetesConfig()
	for i := range cfg.Spec.Nodes {
		cfg.Spec.Nodes[i].Role = "worker"
	}
	err := cfg.ValidateKubernetesBootstrap()
	if err == nil || !strings.Contains(err.Error(), "at least one control-plane machine") {
		t.Fatalf("expected control-plane error, got %v", err)
	}
}

func TestValidateKubernetesBootstrapReportsSortedRequirements(t *testing.T) {
	cfg := bootstrapConfig()
	cfg.Spec.Kubernetes = &KubernetesConfig{}
	err := cfg.ValidateKubernetesBootstrap()
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("expected ValidationError, got %v", err)
	}
	if !sort.StringsAreSorted(validationErr.Problems) {
		t.Fatalf("problems are not sorted: %#v", validationErr.Problems)
	}
}

func TestValidateRejectsMalformedKubernetesVersions(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		mutate func(*KubernetesConfig)
	}{
		{name: "Kubernetes", path: "spec.kubernetes.version", mutate: func(k *KubernetesConfig) { k.Version = "v1.36.4" }},
		{name: "kube-vip", path: "spec.kubernetes.kubeVIPVersion", mutate: func(k *KubernetesConfig) { k.KubeVIPVersion = "1.2" }},
		{name: "Cilium", path: "spec.kubernetes.ciliumVersion", mutate: func(k *KubernetesConfig) { k.CiliumVersion = "1.20.1-rc.1" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := completeKubernetesConfig()
			test.mutate(cfg.Spec.Kubernetes)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), test.path+" must be an exact semantic version") {
				t.Fatalf("expected field-specific semantic version error, got %v", err)
			}
		})
	}
}

func TestValidateRejectsUnsupportedKubernetesVersionRanges(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		mutate func(*KubernetesConfig)
	}{
		{name: "old Kubernetes", path: "spec.kubernetes.version", mutate: func(k *KubernetesConfig) { k.Version = "1.34.11" }},
		{name: "new Kubernetes", path: "spec.kubernetes.version", mutate: func(k *KubernetesConfig) { k.Version = "1.37.0" }},
		{name: "old kube-vip", path: "spec.kubernetes.kubeVIPVersion", mutate: func(k *KubernetesConfig) { k.KubeVIPVersion = "1.1.2" }},
		{name: "new kube-vip", path: "spec.kubernetes.kubeVIPVersion", mutate: func(k *KubernetesConfig) { k.KubeVIPVersion = "1.3.0" }},
		{name: "old Cilium", path: "spec.kubernetes.ciliumVersion", mutate: func(k *KubernetesConfig) { k.CiliumVersion = "1.19.6" }},
		{name: "new Cilium", path: "spec.kubernetes.ciliumVersion", mutate: func(k *KubernetesConfig) { k.CiliumVersion = "1.21.0" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := completeKubernetesConfig()
			test.mutate(cfg.Spec.Kubernetes)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), test.path+" must be >=") {
				t.Fatalf("expected field-specific supported range error, got %v", err)
			}
		})
	}
}

func TestSupportedVersionRangesIncludeMinimumVersions(t *testing.T) {
	cfg := completeKubernetesConfig()
	cfg.Spec.Kubernetes.Version = minimumKubernetesVersion
	cfg.Spec.Kubernetes.KubeVIPVersion = minimumKubeVIPVersion
	cfg.Spec.Kubernetes.CiliumVersion = minimumCiliumVersion
	if err := cfg.ValidateKubernetesBootstrap(); err != nil {
		t.Fatalf("minimum supported versions were rejected: %v", err)
	}
}

func TestParseSemanticVersionRequiresCanonicalExactVersion(t *testing.T) {
	for _, value := range []string{"0.0.0", "1.35.0", "1.36.4294967295", "4294967295.0.0"} {
		if _, ok := parseSemanticVersion(value); !ok {
			t.Fatalf("valid exact version %q was rejected", value)
		}
	}
	for _, value := range []string{
		"", "1", "1.2", "1.2.3.4", "v1.2.3", "+1.2.3", "1.-2.3", "1.2.3-rc.1",
		"1.2.3+build", "01.2.3", "1.02.3", "1.2.03", "4294967296.0.0", "latest",
	} {
		if _, ok := parseSemanticVersion(value); ok {
			t.Fatalf("non-canonical exact version %q was accepted", value)
		}
	}
}

func TestValidateRejectsInvalidAPIVIP(t *testing.T) {
	for _, value := range []string{
		"api.example.com", "10.0.0.1/24", "0.0.0.0", "127.0.0.1", "169.254.1.1",
		"224.0.0.1", "255.255.255.255", "::", "::1", "::ffff:192.168.1.100", "fe80::1", "fe80::1%eth0", "ff02::1",
	} {
		t.Run(value, func(t *testing.T) {
			cfg := completeKubernetesConfig()
			cfg.Spec.Kubernetes.APIVIP = value
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), "spec.kubernetes.apiVIP must be a unicast IPv4 or IPv6 address") {
				t.Fatalf("expected invalid API VIP error for %q, got %v", value, err)
			}
		})
	}
}

func TestValidateRejectsInvalidKubernetesCIDRs(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		mutate func(*KubernetesConfig)
	}{
		{name: "malformed pod CIDR", path: "podCIDR", mutate: func(k *KubernetesConfig) { k.PodCIDR = "not-a-cidr" }},
		{name: "pod host bits", path: "podCIDR", mutate: func(k *KubernetesConfig) { k.PodCIDR = "10.244.1.0/16" }},
		{name: "pod prefix too long", path: "podCIDR", mutate: func(k *KubernetesConfig) { k.PodCIDR = "10.244.0.0/33" }},
		{name: "mapped pod CIDR", path: "podCIDR", mutate: func(k *KubernetesConfig) { k.PodCIDR = "::ffff:10.244.0.0/112" }},
		{name: "malformed service CIDR", path: "serviceCIDR", mutate: func(k *KubernetesConfig) { k.ServiceCIDR = "10.96.0.0" }},
		{name: "service host bits", path: "serviceCIDR", mutate: func(k *KubernetesConfig) { k.ServiceCIDR = "fd00:20::1/112" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := completeKubernetesConfig()
			test.mutate(cfg.Spec.Kubernetes)
			err := cfg.Validate()
			expected := "spec.kubernetes." + test.path + " must be a canonical IPv4 or IPv6 CIDR"
			if err == nil || !strings.Contains(err.Error(), expected) {
				t.Fatalf("expected %q, got %v", expected, err)
			}
		})
	}
}

func TestValidateRejectsPodAndServiceCIDROverlap(t *testing.T) {
	tests := []struct {
		name        string
		podCIDR     string
		serviceCIDR string
	}{
		{name: "equal", podCIDR: "10.0.0.0/16", serviceCIDR: "10.0.0.0/16"},
		{name: "pod contains service", podCIDR: "10.0.0.0/8", serviceCIDR: "10.96.0.0/12"},
		{name: "service contains pod", podCIDR: "10.244.0.0/16", serviceCIDR: "10.0.0.0/8"},
		{name: "IPv6 nesting", podCIDR: "fd00::/48", serviceCIDR: "fd00:0:0:1::/64"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := completeKubernetesConfig()
			cfg.Spec.Kubernetes.PodCIDR = test.podCIDR
			cfg.Spec.Kubernetes.ServiceCIDR = test.serviceCIDR
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), "podCIDR must not overlap spec.kubernetes.serviceCIDR") {
				t.Fatalf("expected overlap error, got %v", err)
			}
		})
	}
}

func TestValidateRejectsMixedCIDRAddressFamilies(t *testing.T) {
	cfg := completeKubernetesConfig()
	cfg.Spec.Kubernetes.ServiceCIDR = "fd00:20::/112"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "podCIDR and spec.kubernetes.serviceCIDR must use the same address family") {
		t.Fatalf("expected address family error, got %v", err)
	}
}

func TestValidateRejectsAPIVIPInsideClusterCIDRs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*KubernetesConfig)
		path   string
	}{
		{name: "pod CIDR", path: "podCIDR", mutate: func(k *KubernetesConfig) { k.APIVIP = "10.244.1.10" }},
		{name: "service CIDR", path: "serviceCIDR", mutate: func(k *KubernetesConfig) { k.APIVIP = "10.96.1.10" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := completeKubernetesConfig()
			test.mutate(cfg.Spec.Kubernetes)
			err := cfg.Validate()
			expected := "spec.kubernetes.apiVIP must not be inside spec.kubernetes." + test.path
			if err == nil || !strings.Contains(err.Error(), expected) {
				t.Fatalf("expected %q, got %v", expected, err)
			}
		})
	}
}

func TestKubernetesConfigRoundTripAndStrictFields(t *testing.T) {
	cfg := completeKubernetesConfig()
	var encoded bytes.Buffer
	if err := Encode(&encoded, cfg); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	loaded, err := Load(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Spec.Kubernetes == nil || *loaded.Spec.Kubernetes != *cfg.Spec.Kubernetes {
		t.Fatalf("Kubernetes settings changed during round trip: got %#v", loaded.Spec.Kubernetes)
	}

	withUnknownField := strings.Replace(encoded.String(), "  kubernetes:\n", "  kubernetes:\n    unexpected: true\n", 1)
	if withUnknownField == encoded.String() {
		t.Fatal("encoded configuration did not contain spec.kubernetes")
	}
	_, err = Load(strings.NewReader(withUnknownField))
	if err == nil || !strings.Contains(err.Error(), "field unexpected not found") {
		t.Fatalf("expected strict Kubernetes field error, got %v", err)
	}
}

func completeKubernetesConfig() Config {
	cfg := bootstrapConfig()
	cfg.Spec.Kubernetes = &KubernetesConfig{
		Version:              "1.36.4",
		APIVIP:               "192.168.1.100",
		PodCIDR:              "10.244.0.0/16",
		ServiceCIDR:          "10.96.0.0/12",
		KubeVIPVersion:       "1.2.3",
		CiliumVersion:        "1.20.1",
		KubeProxyReplacement: true,
	}
	return cfg
}
