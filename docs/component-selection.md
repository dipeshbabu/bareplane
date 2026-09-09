# Explicit component selection

The optional `spec.components` block controls component selections through the same registry used by profiles and rendering:

```yaml
spec:
  profiles: [minimal]
  components:
    enabled: []
    disabled: [metrics-server, cert-manager]
```

Lists contain stable component IDs, not chart URLs, manifests, credentials, or command overrides. Unknown IDs, malformed names, duplicate entries, enable/disable contradictions, backend conflicts, and disabled required dependencies fail validation. Registry membership is the single source of truth; configuration does not maintain a second ID list.

Profile members and their dependencies are required. In particular, bootstrap-owned Cilium and the Argo foundation cannot be disabled. An optional default may be disabled only when it is not also required by a selected profile, another component, or an existing feature/provider choice. For example, `observability: true` conflicts with explicitly disabling `observability`; an AI profile cannot disable its required GPU host path.

Selection is distinct from availability. A well-formed request for a planned capability can be stored and inspected, but GitOps rendering still refuses unavailable/experimental implementations and emits no partial tree. This contract issue enables no new component. As focused M3 issues land, Metrics Server and cert-manager are candidates for optional minimal defaults; the final core-profile integration must enable only verified implementations. Manual DNS, cluster-local exposure, no automatic storage provisioning, and no implicit secret-key delivery remain the safe baseline.

[SOPS](sops.md) is available only with explicit `spec.secrets.sops` key references and allowed namespaces. The default provider alone still enables no decryption sidecar. Vault remains a separately tracked advanced provider. This block never carries plaintext keys, tokens, secret values, repository credentials, or ownership overrides.

cert-manager is now available explicitly via `enabled: [cert-manager]` or the
[`spec.certificates` issuer contract](certificates.md); it is not yet a default.

[Metrics Server](metrics-server.md) is also explicit, with cert-manager and
bootstrap-owned verified kubelet serving TLS as required dependencies. Its
5,000-node sizing limit is validated only when it is selected.

[ExternalDNS](dns.md) is selected by Cloudflare or explicitly enabling
`external-dns`, but controller rendering requires the full scoped automation
contract. Manual DNS remains controller-free, and dry-run is the automation default.

## Stable acceptance baseline

The public M1/M2 fixture, reference Proxmox acceptance config, and schema/disposable-VM tests explicitly disable optional M3 defaults. This preserves the verified bootstrap/Argo baseline while core capabilities grow. The published Kubernetes payload remains byte-identical; these are configuration selections, not duplicate platform trees.

Future core-component tests must prove their own Argo convergence before default enablement. The full core-profile acceptance test then composes those implemented capabilities through the registry. Optional storage or external DNS/secret integrations require their explicit contracts and operator-provided references, not guessed infrastructure or credentials.

## Ownership and changes

Changing selections changes the desired GitOps payload. Review and publish the generated repository deliberately. Existing installation/handoff contracts refuse implicit adoption or replacement when inputs change. Once handed off, Argo owns steady-state reconciliation; do not try to reclaim components by re-running bootstrap installation. Upgrade/recovery workflows remain separately reviewed operations.

The implementation keeps the dependency direction acyclic: the registry has no dependency on configuration, configuration maps its fields into a registry selection, and rendering consumes the resolved/available plan. Returned selections are copied so callers cannot mutate user intent or later resolutions.
