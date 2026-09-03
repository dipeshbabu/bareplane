package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunBootstrapCheckRejectsTooManyArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runBootstrapCheck([]string{"one", "two"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "usage: bareplane bootstrap check [path]") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestBootstrapHelpListsCheck(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"bootstrap", "help"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "bareplane bootstrap check") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
