# SOPS secret delivery

SOPS is an explicit integration, not a default key distributor. Configure public
references to operator-managed Secrets in the **already-owned** `argocd`
namespace. Bareplane never reads those keys, generates private recipients, or
places key values in its configuration, export, or Git history.

```yaml
spec:
  secrets:
    provider: sops
    sops:
      ageKey:
        name: bareplane-sops-age
        key: keys.txt
      namespaces: [workloads]
      revision: initial
```

The block selects `secrets-sops` and its Argo dependency. `provider: sops` alone
keeps the historical no-integration baseline. Enabling `secrets-sops` without
this block refuses rendering. Vault and SOPS selections conflict.

One or both of `ageKey` and `pgpKey` are required. Each has `name` and `key`;
Secret names must be dedicated `bareplane-sops-*` DNS labels, excluding Argo's
own credentials. Optional `pgpPassphrase` has the same reference shape and
requires `pgpKey`. It supplies a non-interactive GPG passphrase from a file, not
an argument value or environment value. Omit it only for unprotected PGP keys.
Allow 1–32 unique namespaces, excluding `argocd` and `kube-*`. `revision` is a
public DNS label used to roll key consumers; it is never a key fingerprint or
key value. Unknown configuration fields are rejected.

## One Argo owner

The existing Argo Application owns the repo-server Deployment and `argocd-cm`.
The renderer extends those same resources with a SOPS CMP sidecar and its fixed
configuration. There is no second Application patching Argo's Deployment.
The `secrets-sops` Application owns only a validating admission policy/binding.
It refuses adopting unmanaged Secrets, transferring managed Secrets between
Applications, removing their ownership label, and writes from identities other
than the Argo application controller. Deletion is not blocked by the policy;
namespace garbage collection and deliberate operator recovery remain possible.
Use non-pruning Applications and review destructive operations separately.

