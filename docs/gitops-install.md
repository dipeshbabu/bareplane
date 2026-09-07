# Install the minimal Argo control plane

```bash
bareplane bootstrap apply --approve lab ./bareplane.yaml
bareplane gitops render ./bareplane.yaml
# Review, copy, commit, and publish the public GitOps payload yourself.
bareplane gitops install --approve lab ./bareplane.yaml
```

`gitops install` requires exact cluster-name approval on the original Linux/WSL controller. It does not finish an incomplete bootstrap, install platform workloads, or apply the root Application. Check mode, automatic upgrades, unmanaged adoption, and implicit repair are unsupported.

The command verifies the current rendered bootstrap bundle, project-scoped SSH trust, completed eight-phase bootstrap record, current unedited GitOps export, local toolchain, and absence of pending reset. It shares the bootstrap operation lock and runs the full Kubernetes health gate again before any Argo resource creation. A completed historical health record alone is not enough.

Use the supported bootstrap toolchain (ansible-core 2.19.9, OpenSSL 3, and kubectl matching the configured Kubernetes version) plus distribution-maintained Git. Install OS security updates. Kubectl preferences, global kubeconfig, Git system/global configuration, credentials, SSH agents, hooks, submodules, interactive prompts, environment overrides, and Git redirects do not control installation. Repository access is anonymous HTTPS with standard TLS validation; use the canonical clone URL, including `.git` where the server requires it.

## Repository and payload checks

Publish the [reviewed export](gitops-render.md) at the configured repository/revision/root before installation. The installer makes a bounded shallow Git fetch without checkout, resolves an immutable commit, and verifies that the configured root is a real directory with a nonempty, regular `kustomization.yaml`. Symlinks, submodules, unsafe ref/path syntax, YAML aliases/tags/duplicate keys, oversized roots, and missing paths are refused. Git operations have deadlines and pack/output limits; repositories over 64 MiB are outside this initial inspection contract. No credentials are written to Git configuration or repository contents.

These checks prove controller-side repository reachability. They do **not** claim that Argo has reconciled the repository or that a root payload matches a published revision. Full remote-payload comparison, Argo-side repository verification, and root handoff belong to the separate handoff phase. Installation never applies the fetched repository contents.

The actual installation payload is the same pinned Argo CD 3.5.2 desired state emitted by the renderer. Bareplane snapshots its reviewed bytes into a private execution workspace, verifies a SHA-256 contract binding repository settings and every input file, then performs an offline Kustomize build. Cilium, kube-vip, CoreDNS, node configuration, unrelated platform resources, ingress, SSO, notification/ApplicationSet controllers, and optional Argo projects are not installed. The required Redis cache is ephemeral; no persistent storage is provisioned here.

## Ownership, interruption, and reruns

Before the first write, every target identity, including the `argocd` namespace and cluster-scoped Argo resources, must be absent. Labels alone never grant adoption. An existing unmanaged Argo installation blocks the operation without being patched or deleted.

Private `argocd-ownership.json` binds the cluster CA, public input contract, installation nonce, and resource inventory. Before each create, a server-side dry-run records a hash of the API-defaulted desired object and pending identity. Creation uses `kubectl create`, not force-apply. Each completed write records the returned UID. An ambiguous create timeout can resume only when the nonce and expected content match; recorded UIDs cannot silently change. Missing, modified, terminating, redirected, or unexpectedly owned resources require explicit recovery instead of automatic replacement.

Runtime Argo credential values are never recorded in the receipt or Git. The empty `argocd-secret` declaration may acquire only the reviewed runtime credential keys; its contents remain Argo-owned. API status, generated identity metadata, known controller bookkeeping, and Service address allocation are excluded from desired-content hashes; declared configuration remains checked.

Reruns verify existing resources without recreating them and wait for all Argo CRDs and controller workloads to become ready. They also refuse an already-applied Application handoff and verify that Cilium has no Argo tracking marker. Readiness has a ten-minute bound within a fifteen-minute controller operation; the preceding bootstrap health gate has its own bound. Child processes, output, private Git workspaces, and logs are bounded, and interrupted process groups are terminated.

The separate private `gitops.json` record moves from `installing` to `argocd-ready` only after successful execution and revalidation of inputs, trust, bootstrap progress, and generated payload. Failure does not advance readiness. An incorrect read-only repository prerequisite can be corrected and re-rendered only before any Kubernetes creation receipt exists. Once creation starts, changing the contract is an explicit lifecycle operation, not an installer retry. Private diagnostic logs are retained under `.bareplane/state/bootstrap/logs`; inspect them without publishing credentials.

## GitOps ownership transition

Before root handoff, Bareplane owns creation of this minimal control plane. After handoff, Argo self-manages the same desired state from the user repository; Bareplane must not compete with its reconciliation. The Argo child Application ignores and preserves only the `bareplane.io/installation` provenance annotation. This does not ignore workload configuration, credentials, child Application specs, or broad resource differences.

Successful installation reports **Argo ready**, not **GitOps handed off**. The next phase must verify the published payload and Argo reconciliation before declaring the ownership transition complete. Bootstrap reset intentionally refuses an Argo/platform-bearing cluster; application-aware removal, backup, and recovery remain lifecycle work.

## Acceptance coverage

CI creates a disposable, single-node Ubuntu Kubernetes VM, runs the full bootstrap, rejects a missing Git root, refuses a deliberately unmanaged Argo namespace, installs the pinned control plane, and reruns installation while comparing all resource UIDs and desired hashes. It verifies that no root Application/ApplicationSet was created. The immutable public Git fixture is used only for reachability, never applied as a handoff payload. Unit tests cover incomplete bootstrap, approval, cancellation, input/trust drift, process/environment isolation, partial create recovery, foreign ownership, missing resources, malformed state, secret exclusion, and readiness failures.
