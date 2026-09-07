package bootstrappreflight

import (
	"strings"
	"testing"

	"github.com/dipeshbabu/bareplane/internal/doctor"
	"github.com/dipeshbabu/bareplane/internal/topology"
)

func TestApprovedPreparationAndResumePoliciesRemainSeparate(t *testing.T) {
	facts := readyFacts()
	facts.SwapBytes = 1024
	machine := topology.Machine{Name: "lab-control-1", Role: "control-plane", CPU: 2, MemoryGB: 2, DiskGB: 10}
	if evaluate(machine, facts, false).Status != doctor.StatusFail {
		t.Fatal("standalone preflight silently allowed swap")
	}
	if evaluateWithPolicy(machine, facts, false, true, false).Status == doctor.StatusFail {
		t.Fatal("approved preparation cannot disable ordinary swap")
	}
	facts.ClusterState = true
	if evaluateWithPolicy(machine, facts, false, true, false).Status != doctor.StatusFail {
		t.Fatal("fresh preparation adopted cluster state")
	}
	if evaluateWithPolicy(machine, facts, false, false, true).Status != doctor.StatusFail {
		t.Fatal("resume allowed swap after preparation")
	}
	facts.SwapBytes = 0
	if evaluateWithPolicy(machine, facts, false, false, true).Status == doctor.StatusFail {
		t.Fatal("recorded resume cannot reach phase ownership guards")
	}
}

func TestSSHPathsEscapeQuotesSpacesAndPercentTokens(t *testing.T) {
	request := Request{KnownHostsFile: "/project 'quoted' %h/known_hosts", PrivateKeyFile: "/private key %h", User: "debian", Host: "192.0.2.11", Port: 22}
	args := sshArguments(request, 5)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, `UserKnownHostsFile="/project 'quoted' %%h/known_hosts"`) || !strings.Contains(joined, "-i /private key %%h") {
		t.Fatalf("SSH paths were not escaped: %#v", args)
	}
}
