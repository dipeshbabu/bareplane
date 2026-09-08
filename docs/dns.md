# DNS integration

Manual DNS remains the default. Cloudflare automation is explicit and initially
dry-run; merely selecting a provider never supplies credentials or authorizes
record changes. A legacy `provider: cloudflare` configuration can still be used
for bootstrap/diagnostics, but GitOps rendering refuses it until the full
automation contract is provided.

```yaml
spec:
  dns:
    provider: cloudflare
    automation:
      zoneID: 0123456789abcdef0123456789abcdef
      zoneName: example.com
      domain: apps.example.com
      sourceNamespace: apps
      ownerID: fedcba9876543210fedcba9876543210
      mode: dry-run
      tokenSecret:
        name: cloudflare-api
        key: apiToken
        revision: initial
```

The IDs above are examples, not usable credentials or shared defaults. Supply
your own zone ID and a unique ownership ID, for example a freshly generated
32-character lowercase hexadecimal identifier. Never copy another controller's
owner ID to adopt its records. `domain` must be a canonical, explicit subdomain
strictly below `zoneName`; zone-apex, wildcard, IP-address and ambiguous inputs
are refused. Only one zone, managed subdomain and application namespace are
supported. Arbitrary provider flags/API endpoints and plaintext tokens are not
configuration fields.

## Workload scope and permissions

Only annotated **LoadBalancer Services** in `sourceNamespace` are considered:

```yaml
metadata:
  annotations:
    bareplane.io/dns-managed: "true"
    external-dns.kubernetes.io/hostname: web.apps.example.com
    external-dns.kubernetes.io/ttl: "300"
```

The Service must have a provider-assigned load-balancer address or explicitly
configured external IPs/target, consistent with ExternalDNS's Service source.
There is no automatic ingress, NodePort, Pod or arbitrary CRD discovery. Records
outside the managed domain and Services missing the opt-in annotation are ignored.

The controller has only `get/list/watch` permission for Services in the explicit
application namespace. ExternalDNS 0.22's LoadBalancer-only filter does not start
Pod, Node or EndpointSlice informers. A new Role and RoleBinding grant that narrow
access to its ServiceAccount in `external-dns`; no cluster-wide reader or Secret
reader is installed. The source namespace must already exist. Cold handoff
verifies that namespace and refuses any pre-existing reader Role/RoleBinding,
even if apparently identical. It never adopts or creates the application namespace.

## Credentials and enabling writes

Use a Cloudflare API token restricted to Zone Read and DNS Edit for the specified
zone only. The required zone-ID filter makes the provider query that exact zone
rather than enumerate all zones. See the
[upstream Cloudflare guide](https://github.com/kubernetes-sigs/external-dns/blob/v0.22.0/docs/tutorials/cloudflare.md).

`tokenSecret` references a Secret in the controller's `external-dns` namespace.
No Secret or token value is emitted into Git. Supply it through an approved secret
delivery mechanism, or from a private token file through an operator-controlled
Kubernetes command; do not paste token values into shell arguments or Git.

For a cold handoff, Argo creates the controller namespace first. Verify its Argo
ownership before delivering the referenced Secret. The Deployment stays unready
until that Secret/key is available. If handoff times out while credentials are
being supplied, retry with unchanged configuration and owned state; do not delete
receipts or reclaim the controller through bootstrap.

Review dry-run behavior and provider access before changing `mode` to `apply` in
the user-owned GitOps repository. This is explicit authorization for the selected
zone/subdomain, not permission to manage unrelated records. API permission errors
are not successful DNS reconciliation. Argo/Pod health indicates controller
availability, not proof that public DNS is correct; independently verify provider
records and reconciliation metrics. Keep detailed logs private.

Tokens are read when the controller starts. To rotate one, update the Secret
securely, change the public `tokenSecret.revision` identifier, render and publish
the reviewed rollout through Git. The value is an operator revision, never a hash
or copy of the token. Bareplane does not read token contents to trigger a rollout.

## Ownership, updates and recovery

The TXT registry, unique owner ID and fixed `bareplane-` prefix protect ownership.
Unowned or differently owned records are not imported. No owner-migration or
takeover options are exposed. Run only one controller for the managed scope and
avoid overlapping domains across controllers; ExternalDNS does not support leader
election. The one-replica Deployment uses `Recreate` to prevent rollout overlap.

The fixed `upsert-only` policy does not remove DNS records when a Service or its
annotation disappears. Review and clean up obsolete owned records explicitly,
including dangling targets. Updating an owned target can still replace provider
records; Cloudflare uses transactional batches and may fall back to individual
operations after a failed batch. Monitor failures and reconcile before assuming
an update succeeded. It is not a cross-controller transaction or a DNS backup.

Changing ownership IDs, zones or scopes is a reviewed migration, not routine
reconciliation. Disabling selection does not uninstall Argo resources or delete
external records. Existing controller namespaces and permissions require operator
review when enabling automation after an earlier handoff.

## Pins and acceptance

ExternalDNS **0.22.0** is curated from commit
`994f908d4abdfe5fbf38f2f61613ed570432e43a`; `hack/vendor_external_dns.py` verifies
the four source manifests and Apache-2.0 license checksums. The component uses
non-root UID 65532, a read-only root filesystem, dropped capabilities, RuntimeDefault
seccomp, control-plane scheduling support, 100m CPU/128Mi memory requests and a
512Mi memory limit. Large DNS zones can require a separately reviewed capacity
change; the provider reads records from the selected zone before domain filtering.

The public fixture stays in dry-run. CI deploys the exact-source controller through
Argo against an in-memory Cloudflare API on a disposable runner bridge. Test-only
endpoint overrides and egress isolation prevent public Cloudflare traffic. Tests
cover dry-run, provider denial, scoped creation, owned updates, foreign/unowned
record preservation, released-source retention and credential revision rollout.
Only a powerless fake token is used. Real external DNS acceptance always requires
operator authorization and credentials; it is not claimed by these CI tests.
