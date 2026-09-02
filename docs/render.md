# Terraform rendering

`bareplane render [path]` converts a provisioning-ready Bareplane configuration into reviewable Terraform without contacting Proxmox and without executing Terraform.

## Output

By default:

```text
bareplane.yaml
.bareplane/
  terraform/
    .bareplane-generated.json
    main.tf.json
```

When a custom configuration path is supplied, `.bareplane/terraform` is created beside that configuration file.

The generated Terraform uses `bpg/proxmox` `~> 0.111.1` and the mature `proxmox_virtual_environment_vm` resource. Machine identities and target placement come from Bareplane's deterministic topology model.

## Provisioning prerequisites

Rendering requires the optional Proxmox provisioning block to be complete. It includes only non-secret intent such as bridge, datastore, cloud-image file ID, SSH username, and SSH public-key file path.

The public-key path is resolved at render time. Relative paths are relative to the configuration file. `~/...` paths are expanded from the current user's home directory. The file is bounded to 64 KiB and its contents are embedded in cloud-init; the path itself is not written into Terraform.

Proxmox API token credentials remain environment-only and are never written to generated Terraform.

## Image source behavior

- `<datastore>:import/<file>` is rendered with `import_from`.
- `<datastore>:iso/<file>` is rendered with `file_id`.

VMs use DHCP cloud-init, the configured bridge and datastore, explicit CPU, memory, and disk capacity, Linux OS type, and Bareplane ownership tags. The QEMU guest agent is disabled initially rather than assuming the selected image includes it.

## Output safety

Bareplane refuses to replace an existing `.bareplane/terraform` directory unless it contains a valid Bareplane generation marker for Terraform output.

Regeneration uses a staged directory replacement. The previous managed directory is moved aside only after the new files have been written and synced. If installing the new output fails, Bareplane attempts to restore the previous directory.

This prevents a render failure from deliberately deleting the last valid generated output and prevents Bareplane from overwriting unrelated user files.

## Execution state

Generated Terraform is replaceable, so Terraform state and execution metadata must not be stored inside `.bareplane/terraform`. Bareplane reserves `.bareplane/state/terraform` as the persistent execution workspace.

See [terraform-workspace.md](terraform-workspace.md) for the state, provider data, dependency lock, and saved-plan lifecycle contract.

Bareplane does not currently execute Terraform or run `terraform apply`. The next execution layer will use the persistent workspace explicitly rather than relying on Terraform's default state location inside the generated directory.
