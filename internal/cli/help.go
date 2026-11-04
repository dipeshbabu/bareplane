package cli

import "strings"

// Resolve explicit leaf-command help before dispatch can access project files,
// credentials, tooling, or remote machines. Other argument shapes still pass
// through the command's normal validation.
func commandHelp(args []string) (string, bool) {
	if len(args) < 2 || len(args) > 3 {
		return "", false
	}
	flag := args[len(args)-1]
	if flag != "--help" && flag != "-h" {
		return "", false
	}
	command := strings.ToLower(args[0])
	if len(args) == 2 {
		switch command {
		case "init", "validate", "status", "doctor", "plan", "render":
			return "Usage:\n  bareplane " + command + " [path]\n", true
		case "version":
			return "Usage:\n  bareplane version\n", true
		}
		return "", false
	}
	var groupUsage string
	switch command {
	case "bootstrap":
		groupUsage = bootstrapUsage
	case "terraform":
		groupUsage = terraformUsage
	case "gitops":
		groupUsage = gitOpsUsage
	default:
		return "", false
	}
	prefix := "  bareplane " + command + " " + strings.ToLower(args[1]) + " "
	for _, line := range strings.Split(groupUsage, "\n") {
		if strings.HasPrefix(line, prefix) {
			return "Usage:\n" + line + "\n", true
		}
	}
	return "", false
}
