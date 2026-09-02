package config

import (
	"strings"
	"testing"
)

func TestValidateProviderAndGroupTargets(t *testing.T) {
	cfg := Default()
	cfg.Spec.Provider.Targets = []string{"pve2", "pve1"}
	cfg.Spec.Nodes[0].Targets = []string{"pve2"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid placement config, got %v", err)
	}
}

func TestValidateRejectsDuplicateProviderTarget(t *testing.T) {
	cfg := Default()
	cfg.Spec.Provider.Targets = []string{"pve1", "pve1"}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "spec.provider.targets[1] is duplicated") {
		t.Fatalf("expected duplicate provider target error, got %v", err)
	}
}

func TestValidateRejectsInvalidProviderTarget(t *testing.T) {
	cfg := Default()
	cfg.Spec.Provider.Targets = []string{"PVE_1"}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "spec.provider.targets[0]") {
		t.Fatalf("expected invalid provider target error, got %v", err)
	}
}

func TestValidateRejectsUnknownGroupTarget(t *testing.T) {
	cfg := Default()
	cfg.Spec.Provider.Targets = []string{"pve1"}
	cfg.Spec.Nodes[0].Targets = []string{"pve2"}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "must reference spec.provider.targets") {
		t.Fatalf("expected unknown group target error, got %v", err)
	}
}

func TestValidateRejectsDuplicateGroupTarget(t *testing.T) {
	cfg := Default()
	cfg.Spec.Provider.Targets = []string{"pve1", "pve2"}
	cfg.Spec.Nodes[0].Targets = []string{"pve1", "pve1"}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), ".targets[1] is duplicated") {
		t.Fatalf("expected duplicate group target error, got %v", err)
	}
}
