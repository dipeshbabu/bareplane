# External Vault integration

The advanced `vault` provider connects to an **externally managed** Vault using
standard TLS verification and Kubernetes authentication. It does not install,
initialize, unseal, back up, or administer a Vault cluster. Vault availability,
storage, server certificates, auth configuration and recovery remain operator
prerequisites. No Vault token, Kubernetes JWT or runtime secret value belongs in
`bareplane.yaml`, the generated export, or Git.

```yaml
spec:
  secrets:
    provider: vault
    vault:
      address: https://vault.example.com:8200
      workloadNamespace: workloads
      authMount: kubernetes
      role: workloads-reader
      audience: vault
      kvMount: kv
      caSecret:
        name: vault-ca
        key: ca.crt
      refreshInterval: 1m
      secrets:
        - name: database
          path: apps/database
          keys:
            - secretKey: username
              property: username
            - secretKey: password
              property: password
```

`address` is an HTTPS origin, without userinfo, query, fragment or proxy path.
There is no insecure TLS override or custom HTTP-header input. Without
`caSecret`, the image's system roots are used. With it, the referenced PEM CA
bundle must exist in the workload namespace; it cannot be a generated target
Secret. Optional `vaultNamespace` selects an existing Vault Enterprise namespace.
Auth mount, role and KV mount are bounded single-segment references. The role's
audience must match the explicit `audience` value.

Select 1–32 unique target Secret names and 1–64 explicit properties per target.
Paths are relative KV-v2 paths under `kvMount`; do not prepend the API's `data/`
segment. Traversal, wildcards and arbitrary expressions are rejected. Properties support portable
dot-separated JSON paths, not queries or transformations. Output keys are
Kubernetes Secret data keys. The integration creates `Opaque` Secrets only.
Refresh defaults to one minute and accepts whole seconds from 30 seconds to
24 hours. Secrets and keys render in canonical order.

`provider: vault` remains diagnostic when the block is absent, but rendering
refuses an unconfigured Vault integration. Vault and configured SOPS are
mutually exclusive. Switching backends does not adopt existing Secrets or
automatically revoke the old backend; migrate deliberately with distinct
targets and a reviewed cleanup plan.

## Authentication prerequisites

Before deployment, the Vault operator must configure the named Kubernetes auth
mount with the cluster's reachable API endpoint, trusted Kubernetes CA and a
secure, rotated token-reviewer identity. Configure the role to accept only:

- ServiceAccount `bareplane-vault-reader` in the exact `workloadNamespace`;
- the configured audience, with short-lived tokens and a bounded maximum TTL;
- read access to the explicitly selected KV-v2 paths, plus token self-lookup and
  self-revocation needed by the client lifecycle.

For the example, a narrowly scoped Vault policy contains:

```hcl
path "kv/data/apps/database" {
  capabilities = ["read"]
}
path "auth/token/lookup-self" {
  capabilities = ["read"]
}
path "auth/token/revoke-self" {
  capabilities = ["update"]
}
```

