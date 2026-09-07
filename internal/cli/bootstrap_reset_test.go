package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestResetCLIRequiresExplicitScopeAndDestructiveConfirmation(t *testing.T) {
	for _, args := range [][]string{{}, {"--approve", "lab"}, {"--approve", "lab", "--scope", "cluster"},
		{"--approve", "lab", "--scope", "node", "--confirm-destructive"}, {"--check"}, {"--secret", "DO-NOT-PRINT"}} {
		var stdout, stderr bytes.Buffer
		if code := runBootstrapReset(args, &stdout, &stderr); code != 2 || strings.Contains(stderr.String(), "DO-NOT-PRINT") {
			t.Fatalf("reset arguments were not refused safely: %d %s", code, stderr.String())
		}
	}
}

func TestDiagnoseCLIRejectsExtraArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runBootstrapDiagnose([]string{"one", "two"}, &stdout, &stderr); code != 2 {
		t.Fatalf("diagnose accepted extra arguments: %d", code)
	}
}
