package terraformexec

import (
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/provider/proxmox"
)

func TestControlledTerraformEnvironmentRemovesUnsafeOverrides(t *testing.T) {
	credentials := proxmox.Credentials{TokenID: "user@pve!bareplane", TokenSecret: "secret"}
	environment := terraformEnvironment([]string{
		"PATH=/bin",
		"TF_CLI_ARGS=-chdir=/tmp",
		"TF_CLI_ARGS_apply=-destroy",
		"TF_WORKSPACE=other",
		"TF_VAR_injected=value",
		"TF_LOG=TRACE",
		"TF_LOG_PATH=/tmp/terraform-sensitive.log",
		"TF_LOG_PROVIDER=TRACE",
		proxmox.EnvTokenID + "=stale-id",
		proxmox.EnvTokenSecret + "=stale-secret",
		"PROXMOX_VE_INSECURE=true",
		"PROXMOX_VE_ENDPOINT=http://example.invalid",
		providerTokenEnv + "=stale-token",
		"TF_DATA_DIR=stale-data",
	}, "/private/data", credentials)

	if got := envValue(environment, "PATH"); got != "/bin" {
		t.Fatalf("PATH = %q", got)
	}
	if got := envValue(environment, "TF_DATA_DIR"); got != "/private/data" {
		t.Fatalf("TF_DATA_DIR = %q", got)
	}
	if got := envValue(environment, providerTokenEnv); got != "user@pve!bareplane=secret" {
		t.Fatalf("provider token = %q", got)
	}

	for _, entry := range environment {
		key := strings.ToUpper(environmentKey(entry))
		if key == "TF_WORKSPACE" || key == "TF_CLI_ARGS" || strings.HasPrefix(key, "TF_CLI_ARGS_") || strings.HasPrefix(key, "TF_VAR_") || strings.HasPrefix(key, "TF_LOG") {
			t.Fatalf("unsafe Terraform environment key survived: %q", key)
		}
		if key == proxmox.EnvTokenID || key == proxmox.EnvTokenSecret {
			t.Fatalf("Bareplane token component leaked to Terraform: %q", key)
		}
		if strings.HasPrefix(key, "PROXMOX_VE_") && key != providerTokenEnv {
			t.Fatalf("uncontrolled Proxmox environment key survived: %q", key)
		}
	}
}

func TestTerraformVersionEnvironmentContainsNoProviderCredential(t *testing.T) {
	environment := terraformVersionEnvironment([]string{
		"PATH=/bin",
		proxmox.EnvTokenID + "=id",
		proxmox.EnvTokenSecret + "=secret",
		providerTokenEnv + "=combined",
		"PROXMOX_VE_INSECURE=true",
	}, "/private/data")

	for _, key := range []string{proxmox.EnvTokenID, proxmox.EnvTokenSecret, providerTokenEnv, "PROXMOX_VE_INSECURE"} {
		if got := envValue(environment, key); got != "" {
			t.Fatalf("credential/control environment %s leaked to version probe", key)
		}
	}
}
