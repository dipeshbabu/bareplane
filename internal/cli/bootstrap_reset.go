package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dipeshbabu/bareplane/internal/bootstrapapply"
)

const bootstrapResetUsage = "usage: bareplane bootstrap reset --approve <cluster-name> --scope cluster --confirm-destructive [path]"

func runBootstrapReset(args []string, stdout, stderr io.Writer, dependencies ...bootstrapapply.ResetOptions) int {
	flags := flag.NewFlagSet("bootstrap reset", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	approval := flags.String("approve", "", "exact cluster name")
	scope := flags.String("scope", "", "cluster")
	confirm := flags.Bool("confirm-destructive", false, "acknowledge cluster reset and reboot")
	check := flags.Bool("check", false, "unsupported; use diagnose")
	if err := flags.Parse(args); err != nil || flags.NArg() > 1 || *approval == "" || *scope != "cluster" || !*confirm || *check {
		fmt.Fprintln(stderr, bootstrapResetUsage)
		return 2
	}
	options := bootstrapapply.ResetOptions{}
	if len(dependencies) > 0 {
		options = dependencies[0]
	}
	options.ConfigPath = "bareplane.yaml"
	if flags.NArg() == 1 {
		options.ConfigPath = flags.Arg(0)
	}
	options.Approval, options.Scope, options.ConfirmDestructive = *approval, *scope, *confirm
	options.Event = func(event bootstrapapply.Event) {
		fmt.Fprintf(stdout, "%-5s %-22s %s\n", strings.ToUpper(event.Status), event.Phase, event.Message)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := bootstrapapply.Reset(ctx, options); err != nil {
		fmt.Fprintf(stderr, "bootstrap reset: %v\n", err)
		var phase *bootstrapapply.PhaseError
		if errors.As(err, &phase) {
			fmt.Fprintf(stderr, "Inspect first: bareplane bootstrap diagnose %s\n", shellQuote(options.ConfigPath))
		}
		return 1
	}
	fmt.Fprintf(stdout, "Removed bootstrap Kubernetes/etcd/CNI state and the project admin credential; recovery requires backups. Private diagnostics were preserved.\nVMs, application disks, Terraform state, and the global kubeconfig were not removed.\nRebuild: bareplane bootstrap apply --approve %s %s\n", shellQuote(options.Approval), shellQuote(options.ConfigPath))
	return 0
}
