# Verified kubelet serving TLS

After a completed Bareplane Kubernetes bootstrap, use the original Linux/WSL
controller and private project state:

```bash
bareplane bootstrap render ./bareplane.yaml
bareplane bootstrap kubelet-tls --approve lab ./bareplane.yaml
```

Replace `lab` with the exact cluster name. This bootstrap-owned PKI maintenance
operation refuses unfinished formation, changed inventory/trust, check mode,
credential-recovery combinations and foreign ownership. It reuses bootstrap's
lock, authenticated preflight, toolchain checks, bounded private logs, deadline
and input-change verification; it never replays kubeadm or exports private keys.

Kubeadm normally starts kubelets with self-signed serving certificates. Enabling
`serverTLSBootstrap` requests CA-signed replacements, but serving CSRs are not
automatically approved. Common-name validation alone is insufficient.
See [Kubernetes certificate management](https://kubernetes.io/docs/tasks/administer-cluster/kubeadm/kubeadm-certs/#kubelet-serving-certs).

## Ownership and approval

Each node is processed serially. Bareplane verifies its existing primary/join
receipts, CA and Node UID, and authenticates with the machine's kubelet credential.
Trusted SSH independently verifies hostname, machine ID and IPv4 interface on
the VIP network; API self-reported addresses are not sufficient. The running
kubelet must use the reviewed binary/configuration paths without TLS/auth
command-line overrides.

The controller parses YAML strictly and changes only `serverTLSBootstrap: true`.
Duplicate keys, aliases, custom certificate/key paths, anonymous authentication,
insecure read-only ports and non-webhook authorization are refused. Original
bytes are backed up privately before an intent receipt and atomic replacement.
Kubelet restarts only for an unfinished owned transition.

Only one pending CSR for the authenticated node can begin approval. The policy
binds API UID/resourceVersion, complete specification digest, serving signer,
requestor/groups, exact node CN/organization and DNS/IP SANs, strong RSA/EC key,
valid SHA-256-or-stronger signature, and server-only usages. Extra identities,
CA extensions, attributes, client authentication and ambiguous requests fail.
The [Kubernetes signer contract](https://kubernetes.io/docs/reference/access-authn-authz/certificate-signing-requests/#kubernetes-signers)
defines the underlying API restrictions; Bareplane adds independent ownership
and inventory verification.

Kubernetes' X509 credential fingerprint in CSR authentication metadata must match
`kubectl auth whoami` performed over trusted SSH with the node's own credential.
Client-certificate rotation does not change the stable machine/CA receipt; if it
occurs before a new approval intent, an older pending request requires review.

Private intent is saved before an exact resourceVersion-conditional approval
update. A lost acknowledgement is accepted only after reading back the same CSR
UID, specification and saved approval condition. Replaced nodes, denials or
foreign approvals never become readiness. The issued leaf must match the key,
subject, SANs, usages, CA signature and expiry. A real TLS connection to port
10250 must present that certificate with CA and IP verification enabled.

Controller cryptography 36 or newer must be available in the Ansible Python
environment; this prerequisite is checked before node changes. OpenSSL 3, pinned
Ansible and kubectl matching Kubernetes remain required. Remote node modules do
not need cryptography or install packages.

## Renewal and recovery

Kubelet submits renewal CSRs automatically; approval remains explicit. Rerun the
same command when one is pending. A new key must pass the complete policy again.
Without a pending renewal, API/TLS checks are read-only: no approvals, receipt
rewrites or kubelet restarts. No blanket auto-approver is installed. CA private
keys stay on control planes and serving keys stay with kubelet.

Monitor certificate expiry and pending CSRs well before expiry. The default
signer lifetime is one year, but cluster policy may shorten it. Verification
refuses expired/premature certificates, lifetimes above 366 days, certificates
outliving the CA, and less than one hour of remaining validity. A past successful
run is not ongoing renewal automation. Multiple requests require private review,
not bulk approval or automatic deletion.

After interruption, retry with unchanged inputs. Node receipts distinguish
prepared/configured/completed restarts; controller receipts distinguish pending
approval/approved/verified serving state. Missing or edited backups, malformed
receipts, symlinks and hard-link ambiguity fail closed for operator inspection.
Never delete receipts to force adoption. Keep diagnostic logs private.

Bootstrap-only reset verifies completed CA-bound TLS metadata before archiving
it with node diagnostics. Finish interrupted TLS maintenance first. Reset removes
owned active node TLS metadata with Kubernetes PKI, preserving unrelated disks.
Controller receipt filenames include the CA hash: a new cluster cannot reuse
an old approval, and old public-identity audit receipts remain private.

Do not overlap maintenance with other administrators changing node identities,
kubelet configuration or PKI. Argo never owns these resources. The current
contract supports one hostname and one verified IPv4 node address; extra
addresses/custom PKI require a reviewed extension, not disabled verification.

CI covers adversarial inputs, interruption, renewal under the same CA and reset
ownership. A required four-node VM check verifies primary, joined control-plane
and worker serving TLS and unchanged reruns. The recovery VM tests TLS before
reset and after rebootstrap under a new CA.
