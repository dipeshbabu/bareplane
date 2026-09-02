package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/project"
	ansiblerender "github.com/dipeshbabu/bareplane/internal/render/ansible"
)

const bootstrapUsage = `Usage:
  bareplane bootstrap render [path]

Commands:
  render     Render deterministic Ansible inventory without connecting to hosts
`

func runBootstrap(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, bootstrapUsage)
		return 0
	}
	switch strings.ToLower(args[0]) {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, bootstrapUsage)
		return 0
	case "render":
		return runBootstrapRender(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown bootstrap command %q\n\n%s", args[0], bootstrapUsage)
		return 2
	}
}

func runBootstrapRender(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: bareplane bootstrap render [path]")
		return 2
	}
	configPath := "bareplane.yaml"
	if len(args) == 1 {
		configPath = args[0]
	}

	file, err := os.Open(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "bootstrap render %s: open configuration: %v\n", configPath, err)
		return 1
	}
	cfg, loadErr := config.Load(file)
	closeErr := file.Close()
	if loadErr != nil {
		fmt.Fprintf(stderr, "bootstrap render %s: load configuration: %v\n", configPath, loadErr)
		return 1
	}
	if closeErr != nil {
		fmt.Fprintf(stderr, "bootstrap render %s: close configuration: %v\n", configPath, closeErr)
		return 1
	}

	inventory, err := ansiblerender.RenderInventory(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "bootstrap render %s: %v\n", configPath, err)
		return 1
	}
	destination := filepath.Join(filepath.Dir(filepath.Clean(configPath)), ".bareplane", "bootstrap")
	if err := project.ReplaceGeneratedDirectory(destination, "bootstrap", map[string][]byte{
		ansiblerender.InventoryFilename: inventory,
	}); err != nil {
		if errors.Is(err, project.ErrUnmanagedDestination) {
			fmt.Fprintf(stderr, "bootstrap render %s: refusing to replace unmanaged output directory %s\n", configPath, destination)
			return 1
		}
		fmt.Fprintf(stderr, "bootstrap render %s: write generated inventory: %v\n", configPath, err)
		return 1
	}

	fmt.Fprintf(stdout, "rendered bootstrap inventory to %s\n", destination)
	return 0
}
