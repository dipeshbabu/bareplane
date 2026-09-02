package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/version"
)

const usage = `Bareplane builds and operates private Kubernetes platforms on hardware you own.

Usage:
  bareplane <command>

Commands:
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
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
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
