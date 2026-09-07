# Bootstrap connectivity

Bareplane separates infrastructure provisioning from Kubernetes bootstrap. The bootstrap layer needs a reliable address for every desired machine before it can generate inventory or run any SSH or Ansible workflow.

## Why addresses are explicit for now

The current Proxmox renderer gives guests DHCP networking and deliberately leaves the QEMU guest agent disabled unless the selected image is known to support it. Bareplane therefore cannot safely assume that Proxmox can report each guest IP address after provisioning.

Until a trustworthy automatic address-discovery mechanism is implemented, bootstrap connectivity is explicit:

```yaml
spec:
  bootstrap:
    ssh:
      user: debian
      privateKeyFile: ~/.ssh/id_ed25519
      port: 22
      hosts:
        homelab-control-plane-1: 192.168.1.101
        homelab-worker-1: worker-1.lab.example.com
```

The host-map keys are deterministic Bareplane machine names. They must exactly match the current desired topology. If a node group grows, `ValidateBootstrap` requires mappings for the new machines before bootstrap can proceed.

## Validation levels

`Config.Validate()` treats bootstrap settings as optional. Existing validation, doctor, planning, rendering, and Terraform workflows therefore continue to work when no bootstrap block exists.

`Config.ValidateBootstrap()` is the stronger prerequisite for bootstrap workflows. It requires:

- an SSH username;
- a private-key file path;
- a host entry for every desired machine;
- no host entries for machines outside the desired topology;
- a valid SSH port, with 22 used when omitted;
- host values that are IPv4 addresses, IPv6 addresses, or DNS hostnames without schemes, paths, user information, whitespace, or embedded ports.

`Config.ValidateKubernetesBootstrap()` builds on this SSH contract and additionally requires the complete versioned Kubernetes settings, compatible pinned component versions, safe non-overlapping network ranges, at least one control-plane machine, and the v0.1 Cilium kube-proxy replacement mode. See [kubernetes.md](kubernetes.md) for that contract.

## Render the bootstrap bundle

Once both the SSH and Kubernetes bootstrap configurations are complete:

```bash
bareplane bootstrap render
```

Bareplane writes an embedded Ansible workspace beside the project configuration:

```text
.bareplane/
  bootstrap/
    .bareplane-generated.json
    inventory.yaml
    ansible.cfg
    site.yaml
    validate.yaml
    host_prepare.yaml
    kubernetes_install.yaml
    api_vip.yaml
    control_plane_init.yaml
    cilium.yaml
    join.yaml
    kubeconfig.yaml
    health.yaml
    group_vars/all/
      cluster.yaml
      connection.yaml
    roles/
      contract/
        tasks/main.yaml
        templates/summary.j2
      ... phase roles ...
```

The inventory contains deterministic `control_plane` and `workers` groups. Each machine includes `ansible_host`, `ansible_user`, `ansible_port`, and non-secret Bareplane metadata such as its node group, role, provider target when configured, GPU intent, CPU, memory, and disk capacity.

`group_vars/all/cluster.yaml` contains only explicit non-secret inputs: the cluster name, pinned component versions, API VIP/port, pod/service CIDRs, Cilium kube-proxy replacement, sorted machine groups, and a deterministic primary control plane. CPU, memory, disk, GPU intent, provider placement, and SSH endpoints stay in inventory host variables. Credentials, provider API endpoints, and SSH private-key references are excluded from every generated file.

The renderer is offline and requires neither Ansible, a reachable host, nor an existing key or trust file. It uses `ValidateKubernetesBootstrap()`; the lower-level inventory renderer still accepts SSH-only configuration. Re-rendering replaces only a directory with Bareplane's matching generation marker and rejects symlinks anywhere in its generated tree or parent path. The persistent `.bareplane/state/bootstrap/known_hosts` survives re-rendering.

The generated connection variables bind strict host-key verification to the inventory's own project known_hosts file. Paths with spaces, quotes, and OpenSSH percent tokens are escaped; `${...}` in a controller path is rejected because OpenSSH expands environment references. Future execution must set the generated `ANSIBLE_CONFIG` explicitly, supply the validated key through `--private-key`, and control environment/extra-variable overrides. Ansible controllers run on Linux/WSL.

