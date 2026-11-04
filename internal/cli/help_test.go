package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRunCommandHelpHasNoSideEffects(t *testing.T) {
	// In particular, init must not interpret a help flag as an output filename.
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Fatal(err)
		}
	})
	commands := []string{
		"init", "validate", "status", "doctor", "plan", "render", "version",
		"terraform plan", "terraform apply",
		"bootstrap render", "bootstrap doctor", "bootstrap check", "bootstrap trust",
		"bootstrap preflight", "bootstrap apply", "bootstrap diagnose", "bootstrap reset",
		"bootstrap recover-kubeconfig", "bootstrap kubelet-tls",
		"gitops render", "gitops install", "gitops handoff",
	}
	for _, command := range commands {
		for _, flag := range []string{"--help", "-h"} {
			t.Run(command+" "+flag, func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				args := append(strings.Fields(command), flag)
				if code := Run(args, &stdout, &stderr); code != 0 {
					t.Fatalf("help returned %d: %s", code, stderr.String())
				}
				if !strings.Contains(stdout.String(), "bareplane "+command) || stderr.Len() != 0 {
					t.Fatalf("unexpected help: stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
			})
		}
	}
	entries, err := os.ReadDir(".")
	if err != nil || len(entries) != 0 {
		t.Fatalf("help created project files: %v, %v", entries, err)
	}
}

func TestRunCommandHelpPreservesInvalidArgumentErrors(t *testing.T) {
	for _, args := range [][]string{
		{"unknown", "--help"}, {"bootstrap", "unknown", "--help"},
		{"terraform", "unknown", "-h"}, {"gitops", "unknown", "--help"},
		{"init", "--help", "extra"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 {
			t.Fatalf("invalid arguments %v returned %d: %s %s", args, code, stdout.String(), stderr.String())
		}
	}
}
