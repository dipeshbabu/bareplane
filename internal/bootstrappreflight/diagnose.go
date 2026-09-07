package bootstrappreflight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/doctor"
	"github.com/dipeshbabu/bareplane/internal/sshtrust"
	"github.com/dipeshbabu/bareplane/internal/topology"
)

// Diagnose reads only bounded marker/service summaries over authenticated SSH.
// It does not require a working API or valid generated Ansible workspace.
func Diagnose(ctx context.Context, options Options) doctor.Report {
	if ctx == nil {
		return failedReport("diagnose", "diagnostic context is nil")
	}
	if options.ConfigPath == "" {
		options.ConfigPath = "bareplane.yaml"
	}
	if options.Timeout <= 0 {
		options.Timeout = DefaultTimeout
	}
	if options.Timeout > MaximumTimeout {
		return failedReport("diagnose", "diagnostic timeout exceeds the supported bound")
	}
	cfg, err := loadConfig(options.ConfigPath)
	if err != nil {
		return failedReport("configuration", "repair bootstrap configuration before remote diagnosis")
	}
	resolve := options.ResolveKnownHosts
	if resolve == nil {
		resolve = sshtrust.RequireKnownHosts
	}
	trust, err := resolve(options.ConfigPath)
	if err != nil {
		return failedReport("ssh-known-hosts", err.Error())
	}
	if strings.Contains(trust, "${") || strings.ContainsAny(trust, "\x00\r\n\t") {
		return failedReport("ssh-known-hosts", "unsupported known-hosts path")
	}
	key, err := resolvePrivateKey(options.ConfigPath, cfg.Spec.Bootstrap.SSH.PrivateKeyFile, options.UserHomeDir)
	if err != nil {
		return failedReport("ssh-private-key", err.Error())
	}
	runner := options.Runner
	if runner == nil {
		binary, err := exec.LookPath("ssh")
		if err != nil {
			return failedReport("ssh", "ssh executable was not found")
		}
		runner = fixedScriptRunner(binary, remoteDiagnosisScript)
	}
	topo, err := topology.Build(cfg)
	if err != nil {
		return failedReport("topology", "invalid desired bootstrap topology")
	}
	sort.Slice(topo.Machines, func(i, j int) bool { return topo.Machines[i].Name < topo.Machines[j].Name })
	results := make([]doctor.Result, 0, len(topo.Machines))
	for _, machine := range topo.Machines {
		probe, cancel := context.WithTimeout(ctx, options.Timeout)
		output, runErr := runner(probe, Request{Machine: machine, Host: cfg.Spec.Bootstrap.SSH.Hosts[machine.Name],
			Port: cfg.Spec.Bootstrap.SSH.EffectivePort(), User: cfg.Spec.Bootstrap.SSH.User, PrivateKeyFile: key, KnownHostsFile: trust})
		contextErr := probe.Err()
		cancel()
		if runErr != nil || contextErr != nil {
			results = append(results, doctor.Result{Name: machine.Name, Status: doctor.StatusFail, Message: safeRunnerError(runErr, contextErr).Error()})
			continue
		}
		result, err := summarizeDiagnosis(output, cfg.Metadata.Name)
		if err != nil {
			result = doctor.Result{Status: doctor.StatusFail, Message: "remote diagnosis returned an invalid bounded summary"}
		}
		result.Name = machine.Name
		results = append(results, result)
	}
	return doctor.Report{Results: results}
}

