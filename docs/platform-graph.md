# Component and profile graph

`internal/platform` is the single registry for component identifiers, dependencies, ownership, default enablement, profile membership, implementation status, and sync-wave floors. GitOps rendering resolves this registry before reading assets or writing output; it does not maintain separate copies of each profile's manifests.

The current executable minimal profile remains **Argo CD only**, with bootstrap-owned Cilium as an already-implemented dependency. Cilium appears in the diagnostic dependency graph but is never emitted as an Argo component. Infrastructure, bootstrap, and GitOps ownership are separate values; earlier ownership layers cannot depend on later ones.

## Profile semantics

| Profile | Current resolution |
| --- | --- |
| `minimal` (also the empty-profile default) | Bootstrap Cilium plus GitOps Argo CD; available now. |
| `ai` | Base defaults plus passthrough, host drivers, node discovery, GPU scheduling, storage/model cache, model serving, and Ray; unavailable until their focused implementation issues land. |
| `data` | Base defaults plus storage/recovery, Postgres, Kafka, Spark, Airflow, and Trino; unavailable until storage/recovery and component implementations are verified. |
| `full` | Set union of minimal, AI, and data closures, not a duplicated platform tree. |

Jupyter/OpenWebUI and alternative or supporting data services (Flink, ClickHouse, Redis, Superset) have explicit optional registry entries rather than being enabled indiscriminately. Core platform entries such as metrics-server, cert-manager, external DNS, observability, and secrets backends remain unavailable and opt-in until their component issues land. Registry metadata is not an implementation or deployment claim: pins, complete credential/storage contracts, compatibility, and health checks must be added before availability changes.

The existing `gpu`, `observability`, Cloudflare DNS, and Vault config choices request their corresponding capabilities through the same registry. Default SOPS configuration is still the documented extension boundary, not an implicit request to install an unavailable secret-delivery controller. SOPS and Vault backend selections conflict when both are explicitly selected through the resolver API.

[`spec.components`](component-selection.md) exposes strict explicit selections. Configuration maps profiles, optional enable/disable lists, and existing flags into one registry resolution; the registry itself does not import configuration.

## Resolution and ordering

`Registry.Resolve` produces a diagnostic plan and automatically includes dependencies. Explicitly disabled required/profile dependencies, incompatible selections, unknown references, invalid metadata, and cycles fail. Defaults can be disabled only when they are not also required by a selected profile or dependency. Profiles and enabled component lists are treated as sets.

`Resolution.RequireAvailable` is the render/execution gate. `implemented` is available; `experimental` requires explicit opt-in by a caller; `unavailable` and unknown statuses fail. The CLI renderer does not opt into experimental capabilities and returns no partial file tree when any selected infrastructure/bootstrap/GitOps dependency lacks an implementation.

Resolution uses a deterministic topological traversal and produces wave/identifier ordering. Wave floors follow the existing conventions: foundations at `-30`, operators at `-20`, services at `-10`, and workloads at `0`. A dependency always has an earlier wave; additional depth within a band advances the dependent's wave as needed. Bootstrap/infrastructure entries may have earlier diagnostic waves but are excluded from Argo output. The existing Argo wave remains `-30`, preserving byte-identical minimal exports and the public acceptance fixture.

Registry definitions and returned metadata are copied, so callers cannot mutate later resolutions. Construction validates the entire registry, including unselected future nodes. Topological resolution is `O((V + E) log V)` and bounded to 512 components.

## Adding a component

Create the focused issue required by its milestone first. Add one stable registry entry with ownership, status, dependencies, conflicts, profile membership, and wave floor. Keep it `unavailable` until the component's actual renderer, version pins, contracts, and acceptance tests exist. Add its reviewed assets under `internal/render/gitops/assets/<id>/`; an implemented GitOps component without assets fails explicitly. Rendering then emits its one shared component directory and child Application from the resolved graph, without a profile-specific renderer.

Update graph tests, source/ownership checks, public fixtures when applicable, and the component's live convergence coverage before enabling it. AI capabilities must prove the complete passthrough-to-workload chain; stateful data capabilities require an explicit storage and recovery story. This graph issue installs no new platform component.
