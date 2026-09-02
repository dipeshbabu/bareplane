package cli

import (
	"fmt"
	"io"

	"github.com/dipeshbabu/bareplane/internal/projectstatus"
)

func runStatus(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: bareplane status [path]")
		return 2
	}
	path := "bareplane.yaml"
	if len(args) == 1 {
		path = args[0]
	}

	report, err := projectstatus.Inspect(path)
	if err != nil {
		fmt.Fprintf(stderr, "status: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "cluster: %s\n", report.Cluster)
	fmt.Fprintf(stdout, "provisioning-ready: %t\n", report.ProvisioningReady)
	fmt.Fprintf(stdout, "rendered: %t\n", report.Rendered)
	fmt.Fprintf(stdout, "terraform-state: %t\n", report.StatePresent)
	fmt.Fprintf(stdout, "terraform-state-backup: %t\n", report.StateBackupPresent)
	fmt.Fprintf(stdout, "terraform-lock: %t\n", report.LockPresent)
	fmt.Fprintf(stdout, "saved-plan: %t\n", report.PlanPresent)
	fmt.Fprintf(stdout, "plan-attestation: %t\n", report.ManifestPresent)
	if report.Operation.Present {
		fmt.Fprintf(stdout, "operation: %s (pid %d)\n", report.Operation.Operation, report.Operation.PID)
	} else {
		fmt.Fprintln(stdout, "operation: none")
	}
	fmt.Fprintf(stdout, "next: %s\n", report.Next)
	return 0
}
