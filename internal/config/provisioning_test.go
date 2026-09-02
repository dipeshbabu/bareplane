package config

import (
	"strings"
	"testing"
)

func TestReadOnlyConfigurationDoesNotRequireProvisioningSettings(t *testing.T) {
	cfg := Default()
	if cfg.Spec.Provider.Proxmox != nil {
		t.Fatal("default read-only configuration unexpectedly has provisioning settings")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateProvisioningAcceptsCompleteProxmoxSettings(t *testing.T) {
	cfg := provisioningConfig()
	if err := cfg.ValidateProvisioning(); err != nil {
		t.Fatalf("ValidateProvisioning() error = %v", err)
	}
}

func TestValidateProvisioningRequiresTargetsAndSettings(t *testing.T) {
	cfg := Default()
	err := cfg.ValidateProvisioning()
	if err == nil {
		t.Fatal("expected provisioning validation error")
	}
	for _, expected := range []string{
		"spec.provider.targets must contain at least one target",
		"spec.provider.proxmox is required",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("expected %q in error: %v", expected, err)
		}
	}
}

func TestValidateProvisioningRequiresEveryField(t *testing.T) {
	cfg := provisioningConfig()
	cfg.Spec.Provider.Proxmox = &ProxmoxProvisioning{}
	err := cfg.ValidateProvisioning()
	if err == nil {
		t.Fatal("expected missing provisioning fields")
	}
	for _, expected := range []string{
		"bridge is required",
		"systemDatastore is required",
		"cloudImageFileID is required",
		"ssh.user is required",
		"ssh.publicKeyFile is required",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("expected %q in error: %v", expected, err)
		}
	}
}

func TestValidateRejectsMalformedOptionalProvisioningValues(t *testing.T) {
	cfg := Default()
	cfg.Spec.Provider.Proxmox = &ProxmoxProvisioning{
		Bridge:           "bad bridge",
		SystemDatastore:  "bad/storage",
		CloudImageFileID: "local:backup/image.qcow2",
		SSH: SSHProvisioning{
			User:          "BadUser",
			PublicKeyFile: " key.pub ",
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected malformed provisioning values to fail base validation")
	}
	for _, expected := range []string{
		"bridge must be a valid Proxmox bridge name",
		"systemDatastore must be a valid Proxmox datastore ID",
		"cloudImageFileID must use",
		"ssh.user must be a valid lowercase Linux username",
		"ssh.publicKeyFile must be a non-empty single-line path",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("expected %q in error: %v", expected, err)
		}
	}
}

func TestCloudImageFileIDAcceptsImportAndISOContentTypes(t *testing.T) {
	for _, value := range []string{
		"local:import/debian-12-genericcloud-amd64.qcow2",
		"local:iso/debian-12-genericcloud-amd64.qcow2.xz",
	} {
		t.Run(value, func(t *testing.T) {
			if !validCloudImageFileID(value) {
				t.Fatalf("expected %q to be valid", value)
			}
		})
	}
}

func TestCloudImageFileIDRejectsUnsafeOrUnsupportedForms(t *testing.T) {
	for _, value := range []string{
		"local:backup/image.qcow2",
		"local:import/../image.qcow2",
		"local:import/subdir/image.qcow2",
		"local:import/image file.qcow2",
		"bad/store:import/image.qcow2",
	} {
		t.Run(value, func(t *testing.T) {
			if validCloudImageFileID(value) {
				t.Fatalf("expected %q to be invalid", value)
			}
		})
	}
}

func provisioningConfig() Config {
	cfg := Default()
	cfg.Spec.Provider.Targets = []string{"pve1", "pve2"}
	cfg.Spec.Provider.Proxmox = &ProxmoxProvisioning{
		Bridge:           "vmbr0",
		SystemDatastore:  "local-lvm",
		CloudImageFileID: "local:import/debian-12-genericcloud-amd64.qcow2",
		SSH: SSHProvisioning{
			User:          "debian",
			PublicKeyFile: "~/.ssh/id_ed25519.pub",
		},
	}
	return cfg
}
