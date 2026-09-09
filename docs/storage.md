# Explicit static local storage

The minimal storage strategy is an explicit inventory of **retained node-local
directories**. Kubernetes provides the Local PersistentVolume implementation;
Argo owns the StorageClass, read-only inspection Jobs and PV declarations. No
dynamic provisioner, CSI driver, Ceph cluster, disk formatter or automatic data
cleanup is installed. Bootstrap does not own the storage component.

```yaml
spec:
  storage:
    strategy: local-static
    volumes:
      - name: first
        node: lab-control-1
        capacityGiB: 1
```

The block selects `storage`. Selecting the component without an inventory fails
rendering. Define 1–32 unique volume names, each a lowercase DNS label of at most
32 characters. Nodes must match desired Bareplane machine identities exactly.
Capacity is 1–1024 GiB per volume; the declared total on a node must leave at least
4 GiB of configured disk capacity outside this inventory. Arbitrary device and
path inputs, dynamic provisioning and automatic expansion are not supported.

## Prepare and validate the node directories

The operator must reserve and prepare each empty directory on its named node:

```text
/var/lib/bareplane/local-volumes/<cluster>/<volume-name>/data
```

For the example cluster `lab`, this is
`/var/lib/bareplane/local-volumes/lab/first/data` on `lab-control-1`. Use the node's
root filesystem, root ownership, and private directory permissions such as 0700.
Ancestors must be root-owned and not group/world writable. Inspect existing paths
first; do not overwrite, wipe or repurpose an existing directory to make a check
pass. The directories remain operator-prepared prerequisites; Bareplane does not
create them, format disks or allocate filesystem quotas.

Before Argo creates any PV, a normal, one-time Job checks the node hostname,
directory chain, ownership, filesystem identity, nested mounts, empty data
directory and available space. It uses descriptor-relative `O_NOFOLLOW` opens
and rechecks directory identities. Symlinks, foreign filesystems, bind/nested
mounts, existing data, unsafe permissions and insufficient free space refuse
creation. Actual free space must cover the declared node total plus a 4 GiB
reserve. This is a point-in-time check, not a disk reservation or quota; other
workloads can consume space later. Keep these reserved directories unused until
their claims are bound.

The inspector needs a root-UID view of host directory metadata. Its host mount
is explicitly **recursively read-only**, with no writable host mounts, service
account token, added capabilities, privilege escalation, host PID/network or
network egress. The `local-storage` namespace permits this narrow hostPath
exception, so access to that namespace and its Argo source must remain restricted
to trusted administrators. The fixed script reads only the declared path's
metadata and the node hostname; it does not read or publish application data or
host credentials. It uses Python 3.12.14 from the official
[`slim-bookworm` image source](https://github.com/docker-library/python/tree/688a0b86bb44289df16a363e9f41d90514c1a5f9/3.12/slim-bookworm).

Recursive read-only support is mandatory, not best-effort. It requires a
supported kernel/runtime; the reference Kubernetes 1.36.4 / containerd 2.2.6
Ubuntu path provides it. An unsupported runtime refuses the Pod. See Kubernetes'
[recursive read-only mount contract](https://kubernetes.io/docs/concepts/storage/volumes/#recursive-read-only-mounts).

## Binding and workload ownership

`bareplane-local` is a non-default `kubernetes.io/no-provisioner` StorageClass,
with `WaitForFirstConsumer`, `Retain`, and no expansion. Each PV is a Filesystem
`ReadWriteOnce` volume with required node affinity. Capacity is advertised for
scheduling; this strategy does not enforce per-directory capacity limits or
provide cross-node replication.

Workload owners create their own PVCs and Pods. For example, in an existing
workload namespace:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: local-data
  namespace: workloads
spec:
  storageClassName: bareplane-local
  accessModes: [ReadWriteOnce]
  selector:
    matchLabels:
      bareplane.io/local-volume: first
  resources:
    requests:
      storage: 1Gi
```

Let the scheduler bind the consumer, or use node selectors/affinity. Do not set
the consumer's `nodeName` to bypass scheduling with WaitForFirstConsumer. A PVC
without an eligible local volume stays Pending; no fallback disk or path is
created. Application permissions and `fsGroup` determine access to the mounted
data directory after binding.

Initial handoff refuses an existing unmanaged `local-storage` namespace,
StorageClass or PV. Do not precreate platform objects or install another storage
owner. The workload namespace and claims are separate from platform ownership.
Argo ignores only the PV's live `spec.claimRef`, preserving Kubernetes' binder
ownership rather than replacing its claim UID during reconciliation.

## Retention and recovery

Data survives consumer Pod deletion/recreation on the same available node.
When that node is unavailable or cordoned, a consumer cannot move its local data
to another node and remains Pending. Restoring the node restores access; node or
root-disk loss can permanently lose data. There is no HA or backup guarantee.

Deleting a PVC leaves a Released PV and retained directory contents. The old
claim UID remains recorded. Argo does not clear that binding, delete the data or
automatically reassign it. Reuse, migration and recovery require a reviewed
operator procedure, preserved node/path identity and appropriate backups. Never
delete directories or clear claim references as an implicit repair.

Completed inspection Jobs remain as initial validation records and are not
recreated on ordinary sync. Treat the initial inventory, node placement,
capacity and checker version as a lifecycle boundary. Changes can require new
validation and will refuse nonempty directories; automatic expansion, relocation
and revalidation/adoption of retained data are not implemented. A missing or
failed inspection Job is not permission to discard existing data.

For an initial refusal, inspect its fixed reason, verify that no PV was created,
and correct only the operator-owned prerequisite. After checking identity, a
failed disposable inspection Job can be deliberately removed and retried through
the same Argo owner. Do not remove successful audit records to bypass a changed
inventory. After handoff, publish platform changes through Git rather than
re-running bootstrap installation.

CI uses disposable guests and a separately created unrelated test disk. It checks
symlink refusal before PV creation, effective recursive read-only mounting,
prepared-volume validation, real PVC scheduling, consumer persistence, cordon/
recovery, retained release/claim UID and preservation of unrelated disk identity
and data. It does not certify an operator's disks or storage capacity.
