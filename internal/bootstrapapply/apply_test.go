package bootstrapapply

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/bootstrapdoctor"
	"github.com/dipeshbabu/bareplane/internal/bootstrappreflight"
	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/doctor"
	"github.com/dipeshbabu/bareplane/internal/project"
	ansible "github.com/dipeshbabu/bareplane/internal/render/ansible"
	"github.com/dipeshbabu/bareplane/internal/sshtrust"
	"gopkg.in/yaml.v3"
)

type fixture struct {
	path, bundle, trust, key string
	cfg                      config.Config
	options                  Options
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "bareplane.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "project 'quoted' %h")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg.Metadata.Name = "lab"
	cfg.Spec.Nodes = []config.NodeGroup{{Name: "control", Role: "control-plane", Count: 1, CPU: 2, MemoryGB: 4, DiskGB: 24}}
	cfg.Spec.Features.GPU = false
	cfg.Spec.Profiles = []string{"minimal"}
	cfg.Spec.Bootstrap.SSH.Hosts = map[string]string{"lab-control-1": "192.0.2.11"}
	cfg.Spec.Bootstrap.SSH.PrivateKeyFile = "private-key"
	cfg.Spec.Kubernetes.APIVIP = "192.0.2.100"
	path := filepath.Join(directory, "bareplane.yaml")
	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(directory, "private-key")
	if err := os.WriteFile(key, []byte("PRIVATE-FIXTURE-NOT-A-REAL-KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := ansible.RenderBundle(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(directory, ".bareplane", "bootstrap")
	if err := project.ReplaceGeneratedTree(bundle, "bootstrap", files); err != nil {
		t.Fatal(err)
	}
	trust, err := sshtrust.KnownHostsPathFor(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(trust), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trust, []byte("approved public fixture keys\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := Options{ConfigPath: path, Approval: "lab",
		Runner:       func(context.Context, Request) error { return nil },
		Doctor:       func(bootstrapdoctor.Options) doctor.Report { return passing() },
		Preflight:    func(context.Context, bootstrappreflight.Options) doctor.Report { return passing() },
		ResolveTrust: func(string) (string, error) { return trust, nil },
		LookPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
	}
	return fixture{path, bundle, trust, key, cfg, opts}
}

func passing() doctor.Report {
	return doctor.Report{Results: []doctor.Result{{Name: "fixture", Status: doctor.StatusPass, Message: "ready"}}}
}
func failed() doctor.Report {
	return doctor.Report{Results: []doctor.Result{{Name: "fixture", Status: doctor.StatusFail, Message: "not ready"}}}
}

func TestApprovalAndCheckModeFailBeforeChecksOrMutation(t *testing.T) {
	f := newFixture(t)
	for _, approval := range []string{"", "LAB", "lab "} {
		options := f.options
		options.Approval = approval
		options.Doctor = func(bootstrapdoctor.Options) doctor.Report { t.Fatal("doctor called before approval"); return failed() }
		options.Runner = func(context.Context, Request) error { t.Fatal("runner called before approval"); return nil }
		if err := Apply(context.Background(), options); !errors.Is(err, ErrApprovalRequired) {
			t.Fatalf("approval %q: %v", approval, err)
		}
	}
	options := f.options
	options.Check = true
	options.ConfigPath = "missing"
	if err := Apply(context.Background(), options); !errors.Is(err, ErrCheckUnsupported) {
		t.Fatal(err)
	}
	if _, exists, err := ReadProgress(f.path); err != nil || exists {
		t.Fatalf("unexpected progress: %v %v", exists, err)
	}
}

func TestPhaseOrderPrerequisitesAndHealthyRerun(t *testing.T) {
	f := newFixture(t)
	var calls []string
	doctors, preflights := 0, 0
	f.options.Doctor = func(bootstrapdoctor.Options) doctor.Report { doctors++; return passing() }
	f.options.Preflight = func(_ context.Context, options bootstrappreflight.Options) doctor.Report {
		if options.Preparation != (preflights == 0) || options.Resume != (preflights > 0) {
			t.Fatalf("incorrect policy at %d: %+v", preflights, options)
		}
		preflights++
		return passing()
	}
	f.options.Runner = func(ctx context.Context, request Request) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("phase has no deadline")
		}
		state, exists, err := ReadProgress(f.path)
		if err != nil || !exists || state.Active != request.Phase || state.Completed != len(calls) {
			t.Fatalf("intent not recorded before mutation: %+v %v", state, err)
		}
		if request.PrivateKeyFile != f.key || request.KnownHostsFile != f.trust || request.BundleDir != f.bundle {
			t.Fatalf("incorrect controlled paths: %+v", request)
		}
		calls = append(calls, request.Phase)
		return nil
	}
	if err := Apply(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, Phases()) || doctors != len(phases) || preflights != len(phases) {
		t.Fatalf("phase/check sequence: %v %d %d", calls, doctors, preflights)
	}
	state, exists, err := ReadProgress(f.path)
	if err != nil || !exists || state.Completed != len(phases) || state.Active != "" {
		t.Fatalf("incomplete progress: %+v %v", state, err)
	}
	calls = nil
	f.options.Preflight = func(_ context.Context, options bootstrappreflight.Options) doctor.Report {
		if !options.Resume || options.Preparation {
			t.Fatal("healthy rerun used fresh-host policy")
		}
		return passing()
	}
	f.options.Runner = func(_ context.Context, request Request) error { calls = append(calls, request.Phase); return nil }
	if err := Apply(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"health"}) {
		t.Fatalf("healthy cluster was reconfigured: %v", calls)
	}
}

