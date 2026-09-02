package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/project"
	"github.com/dipeshbabu/bareplane/internal/version"
)

const usage = `Bareplane builds and operates private Kubernetes platforms on hardware you own.

Usage:
  bareplane <command>

Commands:
  init       Create a starter bareplane.yaml
  validate   Validate bareplane.yaml
  version    Print build version information
  help       Show this help text
`

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return 0
	}

	switch strings.ToLower(args[0]) {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version", "-v", "--version":
		fmt.Fprintln(stdout, version.String())
		return 0
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func runInit(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: bareplane init [path]")
		return 2
	}

	path := "bareplane.yaml"
	if len(args) == 1 {
		path = args[0]
	}

	if err := project.Init(path); err != nil {
		if errors.Is(err, project.ErrAlreadyExists) {
			fmt.Fprintf(stderr, "%s already exists; refusing to overwrite it\n", path)
			return 1
		}
		fmt.Fprintf(stderr, "initialize %s: %v\n", path, err)
		return 1
	}

	fmt.Fprintf(stdout, "created %s\n", path)
	return 0
}

func runValidate(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: bareplane validate [path]")
		return 2
	}

	path := "bareplane.yaml"
	if len(args) == 1 {
		path = args[0]
	}

	file, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(stderr, "open %s: %v\n", path, err)
		return 1
	}
	defer file.Close()

	cfg, err := config.Load(file)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", path, err)
		return 1
	}

	fmt.Fprintf(stdout, "%s is valid for cluster %q\n", path, cfg.Metadata.Name)
	return 0
}
