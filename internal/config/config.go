package config

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	APIVersion = "bareplane.io/v1alpha1"
	Kind       = "BareplaneCluster"
)

type Config struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Metadata   Metadata `yaml:"metadata"`
	Spec       Spec     `yaml:"spec"`
}

type Metadata struct {
	Name string `yaml:"name"`
}

type Spec struct {
	Domain    string           `yaml:"domain"`
	Provider  Provider         `yaml:"provider"`
	Nodes     []NodeGroup      `yaml:"nodes"`
	Bootstrap *BootstrapConfig `yaml:"bootstrap,omitempty"`
	Features  Features         `yaml:"features"`
	Profiles  []string         `yaml:"profiles"`
	DNS       DNS              `yaml:"dns"`
	Secrets   Secrets          `yaml:"secrets"`
}

type Provider struct {
	Type     string               `yaml:"type"`
	Endpoint string               `yaml:"endpoint,omitempty"`
	Targets  []string             `yaml:"targets,omitempty"`
	Proxmox  *ProxmoxProvisioning `yaml:"proxmox,omitempty"`
}

type ProxmoxProvisioning struct {
	Bridge           string          `yaml:"bridge"`
	SystemDatastore  string          `yaml:"systemDatastore"`
	CloudImageFileID string          `yaml:"cloudImageFileID"`
	SSH              SSHProvisioning `yaml:"ssh"`
}

type SSHProvisioning struct {
	User          string `yaml:"user"`
	PublicKeyFile string `yaml:"publicKeyFile"`
}

type NodeGroup struct {
	Name     string   `yaml:"name"`
	Role     string   `yaml:"role"`
	Count    int      `yaml:"count"`
	CPU      int      `yaml:"cpu"`
	MemoryGB int      `yaml:"memoryGB"`
	DiskGB   int      `yaml:"diskGB"`
	GPU      bool     `yaml:"gpu,omitempty"`
	Targets  []string `yaml:"targets,omitempty"`
}

type Features struct {
	Observability bool `yaml:"observability"`
	GPU           bool `yaml:"gpu"`
}

type DNS struct {
	Provider string `yaml:"provider"`
}

type Secrets struct {
	Provider string `yaml:"provider"`
}

type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "invalid configuration: " + strings.Join(e.Problems, "; ")
}

func Load(r io.Reader) (Config, error) {
	var cfg Config
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)

	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("decode configuration: multiple YAML documents are not supported")
		}
		return Config{}, fmt.Errorf("decode trailing configuration: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	var problems []string

	if c.APIVersion != APIVersion {
		problems = append(problems, fmt.Sprintf("apiVersion must be %q", APIVersion))
	}
	if c.Kind != Kind {
		problems = append(problems, fmt.Sprintf("kind must be %q", Kind))
	}
	if !validName(c.Metadata.Name) {
		problems = append(problems, "metadata.name must use lowercase letters, numbers, and hyphens")
	}
	if !validDomain(c.Spec.Domain) {
		problems = append(problems, "spec.domain must be a valid DNS name")
	}

	switch c.Spec.Provider.Type {
	case "proxmox":
		if strings.TrimSpace(c.Spec.Provider.Endpoint) == "" {
			problems = append(problems, "spec.provider.endpoint is required for proxmox")
		}
	default:
		problems = append(problems, "spec.provider.type must be proxmox")
	}

	providerTargets := make(map[string]struct{}, len(c.Spec.Provider.Targets))
	for i, target := range c.Spec.Provider.Targets {
		path := fmt.Sprintf("spec.provider.targets[%d]", i)
		if !validName(target) {
			problems = append(problems, path+" must use lowercase letters, numbers, and hyphens")
		}
		if _, exists := providerTargets[target]; exists && target != "" {
			problems = append(problems, path+" is duplicated")
		}
		providerTargets[target] = struct{}{}
	}

	problems = append(problems, validateOptionalProxmoxProvisioning(c.Spec.Provider.Proxmox)...)
	problems = append(problems, validateOptionalBootstrap(c.Spec.Bootstrap)...)

	if len(c.Spec.Nodes) == 0 {
		problems = append(problems, "spec.nodes must contain at least one node group")
	}
	seenNodes := map[string]struct{}{}
	for i, node := range c.Spec.Nodes {
		path := fmt.Sprintf("spec.nodes[%d]", i)
		if !validName(node.Name) {
			problems = append(problems, path+".name must use lowercase letters, numbers, and hyphens")
		}
		if _, exists := seenNodes[node.Name]; exists && node.Name != "" {
			problems = append(problems, path+".name is duplicated")
		}
		seenNodes[node.Name] = struct{}{}
		if node.Role != "control-plane" && node.Role != "worker" {
			problems = append(problems, path+".role must be control-plane or worker")
		}
		if node.Count < 1 {
			problems = append(problems, path+".count must be at least 1")
		}
		if node.CPU < 1 {
			problems = append(problems, path+".cpu must be at least 1")
		}
		if node.MemoryGB < 1 {
			problems = append(problems, path+".memoryGB must be at least 1")
		}
		if node.DiskGB < 10 {
			problems = append(problems, path+".diskGB must be at least 10")
		}

		seenTargets := make(map[string]struct{}, len(node.Targets))
		for j, target := range node.Targets {
			targetPath := fmt.Sprintf("%s.targets[%d]", path, j)
			if !validName(target) {
				problems = append(problems, targetPath+" must use lowercase letters, numbers, and hyphens")
			}
			if _, exists := seenTargets[target]; exists && target != "" {
				problems = append(problems, targetPath+" is duplicated")
			}
			seenTargets[target] = struct{}{}
			if _, exists := providerTargets[target]; !exists && target != "" {
				problems = append(problems, targetPath+" must reference spec.provider.targets")
			}
		}
	}

	allowedProfiles := map[string]struct{}{"minimal": {}, "ai": {}, "data": {}, "full": {}}
	seenProfiles := map[string]struct{}{}
	for i, profile := range c.Spec.Profiles {
		if _, ok := allowedProfiles[profile]; !ok {
			problems = append(problems, fmt.Sprintf("spec.profiles[%d] is unsupported", i))
		}
		if _, exists := seenProfiles[profile]; exists {
			problems = append(problems, fmt.Sprintf("spec.profiles[%d] is duplicated", i))
		}
		seenProfiles[profile] = struct{}{}
	}

	if c.Spec.DNS.Provider != "cloudflare" && c.Spec.DNS.Provider != "manual" {
		problems = append(problems, "spec.dns.provider must be cloudflare or manual")
	}
	if c.Spec.Secrets.Provider != "vault" && c.Spec.Secrets.Provider != "sops" {
		problems = append(problems, "spec.secrets.provider must be vault or sops")
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return &ValidationError{Problems: problems}
}

var namePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func validName(value string) bool {
	return namePattern.MatchString(value)
}

func validDomain(value string) bool {
	if len(value) == 0 || len(value) > 253 || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !validName(label) {
			return false
		}
	}
	return true
}
