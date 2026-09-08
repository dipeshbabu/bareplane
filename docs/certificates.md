# Certificates

cert-manager is an opt-in, Argo-owned component. It is not installed by bootstrap
and it does not enable ingress, public DNS, external certificate authorities, or
trust distribution. Enable the controllers without an issuer using
`spec.components.enabled: [cert-manager]`, or configure explicit issuers:

```yaml
spec:
  certificates:
    issuers:
      - name: lab-selfsigned
        type: self-signed
      - name: internal-ca
        type: ca
        secretName: operator-managed-ca
```

The block selects cert-manager through the component registry and conflicts with
explicitly disabling it. An empty block installs only the controllers. Issuers
render as `ClusterIssuer` resources, sorted by name, after the CRDs and workloads.
Names must be unique lowercase DNS labels; at most 32 issuers are accepted.

`self-signed` is an explicit lab/bootstrap path, not public trust. For a private
CA, `secretName` references a dedicated Secret in the `cert-manager` namespace
containing `tls.crt` and `tls.key`. The operator must arrange secure delivery;
Bareplane never reads, generates, or exports that Secret. Reusing the webhook CA
is refused. A missing or invalid CA Secret leaves its issuer unready; there is
no fallback to self-signing. CA rotation, expiry monitoring, trust distribution,
backup, and recovery remain operator responsibilities. See the upstream
[SelfSigned](https://cert-manager.io/docs/configuration/selfsigned/) and
[CA issuer](https://cert-manager.io/docs/configuration/ca/) security guidance.

The reserved `acme` reference contract includes `server`, `email`,
`accountKeySecretName`, and `dnsCredentialSecretName` or `ingressClass`. ACME is
currently rejected: DNS-01 requires a supported solver, explicitly authorized
zone, and securely delivered credentials; HTTP-01 requires a configured ingress
class and externally reachable challenge route. Bareplane does not infer these
from a domain or mutate public DNS. Unknown fields, plaintext credential fields,
unsupported issuer types, and mixed issuer modes fail validation.

## Payload and ownership

The vendored release is cert-manager **1.21.1**, source commit
`24e33194fb39488eff2bbf10c6dc640f407cad44`, upstream release YAML SHA-256
`5f6a499b8c1857d57f560f536e0dcc830914b45c420899fe7ad0692c8624e408`.
`hack/vendor_cert_manager.py` verifies this checksum before curating the payload;
the Apache-2.0 license accompanies generated assets. The 1.21 release series
[supports Kubernetes 1.33–1.36](https://cert-manager.io/docs/releases/), including
Bareplane's reference Kubernetes 1.36.4. Renderer and reconciliation use local
vendored manifests, not mutable remote bases.

The dedicated namespace contains the controller, CA injector, webhook, and
leader-election permissions. Each deployment requests 100m CPU and 64Mi memory,
with a 512Mi memory limit and a control-plane scheduling toleration. Upstream
security contexts and pinned images are preserved. This is a non-HA baseline.
Six CRDs and their required cluster RBAC/webhook configuration are included.
Only dynamically injected webhook CA bundles are excluded from Argo diffs;
TLS verification is not disabled. There are no Secret payloads in generated Git.

Before a **cold initial handoff**, Bareplane checks that every new component
namespace and cluster-scoped platform object is absent. Existing installations
are refused, not silently adopted. This check is not a cluster-wide lock: the
operator must prevent concurrent platform installation during initial handoff.
After handoff, Git is the authority. Before adding this component to an already
managed Git repository, independently review existing namespaces, CRDs, RBAC,
and webhooks for conflicts. Do not overwrite a pre-existing cert-manager
installation. Argo also refuses resources tracked by another Application.
Pruning and cascading deletion remain disabled; disabling the selection does
not uninstall controllers, delete certificates, or remove Secrets.

## Verification

The public fixture is `examples/cert-manager-fixture.yaml`; generated component
assets live under `components/cert-manager` and its test App-of-Apps root under
`examples/gitops/cert-manager-root`. The earlier bootstrap/Argo fixture is
unchanged. Regenerate only this fixture with
`BAREPLANE_UPDATE_CERT_MANAGER_FIXTURE=1 go test ./internal/render/gitops -run TestPublishedCertManagerFixture`.

CI validates real Kustomize output, all Kubernetes/CRD/Application schemas, and
issuer/Certificate resources against the vendored schemas. A required disposable
VM check bootstraps Kubernetes, completes the Argo handoff, deploys cert-manager
from the exact tested Git commit, waits for controller and issuer readiness,
issues a CA and leaf certificate, and verifies its chain and DNS identity with
OpenSSL. A fresh reconciliation must preserve deployment UIDs and generations.
Only public certificates are retrieved for validation; private keys stay inside
the disposable cluster.