func TestFailureNeverCompletesPhaseAndResumeStartsThere(t *testing.T) {
	f := newFixture(t)
	sentinel := "NEVER-PRINT-PRIVATE-RUNNER-OUTPUT"
	f.options.Runner = func(_ context.Context, request Request) error {
		if request.Phase == "join" {
			io.WriteString(request.Log, sentinel)
			return errors.New(sentinel)
		}
		return nil
	}
	err := Apply(context.Background(), f.options)
	var failure *PhaseError
	if !errors.As(err, &failure) || failure.Phase != "join" || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("unsafe failure: %v", err)
	}
	state, _, err := ReadProgress(f.path)
	if err != nil || state.Completed != 5 || state.Active != "join" {
		t.Fatalf("failed phase advanced: %+v %v", state, err)
	}
	data, err := os.ReadFile(failure.LogPath)
	if err != nil || string(data) != sentinel {
		t.Fatal("private diagnostic log was not preserved")
	}
	var calls []string
	f.options.Runner = func(_ context.Context, request Request) error { calls = append(calls, request.Phase); return nil }
	if err := Apply(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"join", "kubeconfig", "health"}) {
		t.Fatalf("incorrect resume: %v", calls)
	}
}

func TestPreflightFailureAndCancellationDoNotAdvance(t *testing.T) {
	for _, mode := range []string{"doctor", "preflight", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "doctor" {
				f.options.Doctor = func(bootstrapdoctor.Options) doctor.Report { return failed() }
			}
			if mode == "preflight" {
				f.options.Preflight = func(context.Context, bootstrappreflight.Options) doctor.Report { return failed() }
			}
			f.options.Runner = func(context.Context, Request) error {
				if mode != "cancel" {
					t.Fatal("failed prerequisite still ran Ansible")
				}
				cancel()
				return nil
			}
			if err := Apply(ctx, f.options); err == nil {
				t.Fatal("failure accepted")
			}
			state, exists, err := ReadProgress(f.path)
			if err != nil || state.Completed != 0 || (mode == "cancel") != exists {
				t.Fatalf("bad failure progress: %+v %v %v", state, exists, err)
			}
		})
	}
}

