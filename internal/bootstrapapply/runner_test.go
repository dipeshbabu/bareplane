package bootstrapapply

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunnerAcceptsOnlyOwnedPhasesAndControlledArguments(t *testing.T) {
	request := Request{Phase: "join", BundleDir: "/project 'quoted' %h/bootstrap", PrivateKeyFile: "/private/key %h", KnownHostsFile: "/project/state/known_hosts"}
	args, err := phaseArguments(request)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--inventory", filepath.Join(request.BundleDir, "inventory.yaml"), "--private-key", "/private/key %%h", "join.yaml"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	for _, phase := range []string{"site", "../../custom", "join.yaml", "join; reset", "--extra-vars"} {
		request.Phase = phase
		if _, err := phaseArguments(request); err == nil {
			t.Fatalf("unowned phase %q accepted", phase)
		}
	}
}

func TestRunnerDropsEnvironmentOverridesAndProviderCredentials(t *testing.T) {
	request := Request{BundleDir: "/owned/bootstrap", StateDir: "/owned/state"}
	env := controlledEnvironment([]string{"PATH=/bin", "HOME=/controller", "USER=operator", "KUBECONFIG=/global/config",
		"ANSIBLE_CONFIG=/unowned", "ANSIBLE_DEBUG=True", "ANSIBLE_CALLBACK_PLUGINS=/unowned", "PYTHONPATH=/unowned",
		"SSH_AUTH_SOCK=/agent", "PROXMOX_API_TOKEN=SECRET", "HTTP_PROXY=SECRET"}, request)
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"SECRET", "/unowned", "KUBECONFIG", "SSH_AUTH_SOCK", "PYTHONPATH"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("unsafe environment survived: %s", forbidden)
		}
	}
	for _, required := range []string{"PATH=/bin", "HOME=/controller", "ANSIBLE_HOST_KEY_CHECKING=True", "ANSIBLE_DEBUG=False", "ANSIBLE_STDOUT_CALLBACK=default"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing environment control %s", required)
		}
	}
}

func TestControllerToolVersionPins(t *testing.T) {
	for _, test := range []struct {
		tool, output string
		valid        bool
	}{
		{"ansible-playbook", "ansible-playbook [core 2.19.9]\n", true},
		{"ansible-playbook", "ansible-playbook [core 2.20.0]\n", false},
		{"openssl", "OpenSSL 3.0.13", true}, {"openssl", "LibreSSL 3.0.0", false},
		{"kubectl", `{"clientVersion":{"gitVersion":"v1.36.4"}}`, true},
		{"kubectl", `{"clientVersion":{"gitVersion":"v1.36.3"}}`, false},
		{"kubectl", "malformed", false},
	} {
		if got := supportedToolVersion(test.tool, []byte(test.output), "1.36.4"); got != test.valid {
			t.Fatalf("version %s: %v", test.tool, got)
		}
	}
}

func TestLogsAreBoundedWithoutReturningSecretOutput(t *testing.T) {
	var output bytes.Buffer
	writer := &limitedLog{writer: &output, remaining: 4}
	for _, value := range []string{"123456", "private overflow"} {
		if written, err := writer.Write([]byte(value)); err != nil || written != len(value) {
			t.Fatalf("write = %d %v", written, err)
		}
	}
	if output.String() != "1234" || writer.remaining != 0 {
		t.Fatalf("unbounded log: %q", output.String())
	}
}

func TestControlledAnsibleIntegration(t *testing.T) {
	binary, err := exec.LookPath("ansible-playbook")
	if err != nil {
		if os.Getenv("BAREPLANE_TEST_ANSIBLE") == "1" {
			t.Fatal(err)
		}
		t.Skip("ansible-playbook is not installed")
	}
	f := newFixture(t)
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	request := Request{Phase: "health", BundleDir: f.bundle, StateDir: filepath.Dir(f.trust), PrivateKeyFile: f.key, KnownHostsFile: f.trust, Log: &output}
	err = commandRunner(binary)(ctx, request)
	if err == nil || ctx.Err() != nil || !strings.Contains(output.String(), "Private kubeconfig is unavailable") || strings.Contains(output.String(), "UNREACHABLE") {
		t.Fatalf("controlled controller-only refusal failed: %v\n%s", err, output.String())
	}
	if strings.Contains(output.String(), "PRIVATE-FIXTURE-NOT-A-REAL-KEY") {
		t.Fatal("private key leaked")
	}
	if _, _, err := verifyBundle(f.path, f.cfg); err != nil {
		t.Fatalf("Ansible changed the owned bundle: %v", err)
	}
}