func summarizeDiagnosis(data []byte, cluster string) (doctor.Result, error) {
	if len(data) > MaximumOutputSize {
		return doctor.Result{}, errors.New("oversized diagnosis")
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 11 || lines[0] != "BAREPLANE_DIAGNOSIS_V1" {
		return doctor.Result{}, errors.New("invalid diagnosis protocol")
	}
	values := map[string]string{}
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, "=")
		if !ok || len(line) > 128 || values[key] != "" {
			return doctor.Result{}, errors.New("invalid diagnosis field")
		}
		values[key] = value
	}
	for _, key := range []string{"init_intent", "init_complete", "join_intent", "join_complete", "cilium_intent", "cilium_complete", "reset_receipt", "kubelet_active"} {
		if _, ok := parseBool(values[key]); !ok {
			return doctor.Result{}, errors.New("invalid diagnosis flag")
		}
	}
	marker := values["init_marker_digest"]
	if marker != "none" {
		decoded, err := hex.DecodeString(marker)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != marker {
			return doctor.Result{}, errors.New("invalid marker digest")
		}
	}
	if values["ca_record_match"] != "none" && values["ca_record_match"] != "true" && values["ca_record_match"] != "false" {
		return doctor.Result{}, errors.New("invalid CA summary")
	}
	want := sha256.Sum256([]byte(cluster + "\n"))
	if (marker != "none" && marker != hex.EncodeToString(want[:])) || values["ca_record_match"] == "false" {
		return doctor.Result{Status: doctor.StatusFail, Message: "cluster ownership or CA record differs; no automatic recovery is allowed"}, nil
	}
	state := func(prefix string) string {
		if values[prefix+"_complete"] == "true" {
			return "complete"
		}
		if values[prefix+"_intent"] == "true" {
			return "incomplete"
		}
		return "absent"
	}
	status := doctor.StatusPass
	message := fmt.Sprintf("init=%s join=%s cilium=%s kubelet-active=%s", state("init"), state("join"), state("cilium"), values["kubelet_active"])
	if state("init") == "incomplete" || state("join") == "incomplete" || state("cilium") == "incomplete" {
		status = doctor.StatusWarn
		message += "; inspect private phase logs and explicit recovery before retrying"
	}
	if values["reset_receipt"] == "true" {
		status = doctor.StatusWarn
		message += "; reset receipt is present; finish or verify the recorded reset"
	}
	return doctor.Result{Status: status, Message: message}, nil
}

const remoteDiagnosisScript = `set -u
sudo -n true >/dev/null 2>&1 || exit 1
printf '%s\n' 'BAREPLANE_DIAGNOSIS_V1'
present() { if sudo -n test -f "$1"; then printf 'true'; else printf 'false'; fi; }
printf 'init_intent=%s\n' "$(present /var/lib/bareplane/bootstrap/init-intent)"
printf 'init_complete=%s\n' "$(present /etc/kubernetes/.bareplane-init-complete)"
printf 'join_intent=%s\n' "$(present /var/lib/bareplane/bootstrap/join-intent)"
printf 'join_complete=%s\n' "$(present /var/lib/bareplane/bootstrap/join-complete)"
printf 'cilium_intent=%s\n' "$(present /var/lib/bareplane/bootstrap/cilium-intent)"
printf 'cilium_complete=%s\n' "$(present /var/lib/bareplane/bootstrap/cilium-complete)"
printf 'reset_receipt=%s\n' "$(present /var/lib/bareplane/bootstrap/reset-receipt.json)"
if systemctl is-active --quiet kubelet; then printf 'kubelet_active=true\n'; else printf 'kubelet_active=false\n'; fi
marker=none
if sudo -n test -f /etc/kubernetes/.bareplane-init-complete; then
  marker=$(sudo -n sha256sum /etc/kubernetes/.bareplane-init-complete | cut -d ' ' -f 1)
fi
printf 'init_marker_digest=%s\n' "$marker"
match=none
if sudo -n test -f /var/lib/bareplane/bootstrap/init-ca-sha256; then
  ca=$(sudo -n sha256sum /etc/kubernetes/pki/ca.crt 2>/dev/null | cut -d ' ' -f 1)
  record=$(sudo -n head -c 65 /var/lib/bareplane/bootstrap/init-ca-sha256)
  if [ -n "$ca" ] && [ "$ca" = "$record" ]; then match=true; else match=false; fi
fi
printf 'ca_record_match=%s\n' "$match"
`
