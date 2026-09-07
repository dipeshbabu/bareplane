package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/bootstrapapply"
	"github.com/dipeshbabu/bareplane/internal/bootstrappreflight"
	"github.com/dipeshbabu/bareplane/internal/project"
)

func runBootstrapDiagnose(args []string, stdout, stderr io.Writer, dependencies ...bootstrappreflight.Options) int {
	if len(args) > 1 || (len(args) == 1 && strings.HasPrefix(args[0], "-")) {
		fmt.Fprintln(stderr, "usage: bareplane bootstrap diagnose [path]")
		return 2
	}
	path := "bareplane.yaml"
	if len(args) == 1 {
		path = args[0]
	}
	code := 0
	progress, exists, err := bootstrapapply.ReadProgress(path)
	if err != nil {
		fmt.Fprintln(stdout, "FAIL local-progress: private progress is unavailable or invalid; do not remove ownership state to force bootstrap")
		code = 1
	} else if exists {
		fmt.Fprintf(stdout, "INFO local-progress: cluster=%s completed=%d active=%s\n", progress.Cluster, progress.Completed, progress.Active)
		if progress.Log != "" {
			fmt.Fprintf(stdout, "INFO private-log: .bareplane/state/bootstrap/logs/%s (inspect privately)\n", progress.Log)
		}
	} else {
		fmt.Fprintln(stdout, "INFO local-progress: no recorded orchestration; existing cluster state must not be adopted implicitly")
	}
	lock, err := project.InspectBootstrapOperation(path)
	if err != nil {
		fmt.Fprintln(stdout, "FAIL operation-lock: unreadable or redirected lock; inspect it on the original controller")
		code = 1
	} else if lock.Present {
		fmt.Fprintf(stdout, "WARN operation-lock: operation=%s pid=%d; confirm status on the original controller before any stale-lock cleanup\n", lock.Operation, lock.PID)
	} else {
		fmt.Fprintln(stdout, "INFO operation-lock: none")
	}
	reset, present, err := bootstrapapply.ReadReset(path)
	if err != nil {
		fmt.Fprintln(stdout, "FAIL reset-state: reset metadata is invalid; do not bypass it")
		code = 1
	} else if present {
		fmt.Fprintf(stdout, "WARN reset-state: stage=%s; finish the explicitly approved full-cluster reset before apply\n", reset.Stage)
	}
	options := bootstrappreflight.Options{ConfigPath: path}
	if len(dependencies) > 0 {
		options = dependencies[0]
		options.ConfigPath = path
	}
	report := bootstrappreflight.Diagnose(context.Background(), options)
	for _, result := range report.Results {
		fmt.Fprintf(stdout, "%-4s %-28s %s\n", strings.ToUpper(string(result.Status)), result.Name, result.Message)
	}
	if report.HasFailures() {
		code = 1
	}
	fmt.Fprintln(stdout, "Diagnosis made no remote changes. See docs/bootstrap-recovery.md before reset or retry.")
	return code
}