CI installs `ansible-core==2.19.9` and checks the generated playbooks and their static role imports. Only `ansible.builtin` content is used, with no external collection downloads. To inspect a generated bundle locally:

```bash
cd .bareplane/bootstrap
export ANSIBLE_CONFIG="$PWD/ansible.cfg"
ansible-playbook --syntax-check site.yaml
ansible-playbook validate.yaml
```

The second command previews the contract entirely on the controller. `site.yaml` orders host preparation, package installation, API VIP, primary control plane, Cilium, joins, kubeconfig, and health. These phases are implemented, including the [full bootstrap health gate](bootstrap-health.md). Use [guarded bootstrap apply](bootstrap-apply.md) for approval, current prerequisites, locking, and phase-aware resume. Syntax validation and contract preview do not indicate a bootstrapped cluster; rerunning the entire site over existing state is not a recovery workflow.

## Kubernetes packages

After `host_prepare`, `kubernetes_install.yaml` installs kubeadm, kubelet, and kubectl at the exact `spec.kubernetes.version` (`-1.1` APT revision) from the matching minor's `pkgs.k8s.io` repository. The embedded upstream signing key is restricted to this repository. Exact version pins and dpkg holds protect the three packages from unattended changes.

The role verifies the prepared containerd 2.2.6 runtime and healthy CRI plugins, then checks all installed Kubernetes binary versions and CRI readiness. It owns `/etc/apt/keyrings/bareplane-kubernetes.asc`, `/etc/apt/sources.list.d/bareplane-kubernetes.sources`, `/etc/apt/preferences.d/bareplane-kubernetes`, `/etc/default/kubelet`, and `/etc/crictl.yaml`. Conflicting files, symlinks, untracked binaries, existing cluster state, and installed versions different from the requested pin are refused before any mutation. Changing the desired version is not an implicit upgrade or downgrade.

Kubelet is enabled for boot, with the containerd endpoint and systemd cgroup driver, but the role does not force a service start before kubeadm has written its configuration. An already running kubelet's expected pre-bootstrap failure/restart state is tolerated. No kubeadm init/join, certificates, or Kubernetes API writes occur in this phase. The disposable VM test checks exact binary versions, package holds, version-change refusals, and zero changes on a repeat application.

## Host preparation

The subsequent Kubernetes package phase also installs and holds `cri-tools` at the matching minor's `.0-1.1` revision, rather than relying on an implicit package dependency for `crictl`.

After verifying the host identities and reviewing `bootstrap preflight`, the generated `host_prepare.yaml` prepares supported Linux machines with non-interactive sudo. It comments fstab swap entries (keeping a backup), disables active swap, persists and loads `overlay`/`br_netfilter`, and configures IPv4/IPv6 forwarding and bridge filtering. Enabled custom systemd swap units and zram generators require operator review first.

The role installs the exact `containerd.io` 2.2.6 package revision 1 for the host distribution from Docker's authenticated APT repository. The Docker release signing key is embedded in the binary, and only the Bareplane repository uses it. APT version pinning and a dpkg hold prevent unattended runtime upgrades. The deterministic containerd v3 configuration enables CRI and systemd cgroups; the role verifies the running server and CRI plugins after startup.

Owned files are `/etc/containerd/config.toml`, `/etc/modules-load.d/bareplane.conf`, `/etc/sysctl.d/90-bareplane.conf`, `/etc/apt/keyrings/bareplane-docker.asc`, `/etc/apt/sources.list.d/bareplane-containerd.sources`, and `/etc/apt/preferences.d/bareplane-containerd`. Existing files must exactly match the generated content before the role will proceed. Symlinked paths, custom runtime services, alternative runtime packages, existing Kubernetes/CNI state, and mismatched containerd versions block preparation before any mutation. Inspect and back up conflicting state before manually removing it or choosing an explicit lifecycle recovery path; the role has no force/adoption switch.

The supported hosts are Debian 12/13 and Ubuntu 22.04/24.04/26.04 on amd64/arm64 with systemd and kernel 5.10 or newer. Python 3 with the distribution's `python3-apt` bindings is required. Linux VM CI exercises unmanaged-file refusal, containerd health, and an unchanged second application. Real Proxmox acceptance remains part of the release gate. The role does not install kubeadm, initialize/join Kubernetes, or configure registry mirrors.

## API virtual IP

