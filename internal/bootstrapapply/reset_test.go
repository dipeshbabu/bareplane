package bootstrapapply

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func initializedResetFixture(t *testing.T) fixture {
	t.Helper()
	f := newFixture(t)
	if err := Apply(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestResetRequiresBothApprovalBoundariesAndExplicitScope(t *testing.T) {
	f := initializedResetFixture(t)
	for _, test := range []struct {
		approval, scope string
		confirm         bool
	}{{"wrong", "cluster", true}, {"lab", "cluster", false}, {"lab", "", true}, {"lab", "node", true}} {
		options := ResetOptions{Options: f.options, Scope: test.scope, ConfirmDestructive: test.confirm}
		options.Approval = test.approval
		options.Runner = func(context.Context, Request) error { t.Fatal("unapproved reset executed"); return nil }
		if err := Reset(context.Background(), options); err == nil {
			t.Fatalf("unsafe reset accepted: %+v", test)
		}
		if _, exists, err := ReadReset(f.path); err != nil || exists {
			t.Fatalf("unapproved reset wrote intent: %v %v", exists, err)
		}
	}
}

func TestResetRefusesUntrackedOrUnstartedClusters(t *testing.T) {
	f := newFixture(t)
	options := ResetOptions{Options: f.options, Scope: "cluster", ConfirmDestructive: true}
	options.Runner = func(context.Context, Request) error { t.Fatal("untracked reset executed"); return nil }
	if err := Reset(context.Background(), options); err == nil {
		t.Fatal("untracked cluster accepted")
	}
}

func TestValidatedResetHasSeparateIntentAndResetsOnlyBootstrapProgress(t *testing.T) {
	f := initializedResetFixture(t)
	var phases []string
	options := ResetOptions{Options: f.options, Scope: "cluster", ConfirmDestructive: true}
	options.Runner = func(ctx context.Context, request Request) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("reset phase has no deadline")
		}
		record, exists, err := ReadReset(f.path)
		if err != nil || !exists || record.ID != request.RecoveryID {
			t.Fatal("reset intent not recorded")
		}
		if request.Phase == "reset_validate" && request.AllowUnavailableAPI {
			t.Fatal("completed cluster bypassed API storage checks")
		}
		if request.Phase == "reset_execute" && record.Stage != "validated" {
			t.Fatal("destruction preceded validation")
		}
		phases = append(phases, request.Phase)
		return nil
	}
	if err := Reset(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(phases, []string{"reset_validate", "reset_execute"}) {
		t.Fatalf("unexpected reset sequence: %v", phases)
	}
	progress, _, err := ReadProgress(f.path)
	if err != nil || progress.Completed != 2 || progress.Active != "" {
		t.Fatalf("invalid post-reset progress: %+v %v", progress, err)
	}
	if _, pending, err := ReadReset(f.path); err != nil || pending {
		t.Fatalf("reset remained pending: %v %v", pending, err)
	}
}

func TestFailedResetValidationDoesNotBlockOrdinaryApply(t *testing.T) {
	f := initializedResetFixture(t)
	options := ResetOptions{Options: f.options, Scope: "cluster", ConfirmDestructive: true}
	options.Runner = func(context.Context, Request) error { return errors.New("PRIVATE-RESET-ERROR") }
	err := Reset(context.Background(), options)
	if err == nil || strings.Contains(err.Error(), "PRIVATE-RESET-ERROR") {
		t.Fatalf("unsafe reset error: %v", err)
	}
	if _, pending, err := ReadReset(f.path); err != nil || pending {
		t.Fatal("read-only validation left destructive intent")
	}
	if err := Apply(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptedDestructiveResetBlocksApplyAndResumesSameIdentity(t *testing.T) {
	f := initializedResetFixture(t)
	options := ResetOptions{Options: f.options, Scope: "cluster", ConfirmDestructive: true}
	var identity string
	options.Runner = func(_ context.Context, request Request) error {
		identity = request.RecoveryID
		if request.Phase == "reset_execute" {
			return errors.New("interrupted")
		}
		return nil
	}
	if err := Reset(context.Background(), options); err == nil {
		t.Fatal("reset failure accepted")
	}
	if err := Apply(context.Background(), f.options); err == nil || !strings.Contains(err.Error(), "reset is pending") {
		t.Fatalf("apply bypassed pending reset: %v", err)
	}
	options.Runner = func(_ context.Context, request Request) error {
		if request.RecoveryID != identity {
			t.Fatal("reset retry changed ownership identity")
		}
		return nil
	}
	if err := Reset(context.Background(), options); err != nil {
		t.Fatal(err)
	}
}

func TestResetCommandShapeNeverEntersOrdinaryApply(t *testing.T) {
	request := Request{Phase: "reset_execute", BundleDir: "/owned/bootstrap", PrivateKeyFile: "/private/key"}
	if _, err := phaseArguments(request); err == nil {
		t.Fatal("ordinary runner accepted reset")
	}
	request.RecoveryID = strings.Repeat("a", 32)
	args, err := phaseArguments(request)
	if err != nil {
		t.Fatal(err)
	}
	if args[len(args)-1] != "reset_execute.yaml" || !strings.Contains(strings.Join(args, " "), `"bareplane_reset_scope":"cluster"`) {
		t.Fatalf("wrong reset shape: %#v", args)
	}
	request.RecoveryID = "../unowned"
	if _, err := phaseArguments(request); err == nil {
		t.Fatal("unsafe reset identifier accepted")
	}
}
