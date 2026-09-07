# Verify root GitOps handoff

```bash
bareplane bootstrap render ./bareplane.yaml
bareplane gitops render ./bareplane.yaml
# Review and publish this exact payload to your configured repository.
bareplane gitops install --approve lab ./bareplane.yaml
bareplane gitops handoff --approve lab ./bareplane.yaml
bareplane status ./bareplane.yaml
```

Handoff requires the original Linux/WSL controller, the supported toolchain, exact cluster approval, matching completed bootstrap/Argo records, unchanged project-scoped SSH trust, and the current managed local export. Re-render bootstrap after updating the Bareplane binary so its embedded implementation matches the controller workspace. Check mode, implicit adoption, and automatic replacement of unrelated resources are unsupported.

The shared bootstrap operation lock covers a fresh full Kubernetes health gate and the entire handoff. Only the approved root Argo `Application` may be created or updated by the handoff client. Argo applies its child Applications and platform resources; Bareplane does not apply the fetched platform payload directly. No Git commit or push occurs.

## One verified initial snapshot

Before root creation, Bareplane shallow-fetches the anonymous HTTPS repository, resolves the configured revision to a commit, and checks every approved payload file byte-for-byte. The selected root/component source directories must contain exactly the expected files. Missing, modified, executable, redirected, or unexpected source files block handoff. Other unrelated repository directories are not managed by Bareplane. No checkout, hooks, Git credentials, repository plugins, or arbitrary remote Kustomize resources are executed during this inspection.

The actual owned Ready repository-server pod must also read the anonymous HTTPS repository before root creation. Its pod/ReplicaSet/Deployment ownership chain and pinned image are verified, and the read-only Git probe runs with an empty environment plus explicit safe Git settings. Redis credentials and other container environment values are not passed to the probe. Subsequent real Argo reconciliation proves the controller/repository service path, not just controller-side network reachability.

Initial root reconciliation is pinned to the verified commit. Root-level Kustomize patches pin **every reviewed first-level child** to that same commit. This avoids a moving branch changing either the root's manifest set or a child's source during the initial acceptance gate. Bareplane waits for current-source `Synced` and `Healthy` status on the root and all expected children, with correct Argo parent tracking. It refuses unrelated Applications, unsupported initial ApplicationSets, and any Argo claim on bootstrap-owned Cilium.

The configured remote revision is checked again after this gate. If it advanced, the root remains pinned and handoff stops. A retry can verify a newer byte-matching snapshot and update the owned pin, then repeat the gate. A changed payload must be reconciled with the reviewed local contract before initial ownership transfer; it is never silently accepted.

After the initial snapshot is verified, only the owned root is updated to follow the actual configured revision and the temporary child pinning patches are removed. Named branches then follow future user Git commits as intended; immutable revisions remain fixed. Initial pruning and cascading deletion stay disabled. The final gate verifies that root/child reconciliation is active before reporting completion.

## Durable intent and safe retries

Private `handoff.json` binds the cluster CA, installed Argo identity, complete payload contract, root name, nonce, UID, expected object hashes, verified commit, and transition state. Server-side dry-run provides API-defaulted expected hashes before creation/update intent is persisted. Creation cannot overwrite an existing root. Updates carry both UID and resourceVersion preconditions, and retries re-read ownership before resubmitting after a conflict or ambiguous response.

An existing unrelated root with the same name, a replaced UID, changed spec, unexpected owner/finalizer/tracking annotation, or terminating root blocks automation. Cancellation stops command retries and leaves durable intent. There is no automatic deletion or rollback of a root that may already reconcile resources. Inspect the private log and receipt before retrying.

Once following Git starts, ownership does not revert to bootstrap pinning. Completed handoff reruns are read-only Kubernetes verification; they do not rewrite the root or reinstall Argo. Later user Git changes are authoritative, so completed handoff does not require Git to remain byte-identical to the initial bootstrap export. Local repository identity/configuration must still match its recorded contract. The installer refuses to reclaim handed-off Argo state.

Each reconciliation gate has a ten-minute bound within a twenty-five-minute controller operation, separate from the preceding bootstrap health gate. Errors expose only sanitized sync/health/operation states and known resource names, not Argo error bodies or credentials. Failure before completion does not record a new success. Failure of a later read-only check does not erase the already-transferred ownership state.

## Local lifecycle status

`bareplane status` distinguishes `kubernetes-ready`, `argocd-ready`, and `gitops-handed-off`, and reports active bootstrap/GitOps operation locks. These are applicable **local success records**, not a live health scan. Missing, changed, or unsafe current contracts/trust prevent stale records from being reported as current readiness. Use the guarded operations or Argo itself for fresh live checks.

CI uses the [public acceptance fixture](../examples/gitops/readme.md) on disposable Kubernetes VMs. It verifies modified-export and unrelated-root refusals, actual pinned-then-following reconciliation, Argo self-management without recreating the original resources, a read-only handoff rerun, installer non-reclamation, and the three lifecycle status fields. Local tests cover atomic root writes, timeouts/cancellation, source drift, partial resume, ownership, scoped writes, and actual Kustomize child pinning.