func TestConfigurationTrustAndBundleChangesStopTransitions(t *testing.T) {
	for _, target := range []string{"config", "trust", "bundle", "progress", "during-preflight"} {
		t.Run(target, func(t *testing.T) {
			f := newFixture(t)
			calls := 0
			mutate := func() {
				var err error
				switch target {
				case "config":
					f.cfg.Spec.Kubernetes.Version = "1.36.3"
					data, _ := yaml.Marshal(f.cfg)
					err = os.WriteFile(f.path, data, 0o600)
				case "trust":
					err = os.WriteFile(f.trust, []byte("rotated fixture keys"), 0o600)
				case "bundle", "during-preflight":
					err = os.WriteFile(filepath.Join(f.bundle, "health.yaml"), []byte("unowned"), 0o644)
				case "progress":
					directory, _ := project.BootstrapStateDirFor(f.path)
					err = os.WriteFile(filepath.Join(directory, ProgressFilename), []byte("unmanaged"), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if target == "during-preflight" {
				f.options.Preflight = func(context.Context, bootstrappreflight.Options) doctor.Report { mutate(); return passing() }
			}
			f.options.Runner = func(context.Context, Request) error { calls++; mutate(); return nil }
			if err := Apply(context.Background(), f.options); err == nil {
				t.Fatal("changed prerequisite accepted")
			}
			want := 1
			if target == "during-preflight" {
				want = 0
			}
			if calls != want {
				t.Fatalf("ran %d phases after changed inputs", calls)
			}
		})
	}
}

func TestProgressRejectsChangedConfigurationAndTrustOnResume(t *testing.T) {
	for _, changeTrust := range []bool{false, true} {
		f := newFixture(t)
		f.options.Runner = func(context.Context, Request) error { return errors.New("stop") }
		if err := Apply(context.Background(), f.options); err == nil {
			t.Fatal("fixture did not fail")
		}
		if changeTrust {
			if err := os.WriteFile(f.trust, []byte("different identity"), 0o600); err != nil {
				t.Fatal(err)
			}
		} else {
			f.cfg.Spec.Kubernetes.Version = "1.36.3"
			data, _ := yaml.Marshal(f.cfg)
			if err := os.WriteFile(f.path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			files, _ := ansible.RenderBundle(f.cfg)
			if err := project.ReplaceGeneratedTree(f.bundle, "bootstrap", files); err != nil {
				t.Fatal(err)
			}
		}
		f.options.Runner = func(context.Context, Request) error { t.Fatal("changed identity resumed"); return nil }
		if err := Apply(context.Background(), f.options); err == nil || !strings.Contains(err.Error(), "different configuration or SSH identities") {
			t.Fatalf("unexpected refusal: %v", err)
		}
	}
}

func TestBootstrapLockSerializesRunsAndPreservesTerraformLock(t *testing.T) {
	f := newFixture(t)
	lock, err := project.AcquireBootstrapOperation(f.path, "apply")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if err := Apply(context.Background(), f.options); !errors.Is(err, project.ErrBootstrapOperationLocked) {
		t.Fatalf("overlapping apply accepted: %v", err)
	}
	terra, err := project.AcquireTerraformOperation(f.path, "plan")
	if err != nil {
		t.Fatalf("bootstrap lock replaced Terraform locking: %v", err)
	}
	if err := terra.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestExtraAssetsSymlinksAndInsecureProgressAreRefused(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.bundle, "custom.yaml"), []byte("not owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyBundle(f.path, f.cfg); err == nil {
		t.Fatal("extra playbook accepted")
	}
	if err := os.Remove(filepath.Join(f.bundle, "custom.yaml")); err != nil {
		t.Fatal(err)
	}
	directory, _ := project.BootstrapStateDirFor(f.path)
	progress := filepath.Join(directory, ProgressFilename)
	if err := os.Symlink(f.key, progress); err == nil {
		if _, _, err := ReadProgress(f.path); err == nil {
			t.Fatal("symlinked progress accepted")
		}
		if err := os.Remove(progress); err != nil {
			t.Fatal(err)
		}
	}
	if runtime.GOOS != "windows" {
		if err := Apply(context.Background(), f.options); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(progress, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadProgress(f.path); err == nil {
			t.Fatal("public progress accepted")
		}
	}
}
