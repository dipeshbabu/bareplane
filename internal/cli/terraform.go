package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/provider/proxmox"
	"github.com/dipeshbabu/bareplane/internal/terraformexec"
)

const terraformUsage = `Usage:
  bareplane terraform plan [path]

Commands:
  plan       Run a read-only Terraform plan against generated infrastructure
`

type terraformPlanFunc func(context.Context, terraformexec.PlanOptions) (terraformexec.PlanResult, error)
type lookPathFunc func(string) (string, error)

func runTerraform(args []string, stdout, stderr io.Writer) int {
	return runTerraformWith(args, stdout, stderr, os.LookupEnv, exec.LookPath, terraformexec.Plan)
}

func runTerraformWith(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	lookupEnv proxmox.LookupEnvFunc,
	lookPath lookPathFunc,
	plan terraformPlanFunc,
) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, terraformUsage)
		return 0
	}

	switch strings.ToLower(args[0]) {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, terraformUsage)
		return 0
	case "plan":
		return runTerraformPlan(args[1:], stdout, stderr, lookupEnv, lookPath, plan)
	default:
		fmt.Fprintf(stderr, "unknown terraform command %q\n\n%s", args[0], terraformUsage)
		return 2
	}
}

func runTerraformPlan(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	lookupEnv proxmox.LookupEnvFunc,
	lookPath lookPathFunc,
	plan terraformPlanFunc,
) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: bareplane terraform plan [path]")
		return 2
	}

	configPath := "bareplane.yaml"
	if len(args) == 1 {
		configPath = args[0]
	}

	credentials, err := proxmox.CredentialsFromEnv(lookupEnv)
	if err != nil {
		fmt.Fprintf(stderr, "terraform plan credentials: %v\n", err)
		return 1
	}
	terraformBinary, err := lookPath("terraform")
	if err != nil {
		fmt.Fprintf(stderr, "terraform plan: terraform executable not found: %v\n", err)
		return 1
	}

	result, err := plan(context.Background(), terraformexec.PlanOptions{
		ConfigPath:      configPath,
		TerraformBinary: terraformBinary,
		Credentials:     credentials,
		BaseEnvironment: os.Environ(),
		Stdout:          stdout,
		Stderr:          stderr,
	})
	if err != nil {
		fmt.Fprintf(stderr, "terraform plan: %v\n", err)
		return 1
	}
	if result.Changes {
		fmt.Fprintln(stdout, "Bareplane Terraform plan completed: changes present")
	} else {
		fmt.Fprintln(stdout, "Bareplane Terraform plan completed: no changes")
	}
	return 0
}
