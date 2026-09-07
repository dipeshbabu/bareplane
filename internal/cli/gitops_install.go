package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dipeshbabu/bareplane/internal/bootstrapapply"
)

const gitOpsInstallUsage = "usage: bareplane gitops install --approve <cluster-name> [path]"

func runGitOpsInstall(args []string, stdout, stderr io.Writer, dependencies ...bootstrapapply.Options) int {
	return runGitOpsMutation(args, stdout, stderr, false, dependencies...)
}

func runGitOpsHandoff(args []string, stdout, stderr io.Writer, dependencies ...bootstrapapply.Options) int {
	return runGitOpsMutation(args, stdout, stderr, true, dependencies...)
}

func runGitOpsMutation(args []string, stdout, stderr io.Writer, handoff bool, dependencies ...bootstrapapply.Options) int {
	operation, usage := "install", gitOpsInstallUsage
	run := bootstrapapply.InstallArgo
	if handoff {
		operation, usage, run = "handoff", "usage: bareplane gitops handoff --approve <cluster-name> [path]", bootstrapapply.HandoffGitOps
	}
	flags := flag.NewFlagSet("gitops "+operation, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	approval := flags.String("approve", "", "exact cluster name")
	if err := flags.Parse(args); err != nil || flags.NArg() > 1 || *approval == "" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	options := bootstrapapply.Options{}
	if len(dependencies) > 0 {
		options = dependencies[0]
	}
	options.ConfigPath, options.Approval = "bareplane.yaml", *approval
	if flags.NArg() == 1 {
		options.ConfigPath = flags.Arg(0)
	}
	options.Event = func(event bootstrapapply.Event) {
		fmt.Fprintf(stdout, "%-5s %-22s %s\n", strings.ToUpper(event.Status), event.Phase, event.Message)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, options); err != nil {
		fmt.Fprintf(stderr, "gitops %s: %v\n", operation, err)
		return 1
	}
	if handoff {
		fmt.Fprintln(stdout, "GitOps handoff verified; Argo owns long-lived platform reconciliation")
	} else {
		fmt.Fprintln(stdout, "minimal pinned Argo is ready; root Application handoff has not been performed")
	}
	return 0
}
