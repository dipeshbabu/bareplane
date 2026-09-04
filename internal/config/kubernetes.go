package config

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

const (
	minimumKubernetesVersion = "1.35.0"
	maximumKubernetesVersion = "1.37.0"
	minimumKubeVIPVersion    = "1.2.0"
	maximumKubeVIPVersion    = "1.3.0"
	minimumCiliumVersion     = "1.20.0"
	maximumCiliumVersion     = "1.21.0"
)

type KubernetesConfig struct {
	Version              string `yaml:"version,omitempty"`
	APIVIP               string `yaml:"apiVIP,omitempty"`
	PodCIDR              string `yaml:"podCIDR,omitempty"`
	ServiceCIDR          string `yaml:"serviceCIDR,omitempty"`
	KubeVIPVersion       string `yaml:"kubeVIPVersion,omitempty"`
	CiliumVersion        string `yaml:"ciliumVersion,omitempty"`
	KubeProxyReplacement bool   `yaml:"kubeProxyReplacement,omitempty"`
}

type semanticVersion struct {
	major uint32
	minor uint32
	patch uint32
}

type semanticVersionRange struct {
	minimum      semanticVersion
	maximum      semanticVersion
	minimumLabel string
	maximumLabel string
}

var (
	supportedKubernetesVersions = mustSemanticVersionRange(minimumKubernetesVersion, maximumKubernetesVersion)
	supportedKubeVIPVersions    = mustSemanticVersionRange(minimumKubeVIPVersion, maximumKubeVIPVersion)
	supportedCiliumVersions     = mustSemanticVersionRange(minimumCiliumVersion, maximumCiliumVersion)
)

func (c Config) ValidateKubernetesBootstrap() error {
	if err := c.ValidateBootstrap(); err != nil {
		return err
	}

	var problems []string
	settings := c.Spec.Kubernetes
	if settings == nil {
		problems = append(problems, "spec.kubernetes is required for Kubernetes bootstrap")
	} else {
		for _, required := range []struct {
			path  string
			value string
		}{
			{path: "version", value: settings.Version},
			{path: "apiVIP", value: settings.APIVIP},
			{path: "podCIDR", value: settings.PodCIDR},
			{path: "serviceCIDR", value: settings.ServiceCIDR},
			{path: "kubeVIPVersion", value: settings.KubeVIPVersion},
			{path: "ciliumVersion", value: settings.CiliumVersion},
		} {
			if strings.TrimSpace(required.value) == "" {
				problems = append(problems, "spec.kubernetes."+required.path+" is required for Kubernetes bootstrap")
			}
		}
		if !settings.KubeProxyReplacement {
			problems = append(problems, "spec.kubernetes.kubeProxyReplacement must be true; Cilium replaces kube-proxy in v0.1")
		}
	}

	if !hasControlPlane(c.Spec.Nodes) {
		problems = append(problems, "spec.nodes must contain at least one control-plane machine for Kubernetes bootstrap")
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return &ValidationError{Problems: problems}
}

func validateOptionalKubernetes(settings *KubernetesConfig) []string {
	if settings == nil {
		return nil
	}

	var problems []string
	problems = append(problems, validateVersionInRange("spec.kubernetes.version", settings.Version, supportedKubernetesVersions)...)
	problems = append(problems, validateVersionInRange("spec.kubernetes.kubeVIPVersion", settings.KubeVIPVersion, supportedKubeVIPVersions)...)
	problems = append(problems, validateVersionInRange("spec.kubernetes.ciliumVersion", settings.CiliumVersion, supportedCiliumVersions)...)

	apiVIP, apiVIPValid := parseAPIVIP(settings.APIVIP)
	if settings.APIVIP != "" && !apiVIPValid {
		problems = append(problems, "spec.kubernetes.apiVIP must be a unicast IPv4 or IPv6 address")
	}

	podCIDR, podCIDRValid := parseCanonicalPrefix(settings.PodCIDR)
	if settings.PodCIDR != "" && !podCIDRValid {
		problems = append(problems, "spec.kubernetes.podCIDR must be a canonical IPv4 or IPv6 CIDR")
	}
	serviceCIDR, serviceCIDRValid := parseCanonicalPrefix(settings.ServiceCIDR)
	if settings.ServiceCIDR != "" && !serviceCIDRValid {
		problems = append(problems, "spec.kubernetes.serviceCIDR must be a canonical IPv4 or IPv6 CIDR")
	}

	if podCIDRValid && serviceCIDRValid {
		if podCIDR.Addr().BitLen() != serviceCIDR.Addr().BitLen() {
			problems = append(problems, "spec.kubernetes.podCIDR and spec.kubernetes.serviceCIDR must use the same address family")
		} else if prefixesOverlap(podCIDR, serviceCIDR) {
			problems = append(problems, "spec.kubernetes.podCIDR must not overlap spec.kubernetes.serviceCIDR")
		}
	}
	if apiVIPValid && podCIDRValid && podCIDR.Contains(apiVIP) {
		problems = append(problems, "spec.kubernetes.apiVIP must not be inside spec.kubernetes.podCIDR")
	}
	if apiVIPValid && serviceCIDRValid && serviceCIDR.Contains(apiVIP) {
		problems = append(problems, "spec.kubernetes.apiVIP must not be inside spec.kubernetes.serviceCIDR")
	}

	return problems
}

func validateVersionInRange(path, value string, supported semanticVersionRange) []string {
	if value == "" {
		return nil
	}
	version, ok := parseSemanticVersion(value)
	if !ok {
		return []string{path + " must be an exact semantic version in MAJOR.MINOR.PATCH form"}
	}
	if !supported.contains(version) {
		return []string{fmt.Sprintf("%s must be >= %s and < %s", path, supported.minimumLabel, supported.maximumLabel)}
	}
	return nil
}

func parseSemanticVersion(value string) (semanticVersion, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return semanticVersion{}, false
	}

	components := [3]uint32{}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return semanticVersion{}, false
		}
		for j := 0; j < len(part); j++ {
			digit := part[j]
			if digit < '0' || digit > '9' {
				return semanticVersion{}, false
			}
		}
		component, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return semanticVersion{}, false
		}
		components[i] = uint32(component)
	}
	return semanticVersion{major: components[0], minor: components[1], patch: components[2]}, true
}

