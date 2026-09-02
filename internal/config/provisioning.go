package config

import (
	"regexp"
	"sort"
	"strings"
)

var (
	proxmoxIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	proxmoxBridgePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,31}$`)
	sshUserPattern           = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

func (c Config) ValidateProvisioning() error {
	if err := c.Validate(); err != nil {
		return err
	}

	var problems []string
	if c.Spec.Provider.Type != "proxmox" {
		problems = append(problems, "spec.provider.type does not support provisioning")
	}
	if len(c.Spec.Provider.Targets) == 0 {
		problems = append(problems, "spec.provider.targets must contain at least one target for provisioning")
	}

	settings := c.Spec.Provider.Proxmox
	if settings == nil {
		problems = append(problems, "spec.provider.proxmox is required for provisioning")
	} else {
		if strings.TrimSpace(settings.Bridge) == "" {
			problems = append(problems, "spec.provider.proxmox.bridge is required for provisioning")
		}
		if strings.TrimSpace(settings.SystemDatastore) == "" {
			problems = append(problems, "spec.provider.proxmox.systemDatastore is required for provisioning")
		}
		if strings.TrimSpace(settings.CloudImageFileID) == "" {
			problems = append(problems, "spec.provider.proxmox.cloudImageFileID is required for provisioning")
		}
		if strings.TrimSpace(settings.SSH.User) == "" {
			problems = append(problems, "spec.provider.proxmox.ssh.user is required for provisioning")
		}
		if strings.TrimSpace(settings.SSH.PublicKeyFile) == "" {
			problems = append(problems, "spec.provider.proxmox.ssh.publicKeyFile is required for provisioning")
		}
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return &ValidationError{Problems: problems}
}

func validateOptionalProxmoxProvisioning(settings *ProxmoxProvisioning) []string {
	if settings == nil {
		return nil
	}

	var problems []string
	if settings.Bridge != "" && !proxmoxBridgePattern.MatchString(settings.Bridge) {
		problems = append(problems, "spec.provider.proxmox.bridge must be a valid Proxmox bridge name")
	}
	if settings.SystemDatastore != "" && !proxmoxIdentifierPattern.MatchString(settings.SystemDatastore) {
		problems = append(problems, "spec.provider.proxmox.systemDatastore must be a valid Proxmox datastore ID")
	}
	if settings.CloudImageFileID != "" && !validCloudImageFileID(settings.CloudImageFileID) {
		problems = append(problems, "spec.provider.proxmox.cloudImageFileID must use <datastore>:import/<file> or <datastore>:iso/<file>")
	}
	if settings.SSH.User != "" && !sshUserPattern.MatchString(settings.SSH.User) {
		problems = append(problems, "spec.provider.proxmox.ssh.user must be a valid lowercase Linux username")
	}
	if settings.SSH.PublicKeyFile != "" && !validPathValue(settings.SSH.PublicKeyFile) {
		problems = append(problems, "spec.provider.proxmox.ssh.publicKeyFile must be a non-empty single-line path")
	}
	return problems
}

func validCloudImageFileID(value string) bool {
	if value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n\t ") {
		return false
	}
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 || !proxmoxIdentifierPattern.MatchString(parts[0]) {
		return false
	}

	path := parts[1]
	var file string
	switch {
	case strings.HasPrefix(path, "import/"):
		file = strings.TrimPrefix(path, "import/")
	case strings.HasPrefix(path, "iso/"):
		file = strings.TrimPrefix(path, "iso/")
	default:
		return false
	}
	return file != "" && file != "." && file != ".." && !strings.ContainsAny(file, "/\\")
}

func validPathValue(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && trimmed == value && !strings.ContainsAny(value, "\x00\r\n")
}
