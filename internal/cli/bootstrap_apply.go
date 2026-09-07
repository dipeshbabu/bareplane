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

const bootstrapApplyUsage = "usage: bareplane bootstrap apply --approve <cluster-name> [path]"

func runBootstrapApply(args []string, stdout, stderr io.Writer, dependencies ...bootstrapapply.Options) int {
	flags := flag.NewFlagSet("bootstrap apply", flag.ContinueOnError)
	// Invalid raw arguments can contain accidental credentials; print only the
	// closed command contract, never the rejected value.
	flags.SetOutput(io.Discard)
	approval := flags.String("approve", "", "exact cluster name")
	check := flags.Bool("check", false, "unsupported for kubeadm and health workloads")
	if err := flags.Parse(args); err != nil || flags.NArg() > 1 || (*approval == "" && !*check) {
		fmt.Fprintln(stderr, bootstrapApplyUsage)
		return 2
	}
	options := bootstrapapply.Options{}
	if len(dependencies) > 0 {
		options = dependencies[0]
	}
	options.ConfigPath = "bareplane.yaml"
	if flags.NArg() == 1 {
		options.ConfigPath = flags.Arg(0)
	}
	options.Approval, options.Check = *approval, *check
	options.Event = func(event bootstrapapply.Event) {
		fmt.Fprintf(stdout, "%-5s %-22s %s\n", strings.ToUpper(event.Status), event.Phase, event.Message)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := bootstrapapply.Apply(ctx, options); err != nil {
		fmt.Fprintf(stderr, "bootstrap apply: %v\n", err)
		if errors.Is(err, bootstrapapply.ErrCheckUnsupported) {
			return 2
		}
		var phaseError *bootstrapapply.PhaseError
		if errors.As(err, &phaseError) {
			fmt.Fprintf(stderr, "After inspecting recovery state, retry: bareplane bootstrap apply --approve %s %s\n", shellQuote(*approval), shellQuote(options.ConfigPath))
		}
		return 1
	}
	fmt.Fprintln(stdout, "bootstrap cluster is healthy; private progress and kubeconfig are ready")
	return 0
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
