package doctor

import (
	"context"
	"testing"
)

func TestRunPreservesCheckOrderAndFailureState(t *testing.T) {
	checks := []Check{
		CheckFunc(func(context.Context) Result {
			return Result{Name: "first", Status: StatusPass, Message: "ok"}
		}),
		CheckFunc(func(context.Context) Result {
			return Result{Name: "second", Status: StatusWarn, Message: "warning"}
		}),
		CheckFunc(func(context.Context) Result {
			return Result{Name: "third", Status: StatusFail, Message: "failed"}
		}),
	}

	report := Run(context.Background(), checks)
	if len(report.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(report.Results))
	}
	for i, expected := range []string{"first", "second", "third"} {
		if report.Results[i].Name != expected {
			t.Fatalf("result %d = %q, want %q", i, report.Results[i].Name, expected)
		}
	}
	if !report.HasFailures() {
		t.Fatal("expected report to contain a failure")
	}
}

func TestRunStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report := Run(ctx, []Check{
		CheckFunc(func(context.Context) Result {
			t.Fatal("check should not run after cancellation")
			return Result{}
		}),
	})

	if len(report.Results) != 1 || report.Results[0].Name != "context" {
		t.Fatalf("unexpected cancellation report: %#v", report.Results)
	}
	if !report.HasFailures() {
		t.Fatal("cancelled report should fail")
	}
}