The `api_vip.yaml` phase derives the interface and node address from live IP address/route facts. The current bootstrap implementation requires an on-link IPv4 VIP shared by the control-plane LAN. IPv6 configuration remains schema-valid, but this ARP phase explicitly rejects it pending NDP support. Network/broadcast addresses, routed VIPs, ambiguous subnets, and a VIP identical to the node address are rejected.

Before writing anything, the role validates existing manifest ownership and probes for address conflicts with three ARP probes over a bounded four-second interval. Existing owners are allowed only when they are authenticated control-plane peers with a matching managed manifest; unrelated claims or simultaneous probes block the phase. This is a point-in-time network conflict check, not a DHCP reservation. Reserve the VIP outside the DHCP allocation pool.

The role writes an owner-only `/etc/kubernetes/manifests/kube-vip.yaml` with the pinned kube-vip version, host networking, control-plane leader election, and only NET_ADMIN/NET_RAW capabilities. It does not enable Kubernetes Service load balancing. Reapplication requires byte-identical managed content and is idempotent; unmanaged or edited files require explicit recovery.

The primary initially mounts `super-admin.conf`, which kubeadm creates during initialization. After a successful initialization, the control-plane phase must switch it to `admin.conf` and write the cluster-specific `.bareplane-init-complete` marker. Other control planes use `admin.conf`. The VIP role creates neither credentials nor cluster state and performs no kubeadm init/join. VM CI checks an occupied VIP using an isolated network namespace, manifest refusal, interface selection, and an unchanged second application.

## Primary control-plane initialization

`control_plane_init.yaml` acts only on the deterministic primary. It renders kubeadm v1beta4 configuration with the stable VIP endpoint, the observed node IP, desired pod/service CIDRs, stacked etcd, systemd cgroups, and kube-proxy replacement. It requires the matching VIP manifest and exact kubeadm binary version.

Before mutation it checks for ambiguous Kubernetes/etcd state, redirected paths, and earlier initialization attempts. The configuration and a SHA-256 intent record are stored under `/var/lib/bareplane/bootstrap` with private permissions. A recorded but incomplete attempt is never retried or reset automatically. Inspect that state and use explicit recovery after a failed attempt; deleting markers without understanding the host state is not a recovery workflow.

Kubeadm output is capped and written directly to an owner-only file in that private state directory. It may contain join tokens and uploaded-certificate keys, so it must not be logged or copied into generated inventory. Initialization has a 15-minute limit and skips kube-proxy. API readiness through the VIP gates the credential handoff from `super-admin.conf` to `admin.conf`; only then is `.bareplane-init-complete` written. Reapplication verifies the marker and configuration digest and never reinitializes a completed cluster.

Cilium remains a later phase. A working primary API and etcd do not yet imply a Ready Kubernetes node or working pod networking.

## Cilium networking

`cilium.yaml` installs the exact configured Cilium chart with full kube-proxy replacement, Kubernetes IPAM, VXLAN tunneling, and the configured API VIP. It keeps one operator replica during primary bootstrap, before other nodes join. Hubble, Gateway API, ingress, BGP, and L2 announcements stay disabled.

The bootstrap installer currently includes reviewed SHA-256 artifact pins for Cilium 1.20.0 and 1.20.1 and Helm 3.21.4 on amd64/arm64. Newly released Cilium patches require adding their reviewed chart checksums before execution; the broader schema compatibility window does not imply an unknown artifact is installable. Helm lives in a private Bareplane directory and does not replace an operator's Helm installation.

The release is named `bareplane-cilium` in `kube-system`. Bootstrap refuses existing CNI configuration and conflicting resources, and verifies its private intent/completion records, Helm chart/version, and live values on reruns. It never upgrades or reinstalls an unchanged release. Cilium remains bootstrap-owned and must be excluded from Argo CD handoff. Any partial installation, conflicting CNI, or changed networking contract requires explicit recovery or lifecycle planning.

Completion requires bounded readiness checks for the Cilium DaemonSet and operator, the primary node, and CoreDNS, followed by an authenticated API request through the Kubernetes ClusterIP to verify service routing without kube-proxy. A completion record is written only after those checks pass.

## Joining the remaining nodes

