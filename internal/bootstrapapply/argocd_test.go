package bootstrapapply

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/bootstrapdoctor"
	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/doctor"
	"github.com/dipeshbabu/bareplane/internal/project"
	gitopsrender "github.com/dipeshbabu/bareplane/internal/render/gitops"
	"gopkg.in/yaml.v3"
)

func newArgoFixture(t *testing.T) fixture {
	t.Helper()
	f := newFixture(t)
	f.cfg.Spec.Features = config.Features{}
	f.cfg.Spec.DNS.Provider = "manual"
	f.cfg.Spec.Secrets.Provider = "sops"
	f.cfg.Spec.GitOps = &config.GitOpsConfig{RepoURL: "https://git.example.com/team/lab.git", Revision: "main", RootPath: "clusters/lab"}
	data, err := yaml.Marshal(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, contract, err := verifyBundle(f.path, f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	trust, err := fileFingerprint(f.trust, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveProgress(f.path, Progress{Version: 1, Cluster: f.cfg.Metadata.Name, Contract: contract, Trust: trust, Completed: len(phases)}); err != nil {
		t.Fatal(err)
	}
	files, err := gitopsrender.Render(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := project.WriteGitOpsExport(f.path, f.cfg.Metadata.Name, files); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestArgoInstallIsSeparateApprovedPhaseWithIntentAndSafeRerun(t *testing.T) {
	f := newArgoFixture(t)
	before, _, err := ReadProgress(f.path)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	f.options.Runner = func(ctx context.Context, request Request) error {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing deadline")
		}
		record, exists, err := ReadGitOpsProgress(f.path)
		if err != nil || !exists || record.Stage != "installing" {
			t.Fatalf("intent missing: %+v %v", record, err)
		}
		if request.Phase != "argocd" || request.Argo == nil || request.Argo.Contract != record.Contract || request.Argo.PayloadDir != filepath.Join(request.StateDir, "argocd-input") {
			t.Fatalf("uncontrolled request: %+v", request)
		}
		if _, err := project.AcquireBootstrapOperation(f.path, "reset"); !errors.Is(err, project.ErrBootstrapOperationLocked) {
			t.Fatal("installation did not hold shared lock")
		}
		return nil
	}
	for i := 0; i < 2; i++ {
		if err := InstallArgo(context.Background(), f.options); err != nil {
			t.Fatal(err)
		}
		record, exists, err := ReadGitOpsProgress(f.path)
		if err != nil || !exists || record.Stage != "argocd-ready" {
			t.Fatalf("readiness missing: %+v %v", record, err)
		}
	}
	after, _, err := ReadProgress(f.path)
	if err != nil || before != after || calls != 2 || len(Phases()) != 8 {
		t.Fatal("Argo changed the bootstrap phase prefix")
	}
}

func TestArgoApprovalPrerequisitesAndProgressRefuseBeforeRunner(t *testing.T) {
	for _, mode := range []string{"approval", "unfinished", "active", "doctor", "missing-export", "edited-export", "pending-reset", "lock"} {
		t.Run(mode, func(t *testing.T) {
			f := newArgoFixture(t)
			state, _, _ := ReadProgress(f.path)
			switch mode {
			case "approval":
				f.options.Approval = "other"
			case "unfinished":
				state.Completed = 7
				if err := saveProgress(f.path, state); err != nil {
					t.Fatal(err)
				}
			case "active":
				state.Active = "health"
				if err := saveProgress(f.path, state); err != nil {
					t.Fatal(err)
				}
			case "doctor":
				f.options.Doctor = func(bootstrapdoctor.Options) doctor.Report { return failed() }
			case "missing-export":
				if err := os.Remove(filepath.Join(filepath.Dir(f.path), "gitops", project.GeneratedMarkerFilename)); err != nil {
					t.Fatal(err)
				}
			case "edited-export":
				if err := os.WriteFile(filepath.Join(filepath.Dir(f.path), "gitops", "readme.md"), []byte("user edit"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "pending-reset":
				if err := saveReset(f.path, ResetRecord{Version: 1, ID: strings.Repeat("a", 32), Stage: "planned", Source: state}); err != nil {
					t.Fatal(err)
				}
			case "lock":
				lock, err := project.AcquireBootstrapOperation(f.path, "apply")
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Release()
			}
			f.options.Runner = func(context.Context, Request) error { t.Fatal("failed prerequisite reached runner"); return nil }
			if err := InstallArgo(context.Background(), f.options); err == nil {
				t.Fatal("unsafe install accepted")
			}
			if _, exists, err := ReadGitOpsProgress(f.path); err != nil || exists {
				t.Fatal("refusal published installation progress")
			}
		})
	}
}

func TestArgoFailureCancellationAndChangedInputsNeverRecordReady(t *testing.T) {
	for _, mode := range []string{"failure", "cancel", "contract", "trust", "bundle", "payload", "progress"} {
		t.Run(mode, func(t *testing.T) {
			f := newArgoFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.options.Runner = func(_ context.Context, request Request) error {
				var err error
				switch mode {
				case "failure":
					return errors.New("PRIVATE-RUNNER-SENTINEL")
				case "cancel":
					cancel()
				case "contract":
					f.cfg.Spec.GitOps.Revision = "changed"
					data, _ := yaml.Marshal(f.cfg)
					err = os.WriteFile(f.path, data, 0o600)
				case "trust":
					err = os.WriteFile(f.trust, []byte("changed"), 0o600)
				case "bundle":
					err = os.WriteFile(filepath.Join(f.bundle, "argocd.yaml"), []byte("changed"), 0o644)
				case "payload":
					err = os.WriteFile(filepath.Join(request.Argo.PayloadDir, "namespace.yaml"), []byte("changed"), 0o600)
				case "progress":
					record, _, _ := ReadProgress(f.path)
					record.Active = "health"
					err = saveProgress(f.path, record)
				}
				if err != nil {
					t.Fatal(err)
				}
				return nil
			}
			err := InstallArgo(ctx, f.options)
			if err == nil || strings.Contains(err.Error(), "PRIVATE-RUNNER-SENTINEL") {
				t.Fatalf("unsafe error: %v", err)
			}
			record, exists, err := ReadGitOpsProgress(f.path)
			if err != nil || !exists || record.Stage != "installing" {
				t.Fatalf("false readiness: %+v %v", record, err)
			}
		})
	}
}

func TestArgoRunnerRequestCannotMixRecoveryOrOrdinaryPhases(t *testing.T) {
	request := Request{Phase: "argocd", StateDir: "/owned/state", BundleDir: "/owned/bootstrap", PrivateKeyFile: "/private/key",
		Argo: &ArgoRequest{Repository: "https://git.example.com/team/repo.git", Revision: "main", RootPath: "clusters/lab", Contract: strings.Repeat("a", 64), PayloadDir: filepath.Join("/owned/state", "argocd-input")}}
	args, err := phaseArguments(request)
	if err != nil {
		t.Fatal(err)
	}
	var variables map[string]any
	if json.Unmarshal([]byte(args[len(args)-2]), &variables) != nil || variables["bareplane_argocd_approved"] != true || variables["bareplane_gitops_contract"] != request.Argo.Contract {
		t.Fatalf("wrong args: %v", args)
	}
	if !reflect.DeepEqual(args[len(args)-1:], []string{"argocd.yaml"}) {
		t.Fatal("wrong playbook")
	}
	for _, phase := range []string{"join", "health", "reset_execute", "../custom"} {
		request.Phase = phase
		if _, err := phaseArguments(request); err == nil {
			t.Fatalf("unowned phase %s accepted", phase)
		}
	}
	request.Phase = "argocd"
	request.RecoveryID = strings.Repeat("b", 32)
	if _, err := phaseArguments(request); err == nil {
		t.Fatal("mixed recovery accepted")
	}
	request.RecoveryID = ""
	request.Argo.PayloadDir = "/unowned"
	if _, err := phaseArguments(request); err == nil {
		t.Fatal("unowned input accepted")
	}
}

func TestArgoReadOnlyPrerequisiteCanBeCorrectedButCreationIntentCannot(t *testing.T) {
	for _, createdIntent := range []bool{false, true} {
		f := newArgoFixture(t)
		f.options.Runner = func(context.Context, Request) error { return errors.New("repository not found") }
		if err := InstallArgo(context.Background(), f.options); err == nil {
			t.Fatal("fixture did not fail")
		}
		if createdIntent {
			state, _ := project.BootstrapStateDirFor(f.path)
			if err := os.WriteFile(filepath.Join(state, "argocd-ownership.json"), []byte("creation intent"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		f.cfg.Spec.GitOps.RootPath = "corrected/root"
		data, _ := yaml.Marshal(f.cfg)
		if err := os.WriteFile(f.path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		files, err := gitopsrender.Render(f.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := project.WriteGitOpsExport(f.path, "lab", files); err != nil {
			t.Fatal(err)
		}
		called := false
		f.options.Runner = func(context.Context, Request) error { called = true; return nil }
		err = InstallArgo(context.Background(), f.options)
		if (err != nil) != createdIntent || called == createdIntent {
			t.Fatalf("unsafe prerequisite correction: intent=%v called=%v err=%v", createdIntent, called, err)
		}
	}
}
