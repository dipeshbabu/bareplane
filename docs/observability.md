# Basic observability

The opt-in baseline contains Prometheus **3.14.0** and kube-state-metrics
**2.20.0**, reconciled by one Argo Application in `observability`. It provides
queryable Kubernetes state and a small set of health rules. It does not install
Grafana, Loki, Alertmanager, node exporters, host agents, public ingress or a
collection of unrelated charts. No bootstrap phase owns these components.

```yaml
spec:
  observability:
    storage:
      mode: ephemeral
    retentionHours: 24
```

The block selects `observability`. The existing `features.observability: true`
also requests it, but does not implicitly authorize disposable history: rendering
requires the explicit storage block. Persistent/PVC/hostPath modes are rejected,
not silently converted to ephemeral storage. The baseline supports at most 100
configured machines; larger deployments need a separately reviewed sizing plan.

## Collection and permissions

Prometheus scrapes only itself and the internal kube-state-metrics service, every
30 seconds with a 10-second timeout. It performs no Kubernetes discovery and
mounts no service-account token. There are no remote-read/write destinations,
external scrape targets or runtime credentials in the export. Administrative,
lifecycle/reload and remote-write receiver APIs are not enabled.

kube-state-metrics has read-only cluster permissions for Nodes, Pods, Namespaces
and Deployments. Its seven exposed metric families cover node readiness, pod
phase, container restarts/waiting reasons, namespace phase and desired/available
Deployment replicas. It does not read Secret objects or collect arbitrary
annotations, environment values, custom-resource data or host filesystems.

The baseline rules report unavailable scrape targets after two minutes and
non-Ready nodes / unavailable Deployments after five minutes. Alerts are visible
through Prometheus; no notification delivery is configured. Rules reflect
controller observations and scrape intervals, not instantaneous state.

## Bounds and storage behavior

Each scrape is limited to 10,000 samples, a 10 MB body, 30 labels, 128-byte label
names and 256-byte label values. Exceeding these bounds fails that scrape and is
visible through `up`; it does not silently claim complete collection. Queries
are limited to four concurrent evaluations, 500,000 loaded samples and 30 seconds.
Even a small machine count can exceed the sample bound with many workloads.

Prometheus requests 100m CPU / 128 MiB memory with a 512 MiB limit. The exporter
requests 50m / 64 MiB with a 256 MiB limit. Both run non-root with read-only root
filesystems and tolerate a single control-plane topology. Services are
ClusterIP-only. They are not authentication boundaries: protect access within
the cluster and use an authorized private kubeconfig for local forwarding.

Prometheus has a 2 GiB `emptyDir` data volume, 1 GiB ephemeral-storage request and
3 GiB ephemeral-storage limit. Retention defaults to 24 hours, accepts 6–168
hours, and also sets a 1 GiB TSDB retention size. The earlier time/size policy wins.
Retention is a cleanup policy, not a hard instantaneous disk-usage ceiling:
write-ahead logs, head chunks and compaction require additional space. Kubelet
eviction may occur under node pressure or filesystem limits. See the upstream
[storage documentation](https://prometheus.io/docs/prometheus/latest/storage/).

History survives a container restart while its Pod's `emptyDir` survives, but is
lost on Pod replacement, rescheduling or node loss. Configuration changes roll
the Prometheus Pod using a public checksum annotation and therefore lose history
in this mode. This is intentional and requires the explicit `ephemeral` choice.
There is no HA, backup, durable retention or remote storage claim.

## Deployment and verification

Render, review and publish the normal GitOps export. Initial handoff refuses an
existing unmanaged observability namespace or conflicting cluster RBAC. Do not
precreate the namespace or install another copy of these resources. After
handoff, the existing Argo owner is authoritative; do not rerun bootstrap
installation to take ownership back.

Use local forwarding with an already-authorized private kubeconfig:

```bash
kubectl --kubeconfig /private/admin.conf -n observability port-forward \
  service/bareplane-prometheus 9090:9090 --address 127.0.0.1
```

Check `/-/ready`, the target status page and queries such as:

```promql
sum(up{job=~"prometheus|kube-state-metrics"})
kube_node_status_condition{condition="Ready",status="true"}
kube_deployment_spec_replicas - kube_deployment_status_replicas_available
```

The first expression should report two healthy scrape targets. Readiness of the
Prometheus process alone does not prove successful collection; inspect target
health and freshness too. A failed scrape, API authorization error, unavailable
exporter or cardinality limit must be investigated rather than bypassed.

For ephemeral corruption or failed recovery, inspect the failure and acknowledge
history loss before deliberately replacing the owned Prometheus Pod. Its
Deployment remains Argo-owned and creates a new Pod with empty storage. Do not
delete host directories or attach an unrelated volume as an implicit repair.

Sources are pinned to Prometheus
[`d7598b71`](https://github.com/prometheus/prometheus/tree/d7598b7141418fa35be2b5ec5d0fefb634199610)
and kube-state-metrics
[`4ffeda2e`](https://github.com/kubernetes/kube-state-metrics/tree/4ffeda2ef866b0fef372849825a803296483b336).
The vendoring script verifies upstream inputs and license notices. CI checks
deterministic rendering, Kubernetes schemas, pinned `promtool` configuration and
rule behavior, real node/workload metrics, Argo ownership, and the documented
history loss/recovery after disposable Pod replacement.
