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

`Config.ValidateBootstrap()` is the stronger prerequisite for future Kubernetes bootstrap workflows. It requires:

- an SSH username;
- a private-key file path;
- a host entry for every desired machine;
- no host entries for machines outside the desired topology;
- a valid SSH port, with 22 used when omitted;
- host values that are IPv4 addresses, IPv6 addresses, or DNS hostnames without schemes, paths, user information, whitespace, or embedded ports.

## Secret boundary

`privateKeyFile` is a local path reference. Configuration validation does not read the file and Bareplane does not serialize private-key contents into generated infrastructure.

The existing Proxmox provisioning SSH block serves a different purpose: it references a public key that cloud-init places on newly provisioned machines. The bootstrap SSH block identifies the corresponding local private key that a future SSH or Ansible runner will use.

Future execution code must keep private-key contents out of logs, generated inventory, command-line arguments, and persisted Bareplane metadata.

## Current limitation

This configuration contract does not connect to machines, probe SSH, generate Ansible inventory, or run Kubernetes bootstrap. Those capabilities are intentionally separate changes so connectivity and mutation boundaries remain reviewable.
