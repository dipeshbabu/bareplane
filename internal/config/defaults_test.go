package config

import (
	"bytes"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("Default() is invalid: %v", err)
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := Encode(&buf, Default()); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	cfg, err := Load(&buf)
	if err != nil {
		t.Fatalf("encoded configuration did not load: %v", err)
	}
	if cfg.Metadata.Name != Default().Metadata.Name {
		t.Fatalf("unexpected metadata.name %q", cfg.Metadata.Name)
	}
}
