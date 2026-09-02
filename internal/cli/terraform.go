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
  bareplane terraform apply --approve <cluster-name> [path]

Commands:
  plan       Run a read-only Terraform plan against generated infrastructure
  apply      Apply only the current attested saved plan with explicit approval
`

type terraformPlanFunc func(context.Context, terraformexec.PlanOptions) (terraformexec.PlanResult, error)
type terraformApplyFunc func(context.Context, terraformexec.ApplyOptions) (terraformexec.ApplyResult, error)
type lookPathFunc func(string) (string, error)

func runTerraform(args []string, stdout, stderr io.Writer) int {
	return runTerraformWith(args, stdout, stderr, os.LookupEnv, exec.LookPath, terraformexec.Plan, terraformexec.Apply)
}

func runTerraformWith(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	lookupEnv proxmox.LookupEnvFunc,
	lookPath lookPathFunc,
	plan terraformPlanFunc,
	applyFns ...terraformApplyFunc,
) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, terraformUsage)
		return 0
	}

	var apply terraformApplyFunc
	if len(applyFns) > 0 {
		apply = applyFns[0]
	}

	switch strings.ToLower(args[0]) {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, terraformUsage)
		return 0
	case "plan":
		return runTerraformPlan(args[1:], stdout, stderr, lookupEnv, lookPath, plan)
	case "apply":
		if apply == nil {
			fmt.Fprintf(stderr, "unknown terraform command %q\n\n%s", args[0], terraformUsage)
			return 2
		}
		return runTerraformApply(args[1:], stdout, stderr, lookupEnv, lookPath, apply)
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

func runTerraformApply(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	lookupEnv proxmox.LookupEnvFunc,
	lookPath lookPathFunc,
	apply terraformApplyFunc,
) int {
	if (len(args) != 2 && len(args) != 3) || args[0] != "--approve" || strings.TrimSpace(args[1]) == "" {
		fmt.Fprintln(stderr, "usage: bareplane terraform apply --approve <cluster-name> [path]")
		return 2
	}

	approval := args[1]
	configPath := "bareplane.yaml"
	if len(args) == 3 {
		configPath = args[2]
	}

	credentials, err := proxmox.CredentialsFromEnv(lookupEnv)
	if err != nil {
		fmt.Fprintf(stderr, "terraform apply credentials: %v\n", err)
		return 1
	}
	terraformBinary, err := lookPath("terraform")
	if err != nil {
		fmt.Fprintf(stderr, "terraform apply: terraform executable not found: %v\n", err)
		return 1
	}

	result, err := apply(context.Background(), terraformexec.ApplyOptions{
		ConfigPath:      configPath,
		Approval:        approval,
		TerraformBinary: terraformBinary,
		Credentials:     credentials,
		BaseEnvironment: os.Environ(),
		Stdout:          stdout,
		Stderr:          stderr,
	})
	if err != nil {
		fmt.Fprintf(stderr, "terraform apply: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Bareplane Terraform apply completed for cluster %q; run a new plan before any further apply\n", result.Cluster)
	return 0
}
