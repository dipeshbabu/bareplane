# Bootstrap diagnosis and recovery

Ordinary `bootstrap apply` never resets a machine, removes an etcd member, or deletes ownership records to force a retry. Start with the read-only view:

```bash
bareplane bootstrap diagnose bareplane.yaml
```

Diagnosis reports private local phase progress, an operation lock's operation/PID, pending reset state, and bounded remote initialization/join/Cilium markers. It verifies SSH trust and authentication but does not need a healthy Kubernetes API or execute Ansible against the hosts. Marker contents, kubeconfigs, and private keys are not printed. A CA/cluster mismatch is a refusal, not permission to adopt or reset the machine.

## Supported destructive boundary

The initial reset workflow is deliberately full-cluster and bootstrap-only:

```bash
bareplane bootstrap reset \
  --approve homelab \
  --scope cluster \
  --confirm-destructive \
  bareplane.yaml
```

Both the exact cluster-name approval and destructive confirmation are required. This removes the owned Kubernetes control-plane/etcd/CNI state, withdraws the matching project-local admin credential, and reboots the configured machines. Kubernetes/etcd data is not recoverable without backups. This is not an etcd backup, production drain, upgrade, or application recovery command.

Reset refuses untracked clusters, configuration/trust drift, foreign CA or kubeadm intent, symlinked/special state, dedicated/nested Kubernetes data mounts, unrecognized runtime namespaces/images, active kubeadm processes, unmanaged static pods/CNI, application workloads, non-bootstrap namespaces/extensions, and persistent volumes/claims/storage classes. Every desired host passes ownership inspection before any node is reset. API access is required to exclude application/storage state after cluster formation; a recorded early bootstrap failure may use restricted local ownership/runtime evidence when the API never became available.

Node-specific control-plane/etcd quorum operations are not supported by this initial command. A failed secondary join can be abandoned using the full bootstrap-only reset; replacing one member of an established or application-bearing cluster requires explicit lifecycle/backup planning rather than a force flag.

## What reset does and preserves

The workflow uses the pinned kubeadm with an explicit CRI socket and certificate directory. For full-cluster abandonment it skips kubeadm's member-removal phase and removes only the validated local `/var/lib/etcd/member` data, avoiding manifest-controlled external data paths. It verifies kubeadm/CRI cleanup instead of treating warning-only output as success.

Only the reviewed bootstrap records, managed kube-vip manifest, and validated Cilium CNI file are cleaned outside kubeadm's normal paths. Containerd/Kubernetes packages, package pins, host preparation configuration, and image caches remain. A reboot clears volatile Cilium interfaces/BPF/runtime state and the temporary API VIP; no blanket iptables flush is performed.

Private node diagnostics and configuration intent are preserved under `/var/lib/bareplane/bootstrap/recovery/<reset-id>`. Local reset metadata is preserved under `.bareplane/state/bootstrap/recovery/reset-<reset-id>`. These are sensitive diagnostic archives, not application or etcd backups.

The command does not remove or format application/data disks, Terraform state, Proxmox VMs, generated GitOps repositories, or the operator's global `~/.kube/config`. Check your global kubeconfig separately: copies of old credentials outside Bareplane's canonical file are not managed by this workflow.

After successful reset, the prepared/toolchain phase prefix remains recorded. Rebuild with:

```bash
bareplane bootstrap apply --approve homelab bareplane.yaml
```

The next initialization creates a new cluster CA. Old credentials are not silently reused for it.

## Interrupted reset

An owner-only `reset.json` records the reset identifier, source configuration/trust, and validation stage. A failed read-only validation does not advance to destruction. Once validation succeeds, an interrupted reset remains pending and blocks ordinary apply. Re-run the same explicitly approved reset to finish it; node receipts prevent adopting a new cluster or confusing two reset attempts. Do not delete reset receipts to bypass checks.

## Common recovery cases

- **Expired join token before enrollment:** a fresh unjoined node receives a new short-lived token on the next join attempt. Tokens are never reused from local generated output.
- **Incomplete primary or secondary kubeadm attempt:** inspect its private node output and diagnosis. Do not blindly repeat kubeadm or remove intent markers. Use the supported full bootstrap-only reset when its ownership/storage guards pass; otherwise follow explicit infrastructure/lifecycle recovery.
- **Cilium installation failure:** inspect private Helm/bootstrap state and run diagnosis. Completed unchanged Cilium releases can be verified idempotently; ambiguous partial state requires explicit recovery, not forced adoption.
- **Lost local kubeconfig after node formation:** recover only that artifact and re-run health:

  ```bash
  bareplane bootstrap recover-kubeconfig --approve homelab bareplane.yaml
  ```

  This does not reinitialize or rejoin nodes and does not overwrite a foreign or expired managed credential. Certificate renewal is a separate lifecycle operation.
- **Stale operation lock:** use diagnosis to find its operation and PID. On the original controller, confirm that the process and any child bootstrap work have stopped. Only then remove the specific `.bareplane/state/bootstrap/.operation.lock/owner.json` and empty lock directory. PID absence on a different controller is not proof of staleness. No automatic force-unlock or recursive state deletion is provided.
- **Changed SSH identities or configuration:** verify the change out of band and plan recovery. Reset and apply refuse to silently rebind existing state to a different cluster/topology/key set.

The supported [kubeadm reset behavior and omissions](https://kubernetes.io/docs/reference/setup-tools/kubeadm/kubeadm-reset/) remain relevant: CNI/network/global-kubeconfig cleanup is not automatically provided by kubeadm itself. Bareplane's additional actions are limited to the owned bootstrap state described above.