The sidecar uses the official `ghcr.io/getsops/sops:v3.13.3` Debian image,
[source commit `26e2f478`](https://github.com/getsops/sops/tree/26e2f4784ca61353082c32dbd987c25eda086dc9),
and the installed Argo 3.5.2 CMP server. The Debian image includes Python and GPG;
the Alpine variant is not interchangeable. The reviewed Python wrapper invokes
the official SOPS CLI with full-file MAC verification. It never executes
repository scripts, shell templates, remote keyservices, or cloud KMS helpers.
The integration follows Argo's
[sidecar plugin contract](https://argo-cd.readthedocs.io/en/stable/operator-manual/config-management-plugins/).

The non-root sidecar has a read-only root filesystem, no Kubernetes service
account token, no Git credential forwarding, and a separate 256 MiB
memory-backed temporary volume. It requests 100m CPU/128 MiB memory with a
512 MiB memory limit. Input, output, key files, subprocesses and execution time
are bounded. Missing optional key projections do not stop the Argo control
plane from starting; attempts to decrypt without usable keys fail closed.

## Installation and post-handoff enablement

For a new cluster, configure this block before rendering. Review and publish the
normal export to your repository, install Argo through the guarded workflow,
and complete handoff. This creates the owned Argo namespace before key delivery.
Do **not** precreate `argocd`, install another Argo release, or apply the entire
export manually. The cold installer retains exactly its original 38 resources
and six verified input files; keys are not part of that installation inventory.

For an already handed-off cluster, render/review the changed public desired
state and publish it through the existing Argo Application's Git source.
Wait for that Application and `secrets-sops` to be Synced/Healthy. Do not rerun
`gitops install` or attempt to replace the recorded bootstrap/handoff contract.
Bootstrap does not reclaim handed-off Argo configuration.

Then deliver keys from private files using your existing private kubeconfig:

```bash
kubectl --kubeconfig /private/admin.conf -n argocd create secret generic bareplane-sops-age \
  --from-file=keys.txt=/private/sops-age-keys.txt
```

The command intentionally does not create the namespace or print YAML. Check
ownership first and refuse overwriting a pre-existing unrelated Secret. Use
your secure operator workflow for updates, including UID/resourceVersion
preconditions. Never pipe private Secret YAML into a Git checkout or CI log.
Wait for projected keys or deliberately bump the public `revision` and publish
the rollout. Application namespaces belong to their own workload workflow;
this integration does not create or adopt them.

## Encrypted application contract

Use one `secret.sops.json` per Application source directory. Encrypt with SOPS
3.13.3 and the exact `--encrypted-regex '^(data|stringData)$'`. Supply public age
X25519 recipients with `--age`, full 40-hex PGP fingerprints with `--pgp`, or
both for recovery. Keep plaintext input outside every Git checkout, set private
permissions, and write only the encryption command's successful output into
Git. When using stdin, supply `--filename-override secret.sops.json`.

The decrypted object must be one `v1/Secret`, with explicit `type`, a DNS-label
`metadata.name`, and `metadata.namespace` matching the Application destination
and configured allowlist. Only `Opaque`, `kubernetes.io/tls` (exact `tls.crt` and
`tls.key` keys), and `kubernetes.io/dockerconfigjson` are supported. Choose
exactly one of `data` or `stringData`, with 1–256 nonempty encrypted string
values. The plugin converts `stringData` into `data` for server-side apply.
Keep metadata public: only name/namespace are accepted in the input.

Lists, YAML, empty plaintext values, service-account tokens, input labels or
annotations, finalizers, owner references, immutable Secrets, duplicate JSON
keys, symlinks, unencrypted values and alternate SOPS encryption/MAC filters are
rejected. Input is limited to 1 MiB and decrypted output to 4 MiB. There is no
`--ignore-mac`, `mac_only_encrypted`, key-group, age-plugin, KMS or Vault path.
SOPS authenticates public name/namespace/type as well as encrypted values.

Create the workload Application only after the integration is ready:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: workload-secret
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/your-team/your-gitops.git
    targetRevision: main
    path: secrets/workload
    plugin:
      name: bareplane-sops-v1
  destination:
    server: https://kubernetes.default.svc
    namespace: workloads
  syncPolicy:
    automated:
      prune: false
      selfHeal: false
      allowEmpty: false
    syncOptions:
      - ServerSideApply=true
      - FailOnSharedResource=true
      - DisableClientSideApplyMigration=true
```

## Rotation, recovery and trust

Back up decryption keys separately from the encrypted Git repository. Loss of
every matching key cannot be repaired by Bareplane or by replaying Git.
During rotation, first deliver a key file containing the old and new age
identities (or PGP private keys), publish a new public `revision`, and wait for
the same Argo-owned Deployment to roll out. Re-encrypt with the new recipients,
review/publish ciphertext, and verify each Secret Application. Remove old keys
only after old ciphertext is no longer needed for recovery. Git history still
contains old ciphertext and may still require old keys. Rotate the underlying
service credentials too if a decryption key was compromised.

Wrong/missing keys, invalid MACs and unsupported input produce a fixed failure
message with no decrypted output. Existing Secrets remain unchanged. Fix the
input or securely restore the key, then hard-refresh/retry the Application;
Argo caches manifest-generation failures. Removing this integration does not
erase live Secrets or Git history, and pruning is not enabled automatically.

This is not a multi-tenant sandbox or protection against Argo/cluster/Git
administrators. Decrypted manifests necessarily pass through Argo memory and
its Redis manifest cache, and Secrets exist in the Kubernetes API/etcd. Protect
Argo access, its network/cache, kubeconfigs, Git write access, cluster backups
and encryption at rest. Do not export Argo manifests, turn on verbose data
logging, or treat base64 as encryption. See Argo's
[secret-management security guidance](https://argo-cd.readthedocs.io/en/stable/operator-manual/secret-management/).

CI uses throwaway age and passphrase-protected PGP keys outside a local Git
repository. It checks full MAC tampering, missing/wrong keys, rotation,
unmanaged Secret refusal, unchanged Argo/Secret identities, plaintext exclusion
from Git objects and bounded controller logs, and actual Argo reconciliation.
The public SOPS overlay under `examples/gitops/sops-argocd` is a CI fixture;
ordinary user exports keep `components/argocd` as their single source path.