`join.yaml` first checks the initialized primary and Cilium, then joins additional control planes in sorted machine-name order, one at a time, followed by workers. A single-primary topology issues no join credentials. The current address discovery requires every joining machine to share the on-link IPv4 VIP LAN.

Each fresh node receives a new ten-minute bootstrap token and pins discovery to the primary CA public-key hash. Additional control planes use kubeadm's encrypted certificate upload and stacked-etcd join. Secret values are suppressed from Ansible output, never enter generated inventory, and live only in a root-only temporary node configuration. Cleanup attempts to remove that configuration, revoke the token, and delete the uploaded certificate Secret even after failure. If connectivity prevents cleanup, tokens expire after ten minutes and kubeadm's uploaded certificates after two hours. Failed cleanup is reported, not ignored.

Join intent binds the cluster CA, machine name, observed IP, role, version, and VIP. Completion also binds the live Kubernetes node UID. Reruns authenticate with the node's own kubelet credential and verify its intended identity, Ready condition, control-plane label/taint, and pinned version. Control planes additionally require a healthy local API, stacked-etcd pod, and kube-vip pod. A successful kubeadm join interrupted before the completion marker can finish these checks without issuing new credentials or rejoining.

Foreign clusters, unmanaged existing nodes, changed identities, and incomplete kubeadm attempts are refused without reset. Inspect `/var/lib/bareplane/bootstrap/join-output` privately; its bounded, owner-only output may contain sensitive join material. Check mode is intentionally unsupported for joining new nodes. Run the complete join playbook, not an inventory-limited subset, so the final gate can verify the exact desired topology.

