package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/planner"
	"github.com/dipeshbabu/bareplane/internal/provider"
	"github.com/dipeshbabu/bareplane/internal/runtime"
	"github.com/dipeshbabu/bareplane/internal/topology"
)

const planConflictExitCode = 3

func runPlan(args []string, stdout, stderr io.Writer, deps runtime.ProviderDependencies) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: bareplane plan [path]")
		return 2
	}

	path := "bareplane.yaml"
	if len(args) == 1 {
		path = args[0]
	}

	cfg, err := loadConfig(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	desired, err := topology.Build(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "build desired topology: %v\n", err)
		return 1
	}
	discoverer, err := runtime.Discoverer(cfg, deps)
	if err != nil {
		fmt.Fprintf(stderr, "initialize provider discovery: %v\n", err)
		return 1
	}
	observed, err := discoverer.Discover(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "discover infrastructure: %v\n", err)
		return 1
	}
	plan, err := planner.Build(desired, observed)
	if err != nil {
		fmt.Fprintf(stderr, "build infrastructure plan: %v\n", err)
		return 1
	}

	renderPlan(stdout, plan)
	if plan.HasConflicts() {
		return planConflictExitCode
	}
	return 0
}

func loadConfig(path string) (config.Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return config.Config{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	cfg, err := config.Load(file)
	if err != nil {
		return config.Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func renderPlan(w io.Writer, plan provider.Plan) {
	counts := map[provider.Action]int{}
	for _, change := range plan.Changes {
		id := change.ID
		if id == "" {
			id = "-"
		}
		fmt.Fprintf(
			w,
			"%-8s %-32s %-12s %s\n",
			strings.ToUpper(string(change.Action)),
			change.Name,
			id,
			change.Reason,
		)
		counts[change.Action]++
	}

	fmt.Fprintf(
		w,
		"\nSummary: %d create, %d update, %d noop, %d conflict\n",
		counts[provider.ActionCreate],
		counts[provider.ActionUpdate],
		counts[provider.ActionNoop],
		counts[provider.ActionConflict],
	)
}
