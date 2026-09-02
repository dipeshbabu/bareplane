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

## Secret boundary

`privateKeyFile` is a local path reference. Configuration validation and inventory rendering do not read the file. The path and private-key contents are deliberately omitted from `inventory.yaml`.

The existing Proxmox provisioning SSH block serves a different purpose: it references a public key that cloud-init places on newly provisioned machines. The bootstrap SSH block identifies the corresponding local private key that a future SSH or Ansible runner will use.

Future execution code must keep private-key contents out of logs, generated inventory, command-line arguments, and persisted Bareplane metadata.

## Current limitation

Bareplane can validate bootstrap connectivity and render the inventory, but it does not yet probe SSH or run Kubernetes bootstrap. Those execution capabilities remain separate changes so connectivity and mutation boundaries stay reviewable.
