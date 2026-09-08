package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/bootstrapapply"
	"github.com/dipeshbabu/bareplane/internal/bootstrapcheck"
	"github.com/dipeshbabu/bareplane/internal/bootstrapdoctor"
	"github.com/dipeshbabu/bareplane/internal/bootstrappreflight"
	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/project"
	ansiblerender "github.com/dipeshbabu/bareplane/internal/render/ansible"
)

const bootstrapUsage = `Usage:
  bareplane bootstrap render [path]
  bareplane bootstrap doctor [path]
  bareplane bootstrap check [path]
  bareplane bootstrap trust [--rotate] [path]
  bareplane bootstrap preflight [path]
  bareplane bootstrap apply --approve <cluster-name> [path]
  bareplane bootstrap diagnose [path]
  bareplane bootstrap reset --approve <cluster-name> --scope cluster --confirm-destructive [path]
  bareplane bootstrap recover-kubeconfig --approve <cluster-name> [path]
  bareplane bootstrap kubelet-tls --approve <cluster-name> [path]

Commands:
  render     Render the deterministic Ansible bootstrap bundle offline
  doctor     Check local bootstrap inventory, SSH key, and tooling readiness
  check      Check remote TCP reachability and SSH service identification only
  trust      Review and explicitly trust remote SSH host identities
  preflight  Authenticate and verify read-only remote host readiness
  apply      Run owned bootstrap phases with approval, locking, and safe resume
  diagnose   Inspect local progress, operation locks, and remote ownership markers
  reset      Explicitly reset and reboot a verified bootstrap-only cluster
  recover-kubeconfig  Recover a lost project credential and verify health
  kubelet-tls  Enable or renew inventory-verified kubelet serving certificates
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
	case "doctor":
		return runBootstrapDoctor(args[1:], stdout, stderr, exec.LookPath, os.UserHomeDir)
	case "check":
		return runBootstrapCheck(args[1:], stdout, stderr)
	case "trust":
		return runBootstrapTrust(args[1:], os.Stdin, stdout, stderr)
	case "preflight":
		return runBootstrapPreflight(args[1:], stdout, stderr)
	case "apply":
		return runBootstrapApply(args[1:], stdout, stderr)
	case "diagnose":
		return runBootstrapDiagnose(args[1:], stdout, stderr)
	case "reset":
		return runBootstrapReset(args[1:], stdout, stderr)
	case "recover-kubeconfig":
		return runBootstrapApply(args[1:], stdout, stderr, bootstrapapply.Options{RecoverCredentials: true})
	case "kubelet-tls":
		return runBootstrapApply(args[1:], stdout, stderr, bootstrapapply.Options{KubeletServingTLS: true})
	default:
		fmt.Fprintf(stderr, "unknown bootstrap command %q\n\n%s", args[0], bootstrapUsage)
		return 2
	}
}

func runBootstrapPreflight(args []string, stdout, stderr io.Writer, runners ...bootstrappreflight.Runner) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: bareplane bootstrap preflight [path]")
		return 2
	}
	configPath := "bareplane.yaml"
	if len(args) == 1 {
		configPath = args[0]
	}
	var runner bootstrappreflight.Runner
	if len(runners) > 0 {
		runner = runners[0]
	}
	report := bootstrappreflight.Inspect(context.Background(), bootstrappreflight.Options{ConfigPath: configPath, Runner: runner})
	for _, result := range report.Results {
		fmt.Fprintf(stdout, "%-4s  %-28s %s\n", strings.ToUpper(string(result.Status)), result.Name, result.Message)
	}
	if report.HasFailures() {
		return 1
	}
	return 0
}

func runBootstrapCheck(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: bareplane bootstrap check [path]")
		return 2
	}
	configPath := "bareplane.yaml"
	if len(args) == 1 {
		configPath = args[0]
	}

	report := bootstrapcheck.Check(context.Background(), bootstrapcheck.Options{ConfigPath: configPath})
	for _, result := range report.Results {
		fmt.Fprintf(stdout, "%-4s  %-28s %s\n", strings.ToUpper(string(result.Status)), result.Name, result.Message)
	}
	if report.HasFailures() {
		return 1
	}
	return 0
}

func runBootstrapDoctor(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	lookPath bootstrapdoctor.LookPathFunc,
	homeDir bootstrapdoctor.UserHomeDirFunc,
) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: bareplane bootstrap doctor [path]")
		return 2
	}
	configPath := "bareplane.yaml"
	if len(args) == 1 {
		configPath = args[0]
	}

	report := bootstrapdoctor.Inspect(bootstrapdoctor.Options{
		ConfigPath:  configPath,
		LookPath:    lookPath,
		UserHomeDir: homeDir,
	})
	for _, result := range report.Results {
		fmt.Fprintf(stdout, "%-4s  %-18s %s\n", strings.ToUpper(string(result.Status)), result.Name, result.Message)
	}
	if report.HasFailures() {
		return 1
	}
	return 0
}

func runBootstrapRender(args []string, stdout, stderr io.Writer) (code int) {
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

	files, err := ansiblerender.RenderBundle(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "bootstrap render %s: %v\n", configPath, err)
		return 1
	}
	lock, err := project.AcquireBootstrapOperation(configPath, "render")
	if err != nil {
		fmt.Fprintf(stderr, "bootstrap render %s: %v\n", configPath, err)
		return 1
	}
	defer func() {
		if err := lock.Release(); err != nil {
			fmt.Fprintf(stderr, "bootstrap render: release operation lock: %v\n", err)
			code = 1
		}
	}()
	destination := filepath.Join(filepath.Dir(filepath.Clean(configPath)), ".bareplane", "bootstrap")
	if err := project.ReplaceGeneratedTree(destination, "bootstrap", files); err != nil {
		if errors.Is(err, project.ErrUnmanagedDestination) {
			fmt.Fprintf(stderr, "bootstrap render %s: refusing to replace unmanaged output directory %s\n", configPath, destination)
			return 1
		}
		fmt.Fprintf(stderr, "bootstrap render %s: write generated bundle: %v\n", configPath, err)
		return 1
	}

	fmt.Fprintf(stdout, "rendered bootstrap bundle to %s\n", destination)
	return 0
}
