# Metrics Server

Metrics Server provides the Kubernetes resource-metrics API for `kubectl top`
and CPU/memory autoscaling. It is not a historical monitoring database or a
replacement for an observability stack. Its
[upstream requirements](https://github.com/kubernetes-sigs/metrics-server/tree/v0.9.0#requirements)
include aggregation, webhook authentication/authorization, and trusted kubelet
serving certificates.

The component is opt-in:

```yaml
spec:
  components:
    enabled: [metrics-server]
```

Selection requires the Argo foundation, cert-manager, and the bootstrap-owned
kubelet serving-TLS capability. No optional profile default is changed. Complete
the explicit `bareplane bootstrap kubelet-tls --approve <cluster>` operation
before publishing/enabling Metrics Server. Offline rendering does not approve
CSRs or prove live certificate readiness. Do not attempt to bypass an unready
kubelet by disabling TLS verification.

## Two separate TLS connections

| Connection | Trust | Owner |
| --- | --- | --- |
| Metrics Server to each kubelet | Bootstrap cluster CA, verified node InternalIP and serving certificate | Bootstrap PKI maintenance |
| Kubernetes aggregation layer to Metrics Server | Dedicated cert-manager serving certificate and injected APIService trust bundle | Argo and cert-manager |

Only `InternalIP` is selected for node scraping. The component loads the cluster
CA from its projected service-account volume and never sets
`--kubelet-insecure-tls`. The APIService explicitly keeps TLS verification enabled;
cert-manager injects its trust bundle. A namespaced SelfSigned Issuer and
server-only Certificate follow the upstream chart's
[cert-manager integration](https://github.com/kubernetes-sigs/metrics-server/blob/v0.9.0/charts/metrics-server/templates/certificate.yaml),
without granting client-authentication usage. The serving identity is restricted
to `metrics-server.metrics-server.svc`, uses ECDSA P-256, and renews 30 days before
its 90-day expiry with key rotation. Serving files reload through the Kubernetes
API-server library; readiness must reconverge after certificate and trust updates.
Neither CA material nor private keys are emitted into Git.

## Payload and privileges

Version **0.9.0** is vendored from upstream commit
`2a7c4b2c7d46552ff47f4aeaa3a735c582587ecd`. The reviewed release manifest SHA-256 is
`1cec29a5267809306a2c6ec74a3e449abbb705b4a8beed0c8a1963910f72c79b`.
`hack/vendor_metrics_server.py` checks the source checksum before curating it.
Rendering and Argo use local pinned assets, not a mutable remote base. The
upstream Apache-2.0 license accompanies the payload. Version 0.9 supports
Kubernetes 1.34 and newer, including Bareplane's 1.35/1.36 bootstrap contract.

The workload, ServiceAccount, Service and TLS resources use a dedicated
`metrics-server` namespace. The single deployment preserves upstream non-root,
read-only-root-filesystem, seccomp and dropped-capability settings. A control-plane
toleration permits single-node clusters. TLS Secret files are read-only `0440`
with group access for the non-root workload.

One namespace-scoped RoleBinding in `kube-system` grants this ServiceAccount the
existing `extension-apiserver-authentication-reader` Role. It reads the public
aggregation authentication settings; it does not own or modify that Role,
ConfigMap or namespace. The cold handoff guard permits only this exact binding
shape and requires it to be absent before handoff. Existing or broader bindings
are refused. Remaining RBAC preserves upstream read-only metrics/node access
and delegated authentication. There is no cluster-wide Secret-reading grant.

## Resources and lifecycle

For up to 100 desired nodes, requests are 100m CPU and 200Mi memory, following
the [upstream scaling envelope](https://github.com/kubernetes-sigs/metrics-server/tree/v0.9.0#scaling).
Larger topologies add 1m CPU and 2Mi memory per node; the memory limit is twice
the request. More than 5,000 nodes are refused. Sizing counts nodes without
expanding a topology or overflowing integer arithmetic. High pod density and
autoscaling activity also affect capacity; this non-HA baseline does not claim
load-test certification for every upstream scale limit.

Initial Namespace, issuer, certificate, workload and APIService ordering is
explicit. Only the injected APIService CA bundle is ignored by Argo's diff;
other TLS/ownership drift remains visible. Initial pruning and cascading
deletion stay disabled. Removing selection is not an uninstall or authorization
to delete Secrets, metrics or workloads.

Before adding the component to already handed-off Git, independently review
existing metrics API registration, namespaces and RBAC for conflicts. Do not
overwrite another Metrics Server installation. Bootstrap does not reclaim this
Argo-owned service. Continue the separate kubelet CSR renewal workflow and
monitor APIService availability and certificate expiry.

CI validates the actual Kustomize output and Kubernetes/Argo/cert-manager schemas.
The disposable Metrics Server VM first verifies kubelet TLS, then performs the
Argo handoff and deploys both components from the exact tested commit. It checks
fresh node and pod metrics and `kubectl top`, triggers serving-certificate renewal,
and requires API trust and metrics to recover without replacing the deployment.
The fixture generator verifies shared Argo/cert-manager bytes remain unchanged;
only `components/metrics-server` and `examples/gitops/metrics-server-root` are
regenerated by `BAREPLANE_UPDATE_METRICS_FIXTURE=1 go test ./internal/render/gitops -run TestPublishedMetricsFixture`.
