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

## Render the inventory

Once the bootstrap configuration is complete:

```bash
bareplane bootstrap render
```

Bareplane writes a managed inventory beside the project configuration:

```text
.bareplane/
  bootstrap/
    .bareplane-generated.json
    inventory.yaml
```

The inventory contains deterministic `control_plane` and `workers` groups. Each machine includes `ansible_host`, `ansible_user`, `ansible_port`, and non-secret Bareplane metadata such as its node group, role, provider target when configured, GPU intent, CPU, memory, and disk capacity.

The inventory renderer is offline. It performs no SSH connection and does not run Ansible. Re-rendering safely replaces only a directory that carries Bareplane's matching generation marker; an unrelated or symlinked destination is refused.

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
- `ansible-playbook` is installed.

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

## Secret boundary

`privateKeyFile` is a local path reference. Configuration validation and inventory rendering do not read the file. The path and private-key contents are deliberately omitted from `inventory.yaml` and are not used by the SSH service check.

The existing Proxmox provisioning SSH block serves a different purpose: it references a public key that cloud-init places on newly provisioned machines. The bootstrap SSH block identifies the corresponding local private key that a future authenticated SSH or Ansible runner will use.

Future execution code must keep private-key contents out of logs, generated inventory, command-line arguments, and persisted Bareplane metadata.

## Current limitation

Bareplane can validate bootstrap connectivity, render the inventory, verify local readiness, confirm that configured endpoints expose an SSH service, and persist explicitly approved host identities. It does not yet authenticate over SSH or run Kubernetes bootstrap. Those execution capabilities remain separate changes so connectivity and mutation boundaries stay reviewable.
