package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/version"
)

const usage = `Bareplane builds and operates private Kubernetes platforms on hardware you own.

Usage:
  bareplane <command>

Commands:
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
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
