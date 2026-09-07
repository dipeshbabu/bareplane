# Guarded bootstrap apply

`bareplane bootstrap apply --approve <cluster-name> [path]` runs only the current Bareplane-owned Ansible phases. It is not an Ansible argument passthrough and never invokes Terraform, resets Kubernetes, or adopts an untracked cluster.

Use a Linux/WSL controller with `ansible-core==2.19.9`, OpenSSL 3, SSH tooling, and kubectl at the exact configured Kubernetes version. Authentication must work non-interactively with the configured private key and sudo; SSH agent/password fallback is disabled.

```bash
bareplane bootstrap render bareplane.yaml
bareplane bootstrap doctor bareplane.yaml
bareplane bootstrap trust bareplane.yaml
bareplane bootstrap apply --approve homelab bareplane.yaml
```

Trust discovery still requires comparing fingerprints with an out-of-band source and explicitly approving the cluster name. The apply approval must match `metadata.name` exactly and is checked before operation locking or any mutating phase.

## Before each phase

The orchestrator repeats local doctor/toolchain checks, validates project SSH trust, and authenticates to every desired machine for read-only host suitability checks. It also compares every generated file to the current embedded renderer: edited/stale files, extra playbooks/plugin directories, missing files, symlinks, or writable-by-other-user assets are refused. Configuration and trust are checked again after network preflight, before starting the phase.

Only these phases can run, in this order:

1. `host_prepare`
2. `kubernetes_install`
3. `api_vip`
4. `control_plane_init`
5. `cilium`
6. `join`
7. `kubeconfig`
8. `health`

Each role also enforces its own live prerequisites and ownership checks. Primary initialization records the public CA fingerprint; subsequent networking/join prerequisites reject stale initialization markers or an admin configuration pointing at another CA or VIP.

The initial apply preflight allows ordinary swap for the reviewed preparation role to disable; custom swap mechanisms still fail in that role. After preparation, enabled swap is a failure. A validated recorded resume allows existing Kubernetes paths to reach the resumed role's ownership checks. Standalone `bootstrap preflight` remains conservative and does not grant this resume policy.

## Private state, locking, and logs

State lives under `.bareplane/state/bootstrap`, separate from both generated output and Terraform state:

- `progress.json`: versioned, canonical, owner-only progress, binding the completed phase prefix to configuration and SSH-trust fingerprints.
- `.operation.lock/owner.json`: private operation ownership metadata. Apply, render, and trust changes share this lock, so a supported rerender or trust rotation cannot race active bootstrap.
- `logs/<phase>-<id>.log`: unique owner-only diagnostics, capped at 4 MiB per phase. Previous logs are preserved; treat them as sensitive and do not publish them.
- `ansible-local/`: private Ansible controller working files.

The runner controls its inventory, key path, Ansible configuration, module/role/plugin paths, callbacks, and environment. Provider credentials, global kubeconfig, Python injection paths, and user Ansible overrides are not forwarded. Every phase has a 45-minute timeout; cancellation stops the controller process group and does not mark a failed phase complete.

Terraform has its own operation lock. Do not concurrently replace or destroy the VMs with Terraform while bootstrapping; bootstrap does not acquire ownership of infrastructure lifecycle operations.

## Reruns and recovery boundaries

A phase is marked active before execution and complete only after successful exit, uncancelled context, and a successful private progress write. Failure stops immediately and reports the phase, its private log, and the exact retry command. Successful earlier phases are not replayed.

An interrupted or failed run resumes at the recorded active/next phase. A role may safely finalize completed work or explicitly refuse an incomplete attempt; the orchestrator never bypasses that decision. In particular, incomplete kubeadm state requires explicit recovery, not a blind retry or deleted marker. If a progress write failed after a successful phase, the same phase runs again through its idempotency/ownership checks.

A fully completed cluster rechecks prerequisites and runs only the full health gate. It does not reinstall packages, reinitialize Kubernetes, rejoin nodes, or rotate kubeconfig credentials. A previously manual/untracked cluster without valid orchestration progress is refused; matching machine names alone do not establish ownership.

Changing the cluster/network/version/topology/key reference or approved SSH identities invalidates the progress binding. Use explicit lifecycle/recovery planning rather than editing `progress.json`. Updating Bareplane itself is different: rerendering the current embedded implementation is allowed when the semantic bootstrap configuration and trust remain unchanged.

Inspect a stale lock's recorded PID on the original controller and confirm that no operation is active before removing that specific lock. Do not clear progress, initialization intent, or credential ownership records to force a rerun. Use the [explicit diagnosis and recovery workflows](bootstrap-recovery.md).

`bootstrap apply --check` is deliberately refused: kubeadm and workload-based health verification cannot be truthfully simulated by a blanket dry run. Use offline rendering, local doctor, read-only preflight, and `validate.yaml` for the checks they actually provide.

CI exercises fake-runner command/refusal paths, the real controlled Ansible environment, the individual VM phases, and full `bootstrap apply` plus a health-only rerun against three control planes and a worker. The CLI VM test also uses quoted/spaced paths containing OpenSSH percent tokens.
