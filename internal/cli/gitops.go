package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/project"
	gitopsrender "github.com/dipeshbabu/bareplane/internal/render/gitops"
)

const gitOpsUsage = `Usage:
  bareplane gitops render [path]

Render a public, reviewable GitOps export beside bareplane.yaml in gitops/.
No Kubernetes access, Git commit, or push occurs. Copy the reviewed payload to
your own repository before handoff; edited or unmanaged exports are preserved.
`

func runGitOps(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")) {
		fmt.Fprint(stdout, gitOpsUsage)
		return 0
	}
	if strings.ToLower(args[0]) != "render" || len(args) > 2 || (len(args) == 2 && strings.HasPrefix(args[1], "-")) {
		fmt.Fprint(stderr, gitOpsUsage)
		return 2
	}
	configPath := "bareplane.yaml"
	if len(args) == 2 {
		configPath = args[1]
	}
	file, err := os.Open(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "gitops render: open configuration: %v\n", err)
		return 1
	}
	cfg, loadErr := config.Load(file)
	if err := errors.Join(loadErr, file.Close()); err != nil {
		fmt.Fprintf(stderr, "gitops render: load configuration: %v\n", err)
		return 1
	}
	files, err := gitopsrender.Render(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "gitops render: %v\n", err)
		return 1
	}
	destination, err := project.WriteGitOpsExport(configPath, cfg.Metadata.Name, files)
	if err != nil {
		fmt.Fprintf(stderr, "gitops render: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "rendered GitOps export to %s; review and copy the payload to your repository (no commit, push, or cluster mutation performed)\n", destination)
	return 0
}
