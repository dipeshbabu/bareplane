# Render a user-owned GitOps repository

```bash
bareplane gitops render ./bareplane.yaml
```

The optional argument selects the configuration file, consistent with the other render commands. Output is always the `gitops/` directory beside that file, not `.bareplane/`. There is no arbitrary destination or force-overwrite option. Rendering works offline without SSH, kubectl, Terraform, a kubeconfig, repository access, or a running Kubernetes cluster.

Configure the [GitOps repository contract](gitops.md) first. For the initial renderer, select `minimal` (also the default when `profiles` is empty), disable GPU and observability, use manual DNS, and select the SOPS extension boundary. SOPS secret delivery is not implemented here and no secret payload is emitted. AI, data, full, Vault, and observability requests fail explicitly until their component issues land. [Cloudflare DNS automation](dns.md) requires explicit scope, ownership and credential references and defaults to dry-run. The broader example configuration intentionally demonstrates future capabilities; it is not a claim that those profiles can already render.

Selection and ordering now come from the [component dependency registry](platform-graph.md). The renderer uses one asset directory per selected GitOps component, never a separate profile tree, and refuses unavailable dependencies before generating any output.

[cert-manager and explicit issuers](certificates.md) are available as an opt-in
component. It uses the same registry and export contract, without changing the
minimal bootstrap/Argo fixture or enabling public DNS/ingress.

[Metrics Server](metrics-server.md) is opt-in and uses verified TLS to both its
kubelets and the aggregation API. Its rendered resource requests scale within
the upstream node envelope; bootstrap PKI is never emitted as an Argo component.

## Review and publish

The export contains the root Application under `bootstrap/`, child Applications under the configured repository `rootPath`, a pinned Argo component under `components/argocd/`, and a minimal profile description. The remote root must not overlap `bootstrap`, `components`, `profiles`, or `readme.md`. All source references point at the configured repository and explicit revision. Root and child Application names are stable, including for maximum-length cluster names.

Copy the reviewed payload to a **separate checkout** of your repository. Exclude `.bareplane-generated.json` and `.bareplane-export.json`; these describe the disposable local export, not GitOps desired state. Commit and publish the payload yourself. Bareplane does not initialize Git, commit, push, or authenticate to Git on your behalf. Do not directly apply the whole export: use the [guarded minimal Argo installer](gitops-install.md); root handoff is a separate phase.

Identical input produces byte-identical output, including the ownership inventory. Re-rendering accepts only an intact Bareplane export for the same cluster. It verifies every file digest and refuses user edits, missing/extra files, extra directories (including a Git checkout), symlinks/special files, malformed markers, and redirected ancestors. A valid unedited export can be replaced after changing configuration or the renderer; stale generated files are removed as part of the staged replacement. To preserve edits, move/copy them into the user checkout before re-rendering. There is no automatic merge of user edits and no silent overwrite fallback.

Cluster identifiers remain YAML strings even when a valid label resembles a
boolean, number, or date (for example `false`, `123`, or `2026-01-01`). Ordinary
identifiers retain their existing bytes; quoting does not change safe-name
installation or handoff contracts.

The bootstrap operation lock serializes export generation with bootstrap and handoff preparation. Persistent execution state and private credentials remain outside the export. No generic configuration serialization is used: only reviewed public GitOps fields and the cluster name are emitted.

## Minimal Argo payload

Argo CD **3.5.2** is vendored from upstream commit `e258ee23c3e52266d407572f4bcdfe7d9ed36cb5`. The reviewed source SHA-256 is `9a87f2b3e14c278f12501eb0ef5c3955b27cf05370ca425381c6a908cf85a5c5`. This avoids a network fetch or mutable remote Kustomize base during rendering/reconciliation. `hack/vendor_argocd.py` checks that checksum before mechanically curating the upstream manifest; its Apache-2.0 license accompanies the export.

The payload contains the namespace, Argo API CRDs, controller/server/repository service, required ephemeral Redis cache, RBAC, configuration, ClusterIP services, and network policies. Dex/SSO, ApplicationSet and notification controllers, ingress, public exposure, image updater, and optional projects are excluded. The ApplicationSet API type remains part of Argo's upstream API contract, without deploying its optional controller. Cilium, kube-vip, CoreDNS, kubeadm, and node preparation are never generated as Argo components.

This is a non-HA bootstrap baseline, not a claim of production HA. It preserves upstream security contexts and patches only the reviewed bootstrap configuration and control-plane scheduling toleration so single-node clusters can run it. Redis and Argo credentials are initialized in-cluster; only empty Secret declarations and Secret references exist in Git. Private repository credentials are not supported.

Argo initially receives this same desired state from bootstrap and then self-manages it from Git after root handoff. The root stays outside its own source path. Initial Applications use server-side apply, refuse shared-resource takeover, disable cascading prune/finalizers, and leave self-heal disabled. Child health requires synchronization and health for the current source contract; pending/running/stale/failed reconciliation must not appear ready merely because older resources were healthy. New component dependency resolution belongs to the graph issue.

## Validation

CI tests deterministic rendering, path/ownership safety, private-input exclusion, actual Kustomize builds, Lua 5.1 health edge cases, strict Argo Applications against the vendored CRD, CRDs against checksum-pinned Kubernetes OpenAPI, and other resources against commit-pinned Kubernetes 1.36 schemas with kubeconform 0.7.0. Static validation does not replace API admission or a real installation/health test; those are covered by the Argo installation and handoff issues.

```bash
go test ./internal/render/gitops ./internal/project ./internal/cli
python -m pip install PyYAML==6.0.3 jsonschema==4.26.0 lupa==2.6
go build -o bin/bareplane ./cmd/bareplane
python tests/gitops/validate.py bin/bareplane /path/to/kubectl
```

The schema test uses temporary public fixtures and fetches pinned public schemas, never a live Kubernetes API or the configured Git repository. Use kubectl 1.36.4 for the same Kustomize implementation as CI.

References: [upstream installation and compatibility](https://argo-cd.readthedocs.io/en/stable/operator-manual/installation/), [Application health customization](https://argo-cd.readthedocs.io/en/stable/operator-manual/health/), and [vendored release source](https://github.com/argoproj/argo-cd/tree/e258ee23c3e52266d407572f4bcdfe7d9ed36cb5/manifests).