Bind it to the Vault role with `bound_service_account_names=bareplane-vault-reader`,
the exact `bound_service_account_namespaces`, `audience=vault`, and short token
TTL/max-TTL (for example 120s/300s). Do not grant wildcard secret-path access or
attach unrelated default policies. Bareplane does not execute these Vault
administration operations or distribute reviewer tokens. See the official
[Vault Kubernetes auth documentation](https://developer.hashicorp.com/vault/docs/auth/kubernetes).

Argo creates the dedicated ServiceAccount. It has no Kubernetes RBAC grants of
its own and no automatic token mount. The operator can request its 600-second,
audience-bound token through Kubernetes' TokenRequest subresource; token creation
is restricted by `resourceNames` to this one account. No long-lived token Secret
is created. The pinned client revokes Vault tokens after use; token persistence
and token caching are disabled.

## Ownership and namespace boundaries

The integration uses reviewed External Secrets Operator **2.10.0**, source
[commit `84886008`](https://github.com/external-secrets/external-secrets/tree/8488600898e856d74a7e0f53ed5e3cc79d89f4e8).
The checksum-verified Helm chart is used only during vendoring; generated Git is
self-contained. Argo owns the operator Deployment, its new `vault-secrets`
namespace, scoped RBAC, three native-version CRDs, admission policy, SecretStore
and ExternalSecret declarations. No production Vault server, webhook, cert
controller, cluster store, push controller or provider-generator workload is
installed. The operator requires a GeneratorState informer, so its namespaced
schema and read permissions remain, with state generation disabled.

The single replica runs non-root with a read-only root filesystem, 100m CPU /
128 MiB memory requests and a 512 MiB limit. A namespace-local lease and Recreate
rollout prevent overlapping controllers. Informer scope is one explicit workload
namespace. Secret caches are disabled; metadata watches and runtime Secret access
remain namespace-scoped. The operator has no cluster-wide Secret permissions,
Kubernetes token-review privilege, Secret deletion permission, or workload-restart
permission. Read access to ServiceAccount metadata supports the upstream client's
fallback behavior; only the dedicated account permits token creation.

The workload namespace must already exist under its own owner. `argocd`,
`kube-*`, and `vault-secrets` are forbidden as workload namespaces. Initial
handoff refuses existing target Secrets, conflicting ServiceAccounts/bindings,
pre-existing operator namespaces or CRDs, and terminating/missing workload
namespaces. An existing ESO installation is not implicitly adopted or shared.
The namespace itself is never imported into the Vault Application's ownership.

The existing Argo Application owns its own ConfigMap health customization. No
second Application patches Argo configuration. Generated connection names and
controller classes include a hash of the public connection contract. Changing
the connection rolls the class; retained old Stores no longer match the running
controller and cannot continue sending JWTs to an old Vault address. Review and
remove obsolete RBAC/Stores separately after migration; pruning is not implicit.

## Deployment and runtime behavior

Prepare the workload namespace, the external Vault policy/auth role, and optional
CA reference. For example, using an already-authorized private kubeconfig:

```bash
kubectl --kubeconfig /private/admin.conf -n workloads create secret generic vault-ca \
  --from-file=ca.crt=/private/verified-vault-ca.pem
```

Do not fetch and trust an unauthenticated server certificate as its own CA. Do
not precreate `vault-secrets` or overwrite an unrelated CA Secret. Render,
review and publish the export through the normal guarded GitOps workflow. After
handoff, publish changes through the existing Argo owner; do not rerun bootstrap
installation to take ownership back.

Targets use `creationPolicy: Orphan` and `deletionPolicy: Retain`. The admission
policy prevents adopting existing unmanaged Secrets, transferring source labels,
adding garbage-collection owners, and operator writes outside the configured
target set. It also protects against the operator's preliminary metadata patch
on an unmanaged Secret. Existing target values remain unchanged when Vault is
unavailable, authorization is revoked, trust fails, or a source/property is
missing. Review deliberate deletions separately; the admission policy does not
block deletion or namespace garbage collection.

Value rotation is read on the next refresh and preserves the Kubernetes Secret
UID. Applications using Secret-backed environment variables still need their own
rollout; this integration does not restart unrelated workloads. Revoke access in
Vault when credentials or workload identity are compromised, rotate affected
service credentials, and repair auth/trust before resuming. Retained Kubernetes
values may be stale or revoked; retention is not a promise that credentials remain
usable. Deleting a declaration does not erase its retained Secret. Argo can
recreate the declaration while retaining the same managed target.

SecretStore and ExternalSecret Ready conditions feed fixed, non-secret Argo
health messages; ExternalSecret health also checks the synchronized generation.
These are controller observations, not instantaneous availability guarantees.
Inspect resource condition types/reasons and Argo sync/health without dumping
Secret objects, token-review bodies, or verbose request logs. Protect access to
the workload namespace, Vault, Argo and Git; this is not a sandbox against those
administrators. See ESO's
[security guidance](https://external-secrets.io/latest/guides/security-best-practices/)
and [Vault provider contract](https://external-secrets.io/latest/provider/hashicorp-vault/).

CI uses an isolated TLS Vault double, real audience-bound Kubernetes JWT review,
throwaway CA/key material and runtime values. It checks trust refusal, scoped
auth, token cleanup, unmanaged-state refusal, value rotation, source/property
loss, revocation/unavailability, retained identities and Argo reconciliation.
It does not contact or certify an operator's production Vault deployment.
