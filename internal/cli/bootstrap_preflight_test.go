package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/bootstrappreflight"
)

func TestRunBootstrapPreflightRejectsTooManyArguments(t *testing.T) {
	called := false
	var stdout, stderr bytes.Buffer
	code := runBootstrapPreflight([]string{"one", "two"}, &stdout, &stderr, func(context.Context, bootstrappreflight.Request) ([]byte, error) {
		called = true
		return nil, nil
	})
	if code != 2 || called || !strings.Contains(stderr.String(), "usage: bareplane bootstrap preflight [path]") {
		t.Fatalf("code=%d called=%t stderr=%q", code, called, stderr.String())
	}
}

func TestBootstrapHelpListsPreflight(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"bootstrap", "help"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "bareplane bootstrap preflight") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