func mustSemanticVersionRange(minimum, maximum string) semanticVersionRange {
	min, minOK := parseSemanticVersion(minimum)
	max, maxOK := parseSemanticVersion(maximum)
	if !minOK || !maxOK || compareSemanticVersions(min, max) >= 0 {
		panic("invalid internal semantic version range")
	}
	return semanticVersionRange{
		minimum:      min,
		maximum:      max,
		minimumLabel: minimum,
		maximumLabel: maximum,
	}
}

func (r semanticVersionRange) contains(version semanticVersion) bool {
	return compareSemanticVersions(version, r.minimum) >= 0 && compareSemanticVersions(version, r.maximum) < 0
}

func compareSemanticVersions(left, right semanticVersion) int {
	leftParts := [...]uint32{left.major, left.minor, left.patch}
	rightParts := [...]uint32{right.major, right.minor, right.patch}
	for i := range leftParts {
		if leftParts[i] < rightParts[i] {
			return -1
		}
		if leftParts[i] > rightParts[i] {
			return 1
		}
	}
	return 0
}

func parseAPIVIP(value string) (netip.Addr, bool) {
	address, err := netip.ParseAddr(value)
	if err != nil || address.Zone() != "" || address.Is4In6() || !address.IsGlobalUnicast() {
		return netip.Addr{}, false
	}
	return address, true
}

func parseCanonicalPrefix(value string) (netip.Prefix, bool) {
	prefix, err := netip.ParsePrefix(value)
	if err != nil || prefix.Addr().Is4In6() || prefix != prefix.Masked() {
		return netip.Prefix{}, false
	}
	return prefix, true
}

func prefixesOverlap(left, right netip.Prefix) bool {
	return left.Contains(right.Addr()) || right.Contains(left.Addr())
}

func hasControlPlane(groups []NodeGroup) bool {
	for _, group := range groups {
		if group.Role == "control-plane" && group.Count > 0 {
			return true
		}
	}
	return false
}
