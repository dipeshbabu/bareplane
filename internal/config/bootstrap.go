package config

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
)

const DefaultSSHPort = 22

var dnsHostLabelPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func (s SSHBootstrap) EffectivePort() int {
	if s.Port == 0 {
		return DefaultSSHPort
	}
	return s.Port
}

func (c Config) ValidateBootstrap() error {
	if err := c.Validate(); err != nil {
		return err
	}

	var problems []string
	if c.Spec.Bootstrap == nil || c.Spec.Bootstrap.SSH == nil {
		problems = append(problems, "spec.bootstrap.ssh is required for bootstrap")
	} else {
		ssh := c.Spec.Bootstrap.SSH
		if strings.TrimSpace(ssh.User) == "" {
			problems = append(problems, "spec.bootstrap.ssh.user is required for bootstrap")
		}
		if strings.TrimSpace(ssh.PrivateKeyFile) == "" {
			problems = append(problems, "spec.bootstrap.ssh.privateKeyFile is required for bootstrap")
		}

		desired, err := desiredBootstrapMachineNames(c)
		if err != nil {
			problems = append(problems, err.Error())
		} else {
			desiredSet := make(map[string]struct{}, len(desired))
			for _, name := range desired {
				desiredSet[name] = struct{}{}
				if strings.TrimSpace(ssh.Hosts[name]) == "" {
					problems = append(problems, fmt.Sprintf("spec.bootstrap.ssh.hosts[%q] is required for bootstrap", name))
				}
			}
			for name := range ssh.Hosts {
				if _, ok := desiredSet[name]; !ok {
					problems = append(problems, fmt.Sprintf("spec.bootstrap.ssh.hosts[%q] does not match a desired Bareplane machine", name))
				}
			}
		}
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return &ValidationError{Problems: problems}
}

func validateOptionalBootstrap(settings *BootstrapConfig) []string {
	if settings == nil || settings.SSH == nil {
		return nil
	}
	ssh := settings.SSH
	var problems []string
	if ssh.User != "" && !sshUserPattern.MatchString(ssh.User) {
		problems = append(problems, "spec.bootstrap.ssh.user must be a valid lowercase Linux username")
	}
	if ssh.PrivateKeyFile != "" && !validPathValue(ssh.PrivateKeyFile) {
		problems = append(problems, "spec.bootstrap.ssh.privateKeyFile must be a non-empty single-line path")
	}
	if ssh.Port < 0 || ssh.Port > 65535 {
		problems = append(problems, "spec.bootstrap.ssh.port must be between 1 and 65535 when set")
	}
	for machine, host := range ssh.Hosts {
		if !validName(machine) {
			problems = append(problems, fmt.Sprintf("spec.bootstrap.ssh.hosts[%q] machine name is invalid", machine))
		}
		if !validBootstrapHost(host) {
			problems = append(problems, fmt.Sprintf("spec.bootstrap.ssh.hosts[%q] must be an IPv4, IPv6, or DNS hostname without a port", machine))
		}
	}
	return problems
}

func validBootstrapHost(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\x00\r\n\t /@") {
		return false
	}
	if net.ParseIP(value) != nil {
		return true
	}
	if strings.Contains(value, ":") || len(value) > 253 || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !dnsHostLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

func desiredBootstrapMachineNames(c Config) ([]string, error) {
	groups := append([]NodeGroup(nil), c.Spec.Nodes...)
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })

	total := 0
	for _, group := range groups {
		if group.Count < 1 {
			return nil, fmt.Errorf("node group %q count must be at least 1", group.Name)
		}
		if total > 10000-group.Count {
			return nil, errors.New("bootstrap topology exceeds maximum of 10000 machines")
		}
		total += group.Count
	}

	names := make([]string, 0, total)
	for _, group := range groups {
		for ordinal := 1; ordinal <= group.Count; ordinal++ {
			name := fmt.Sprintf("%s-%s-%d", c.Metadata.Name, group.Name, ordinal)
			if len(name) > 63 {
				return nil, fmt.Errorf("generated machine name %q exceeds 63 characters", name)
			}
			names = append(names, name)
		}
	}
	return names, nil
}