The implementation follows the supported [kubeadm join](https://kubernetes.io/docs/reference/setup-tools/kubeadm/kubeadm-join/) and [high-availability certificate upload](https://kubernetes.io/docs/setup/production-environment/tools/kubeadm/high-availability/) workflows. Workload labels remain outside bootstrap ownership.

## Private operator kubeconfig

After the complete desired topology is Ready, `kubeconfig.yaml` retrieves the primary's bounded, owner-only admin kubeconfig over the authenticated SSH connection. It verifies the cluster name and CA against the primary, validates the embedded certificate/key pair with controller-side OpenSSL, and rewrites the server to the configured API VIP. Exec plugins, external key/CA paths, bearer tokens, proxy settings, insecure TLS, extra contexts, and ambiguous YAML are refused.

The canonical file is `.bareplane/state/bootstrap/admin.conf`, with mode `0600` inside owner-only state directories. It is separate from generated output and survives re-rendering. Atomic first publication never overwrites an existing file. A matching, integrity-checked, unexpired managed credential is preserved on reruns; an unmanaged file, changed CA/cluster/VIP, symlink, insecure permissions, or expired credential requires explicit recovery. Credential renewal belongs to an explicit lifecycle operation, not an implicit overwrite during bootstrap.

Use it without modifying your global configuration:

```bash
export KUBECONFIG="$PWD/.bareplane/state/bootstrap/admin.conf"
kubectl get nodes
```

The role never changes `~/.kube/config`, prints credential contents, or persists the canonical file under `/tmp`. Ansible's controller environment needs OpenSSL 3; validation checks that the client certificate is signed by the exact cluster CA and matches the embedded key. Check mode validates identity and predicts a missing-file write without persisting credentials.

After a cluster is reset or its infrastructure destroyed, stop using and explicitly remove this project-local credential as part of that lifecycle operation. Bareplane must not adopt an old credential for a rebuilt cluster with a new CA. Losing only the local file is recoverable by rerunning `kubeconfig.yaml` against the same fully formed cluster. Do not delete ownership metadata merely to bypass a cluster-identity refusal. The guarded reset command owns automated credential withdrawal in issue #67; infrastructure destruction remains Terraform-owned.

## Local bootstrap doctor

Before any remote bootstrap workflow, run:

```bash
bareplane bootstrap doctor
```

This is a local-only readiness check. It verifies:

- the bootstrap configuration is complete;
- `.bareplane/bootstrap` is a valid Bareplane-managed render;
- `inventory.yaml` is a regular file;
- the configured SSH private key exists as a regular, non-symlink file;
- on POSIX systems, the private key is not group- or world-readable;
- `ssh` is installed;
- `ssh-keyscan` is installed;
- `ansible-playbook` is installed;
- `kubectl` is installed;
- `openssl` is installed.

The command does not read or print private-key contents, contact any configured host, invoke SSH, run Ansible, call Proxmox, or mutate the project.

## Remote SSH service check

After local readiness passes, run:

```bash
bareplane bootstrap check
```

This preflight probes every configured machine in deterministic name order. For each host Bareplane:

1. opens a timeout-bounded TCP connection to the configured SSH port;
2. reads only the first SSH identification line, capped at 255 bytes;
3. requires a CRLF-terminated `SSH-2.0-` or compatibility `SSH-1.99-` identification;
4. closes the connection.

Bareplane sends no private key, password, API token, SSH authentication packet, SSH command, or application payload. Failure output intentionally does not echo raw network errors or complete server banners.

A PASS proves only that the configured endpoint is reachable and presents an SSH service. It does **not** verify the server host key, machine identity, SSH authentication, authorization, Python availability, sudo access, or Ansible compatibility.

## SSH host identity trust

After reachability passes, discover and review every endpoint's public SSH host keys:

```bash
bareplane bootstrap trust
```

Bareplane runs timeout-bounded, output-capped `ssh-keyscan` discovery without authenticating. Results are sorted by machine name and key type and display only the machine, endpoint, public-key type, and SHA-256 fingerprint. Bareplane does not open the configured private key, send credentials, start an SSH session, or run a remote command.

Compare the displayed fingerprints with a trusted out-of-band source such as the machine console or provisioning records. To persist the reviewed keys, type the exact `metadata.name` value at the prompt. Any other input exits without creating or changing trust state.

Approved keys are written to:

```text
.bareplane/state/bootstrap/known_hosts
```

This persistent path is separate from the replaceable `.bareplane/bootstrap` render. The file uses OpenSSH known_hosts syntax, formats non-default ports as `[host]:port`, carries a versioned Bareplane management checksum, and is owner-only on POSIX systems. Future authenticated SSH and Ansible operations must use this file with strict host-key checking; bypass modes such as `StrictHostKeyChecking=no` are not supported.

Re-running `bootstrap trust` with the same key set is idempotent and does not prompt or rewrite the file. A changed, added, or removed key is reported and refused. After independently verifying an intentional host replacement or key rotation, use the explicit rotation flow:

```bash
bareplane bootstrap trust --rotate
```

The rotation command shows old and new fingerprints and again requires the exact cluster name before replacing the complete trusted key set. The same rules apply when a custom configuration path is supplied:

```bash
bareplane bootstrap trust --rotate clusters/dev/bareplane.yaml
```

Bareplane refuses an unmanaged, modified, oversized, broadly permissioned, non-regular, or symlinked trust file and refuses symlinks in its state-directory boundary. If trust state is damaged, stop any authenticated bootstrap operation, inspect `.bareplane/state/bootstrap/known_hosts`, verify current fingerprints out of band, remove or relocate the invalid file manually, and run `bootstrap trust` again. Bareplane never silently repairs or adopts unknown trust data.

## Authenticated remote preflight

After approving host identity, authenticate and run the read-only host suitability gate:

```bash
bareplane bootstrap preflight
```

This command requires the complete Kubernetes bootstrap contract, the configured private-key file, and the integrity-checked project known_hosts file. It verifies public-key authentication, non-interactive sudo, supported Linux and architecture versions, kernel and resources, networking, swap, time synchronization, existing container/Kubernetes state, and GPU intent. See [bootstrap-preflight.md](bootstrap-preflight.md) for the complete check, security, and support contract.

## Secret boundary

`privateKeyFile` is a local path reference. Configuration validation and inventory rendering do not read the file. The path and private-key contents are deliberately omitted from `inventory.yaml` and are not used by the SSH service check.

The existing Proxmox provisioning SSH block serves a different purpose: it references a public key that cloud-init places on newly provisioned machines. The bootstrap SSH block identifies the corresponding local private key that a future authenticated SSH or Ansible runner will use.

Future execution code must keep private-key contents out of logs, generated inventory, command-line arguments, and persisted Bareplane metadata.

## Current limitation

Bareplane can validate connectivity, persist approved host identities, and run guarded end-to-end bootstrap with private progress and a verified kubeconfig. Explicit destructive recovery remains issue #67; ordinary apply never performs an automatic reset.
