package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/bootstrapapply"
	"github.com/dipeshbabu/bareplane/internal/bootstrapdoctor"
	"github.com/dipeshbabu/bareplane/internal/bootstrappreflight"
	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/doctor"
	"github.com/dipeshbabu/bareplane/internal/project"
	ansible "github.com/dipeshbabu/bareplane/internal/render/ansible"
	"github.com/dipeshbabu/bareplane/internal/sshtrust"
	"gopkg.in/yaml.v3"
)

func TestBootstrapApplyRejectsRawArgumentsAndCheckMode(t *testing.T) {
	for _, args := range [][]string{{}, {"--approve"}, {"--tags", "SECRET"}, {"--approve", "lab", "one", "two"}, {"--check=SECRET"}} {
		var stdout, stderr bytes.Buffer
		if code := runBootstrapApply(args, &stdout, &stderr); code != 2 || strings.Contains(stderr.String(), "SECRET") {
			t.Fatalf("unsafe argument handling: %d %s", code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runBootstrapApply([]string{"--check"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "cannot safely model") {
		t.Fatalf("check mode promised mutation safety: %d %s", code, stderr.String())
	}
}

func TestBootstrapApplyCLIReportsSafeProgressAndRecovery(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "bareplane.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "bareplane.yaml")
	cfg.Spec.Bootstrap.SSH.PrivateKeyFile = "private-key"
	encoded, _ := yaml.Marshal(cfg)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "private-key"), []byte("PRIVATE-KEY-SENTINEL"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := ansible.RenderBundle(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.ReplaceGeneratedTree(filepath.Join(dir, ".bareplane", "bootstrap"), "bootstrap", files); err != nil {
		t.Fatal(err)
	}
	trust, _ := sshtrust.KnownHostsPathFor(path)
	if err := os.MkdirAll(filepath.Dir(trust), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trust, []byte("public fixture trust"), 0o600); err != nil {
		t.Fatal(err)
	}
	pass := doctor.Report{Results: []doctor.Result{{Name: "fixture", Status: doctor.StatusPass}}}
	options := bootstrapapply.Options{
		Doctor:       func(bootstrapdoctor.Options) doctor.Report { return pass },
		Preflight:    func(context.Context, bootstrappreflight.Options) doctor.Report { return pass },
		ResolveTrust: func(string) (string, error) { return trust, nil },
		Runner: func(_ context.Context, request bootstrapapply.Request) error {
			var renderOut, renderErr bytes.Buffer
			if code := runBootstrapRender([]string{path}, &renderOut, &renderErr); code != 1 || !strings.Contains(renderErr.String(), "bootstrap operation holds") {
				t.Fatal("render was allowed to race active bootstrap")
			}
			io.WriteString(request.Log, "PRIVATE-RUNNER-SENTINEL")
			return errors.New("PRIVATE-RUNNER-SENTINEL")
		},
	}
	var stdout, stderr bytes.Buffer
	args := []string{"--approve", cfg.Metadata.Name, path}
	if code := runBootstrapApply(args, &stdout, &stderr, options); code != 1 {
		t.Fatalf("failure returned %d", code)
	}
	if strings.Contains(stdout.String()+stderr.String(), "SENTINEL") || !strings.Contains(stderr.String(), "After inspecting recovery state, retry:") {
		t.Fatalf("unsafe diagnostic output: %s %s", stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	options.Runner = func(context.Context, bootstrapapply.Request) error { return nil }
	if code := runBootstrapApply(args, &stdout, &stderr, options); code != 0 || !strings.Contains(stdout.String(), "cluster is healthy") {
		t.Fatalf("resume failed: %d %s %s", code, stdout.String(), stderr.String())
	}
}
