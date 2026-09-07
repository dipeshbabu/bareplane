# User-owned GitOps contract

GitOps is optional during infrastructure/Kubernetes bootstrap. Once configured, the repository contract is explicit and complete:

```yaml
spec:
  gitops:
    repoURL: https://github.com/example/homelab-gitops.git
    revision: main
    rootPath: clusters/homelab
    identity:
      owner: example
      name: homelab-gitops
```

`repoURL`, `revision`, and `rootPath` are required when `spec.gitops` is present. `identity` is optional descriptive owner/name metadata; it is not authentication or proof of repository ownership. `ValidateGitOps()` requires this block for GitOps operations, while configurations without it remain valid for bootstrap.

The initial implementation supports anonymously readable HTTPS Git repositories. “Public” refers to credential-free access; configuration validation is offline and does not claim to verify repository existence, reachability, TLS, or its contents. HTTPS URLs with embedded credentials, query strings, fragments, local/file/SSH schemes, traversal, or noncanonical paths are rejected. Loopback/link-local endpoints are unsupported. Standard CA validation remains required.

Revisions are explicit portable branch/tag names or commit IDs, without ref-expression syntax, whitespace, traversal, `.lock` components, or option-like prefixes. A full immutable commit is preferable for reproducible production handoff; a named branch intentionally follows future user commits.

Root paths are lowercase portable repository-relative directories, such as `clusters/homelab`. Absolute paths, dot/traversal components, separators from other platforms, Windows device names, and reserved `.bareplane`, `.git`, `state`, `terraform`, or `node_modules` components are refused. The renderer must also refuse local destinations that overlap Bareplane's generated/state directories; a safe remote path does not authorize an unsafe local write.

## One ownership boundary

Terraform owns infrastructure. Bareplane/Ansible owns Linux/containerd, the Kubernetes toolchain, kubeadm's control plane/etcd/CoreDNS, kube-vip, private access state, and Cilium. Cilium stays bootstrap-owned after handoff; it must not appear as an Argo-managed component.

Argo CD owns the remaining long-lived in-cluster **platform** resources: its own steady-state deployment, platform namespaces/controllers, ingress/certificates, observability, storage services, and supported AI/data profile resources. A component must have one owner, not parallel Ansible and Argo reconciliation.

The only direct Kubernetes writes permitted for GitOps handoff are:

- the reviewed minimal pinned Argo CD bootstrap resources needed to start its controllers;
- the root Argo CD Application pointing at this user-controlled repository/revision/root path.

Bareplane must not directly apply the platform/profile payload during handoff. The user reviews and publishes the generated repository; Argo reconciles the published source. GitOps cannot begin until the private-kubeconfig bootstrap health gate succeeds.

## Repository layout

The deterministic renderer follows this layout, with `rootPath` selecting the cluster's Application set:

```text
readme.md
bootstrap/
  <cluster>-root-application.yaml
clusters/<cluster>/                 # example rootPath
  kustomization.yaml
  applications/
    argocd.yaml
    <component>.yaml
components/
  argocd/
  <component>/
profiles/
  <profile>/
```

The root Application manifest stays outside its own selected source directory. Child Application manifests use the same repository/revision contract and point to reviewed component/profile paths. Names, ordering, paths, and manifests must be deterministic, and no local `.bareplane` state, kubeconfig, SSH key, Terraform state, or generated join material belongs in the repository.

## App-of-Apps and ordering

The root Application reconciles the child Application set. Use explicit sync-wave bands: `-30` for namespace/CRD foundations, `-20` for operators, `-10` for shared services, `0` for supported profiles/workloads, and later waves only for dependent payloads. Dependencies must determine ordering; file names are not a readiness mechanism.

Application creation ordering alone does not prove child-resource readiness. Argo bootstrap must provide the required child-Application health assessment, and handoff must verify sync and health before declaring success. Initial handoff must not enable broad cascading prune/deletion merely to make a first sync pass. The component graph and Argo runtime implement these checks separately.

## Credential and private-repository extension boundary

The base schema has no passwords, tokens, private keys, credential references, insecure-TLS flags, or clone-command overrides. Strict decoding rejects those fields rather than treating them as inert metadata. Do not put credentials in repository URLs or `bareplane.yaml`.

A future private-repository feature may add a separately reviewed secret-reference contract, scoped Git/Argo credentials, host/TLS trust, rotation/revocation, and audit-safe delivery. It must not add plaintext credentials to the base config or generated Git history. This issue defines that boundary only; private repository access is not implemented.

The contract performs no Kubernetes mutation. The [offline renderer](gitops-render.md) now creates a managed local export; Argo installation, handoff, and dependency-driven profile selection are separate implementation issues.
