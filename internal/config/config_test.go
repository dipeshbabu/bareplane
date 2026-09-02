package config

import (
	"strings"
	"testing"
)

const validConfig = `apiVersion: bareplane.io/v1alpha1
kind: BareplaneCluster
metadata:
  name: lab
spec:
  domain: lab.example.com
  provider:
    type: proxmox
    endpoint: https://proxmox.example.com:8006
  nodes:
    - name: control
      role: control-plane
      count: 3
      cpu: 4
      memoryGB: 8
      diskGB: 64
    - name: gpu
      role: worker
      count: 2
      cpu: 16
      memoryGB: 64
      diskGB: 256
      gpu: true
  features:
    observability: true
    gpu: true
  profiles:
    - ai
  dns:
    provider: cloudflare
  secrets:
    provider: sops
`

func TestLoadValidConfig(t *testing.T) {
	cfg, err := Load(strings.NewReader(validConfig))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Metadata.Name != "lab" {
		t.Fatalf("unexpected cluster name %q", cfg.Metadata.Name)
	}
	if got := len(cfg.Spec.Nodes); got != 2 {
		t.Fatalf("expected 2 node groups, got %d", got)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	input := strings.Replace(validConfig, "metadata:\n", "metadata:\n  unexpected: true\n", 1)
	_, err := Load(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "field unexpected not found") {
		t.Fatalf("expected strict field error, got %v", err)
	}
}

func TestLoadRejectsMultipleDocuments(t *testing.T) {
	_, err := Load(strings.NewReader(validConfig + "---\n{}\n"))
	if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("expected multi-document error, got %v", err)
	}
}

func TestValidationReportsSemanticProblems(t *testing.T) {
	input := strings.NewReplacer(
		"name: gpu", "name: control",
		"count: 2", "count: 0",
		"diskGB: 256", "diskGB: 4",
		"- ai", "- unsupported",
	).Replace(validConfig)

	_, err := Load(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, expected := range []string{
		"name is duplicated",
		"count must be at least 1",
		"diskGB must be at least 10",
		"spec.profiles[0] is unsupported",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("expected %q in error: %v", expected, err)
		}
	}
}

func TestValidateRejectsInvalidDomain(t *testing.T) {
	input := strings.Replace(validConfig, "lab.example.com", "Bad_domain", 1)
	_, err := Load(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "spec.domain") {
		t.Fatalf("expected domain validation error, got %v", err)
	}
}
