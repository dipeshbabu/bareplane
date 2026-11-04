# Core platform convergence

The [core fixture](../examples/core-fixture.yaml) composes the implemented
cert-manager, Metrics Server, observability and retained local-storage components
through the same registry and renderer used by every project. Argo CD owns their
steady-state reconciliation. Kubernetes, Cilium and kubelet serving certificates
remain bootstrap-owned.

## Defaults and explicit intent

`minimal` keeps the safe Argo-only default. The composed core bundle is explicit:

```yaml
spec:
  profiles: [minimal]
  components:
    enabled: [metrics-server, observability, storage]
    disabled: [external-dns, secrets-sops, vault]
  certificates:
    issuers:
      - name: lab-selfsigned
        type: self-signed
  observability:
    storage:
      mode: ephemeral
  storage:
    strategy: local-static
    volumes:
      - name: first
        node: lab-control-1
        capacityGiB: 1
```

Adapt the node identity and capacity to your actual approved topology. The example
is a disposable compatibility fixture, not production sizing or an HA claim.
Metrics Server resolves cert-manager and bootstrap-owned serving TLS as required
dependencies. Disabling a required dependency fails instead of silently producing
an incomplete graph. Omitting the storage inventory or ephemeral-history
acknowledgement also fails rendering.

No external DNS controller, decryption key, external Vault connection, public
endpoint or automatic disk provisioning is a default. Those integrations retain
their explicit contracts. SOPS and Vault are alternative secret-delivery backends;
selecting both is refused. Rendering never changes an unavailable AI/data component
to implemented merely because a profile requests it.

## Fresh-project workflow

Configure the entire intended component set before rendering/bootstrap so the
project's recorded contracts match. Follow the normal infrastructure and bootstrap
guides, then explicitly enable verified kubelet serving TLS:

```sh
bareplane bootstrap kubelet-tls --approve lab
bareplane gitops render
```

Replace `lab` with your exact configured cluster name. Review the export, prepare
only the explicitly inventoried empty local directories on their declared nodes,
and publish the export to your repository. The storage inspector only reads the
host; it never creates paths, formats disks or adopts existing data. Complete
`gitops install` and `gitops handoff` with exact cluster-name approval.

After handoff, Git owns component changes. Do not replay bootstrap installation to
reclaim that state. Read [component selection](component-selection.md),
[kubelet TLS](kubelet-tls.md), [storage](storage.md) and
[GitOps handoff](gitops-handoff.md) for the individual boundaries.

## What the combined gate proves

The required **Core profile VM** job runs a fresh disposable Linux Kubernetes
cluster and verifies:

- The root and every child reconcile the same exact public source commit,
  including fork sources, with dependency-ordered sync waves.
- cert-manager becomes ready, node metrics require verified serving TLS, and
  Prometheus and kube-state-metrics collect healthy metrics simultaneously.
- A real PVC writes and reads retained local data across workload replacement.
- A deliberate observability scale-down becomes `OutOfSync`; an explicit sync to
  the approved Git revision recovers it while preserving its workload UID.
- A hard-refresh rerun preserves root/child, workload, node, networking and PV
  identities, including the Kubernetes-owned PVC claim binding.

The explicit M1/M2 fixture and its Kubernetes payload remain unchanged. Existing
component-specific VM jobs remain required, including DNS ownership/dry-run,
SOPS delivery, external Vault, certificate rotation and local-volume refusal/data
preservation. Offline composition tests also combine the core graph with each
explicit external integration and reject missing contracts or unavailable breadth.

## Versions and change policy

[compatibility.json](compatibility.json) records the actual renderer pins, and a
test binds those values to the implementation. The combined gate verifies that
specific set together; it does not claim every historical version permutation.
Component changes require their focused regression checks and the combined core
gate. Rerunning installation is not an upgrade procedure: implicit Kubernetes,
controller and ownership replacement remain refused. Lifecycle upgrades and
application-aware backup/restore remain tracked separately.

Hosted convergence proves the disposable Kubernetes path. Physical Proxmox release
certification still requires the operator evidence in the
[release checklist](release-checklist.md).
