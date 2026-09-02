# Project status

`bareplane status [path]` inspects the local Bareplane project lifecycle without contacting Proxmox or running Terraform.

The command is read-only. It does not create `.bareplane`, change permissions, remove locks, initialize Terraform, or read Terraform state or plan contents.

## Reported fields

- `cluster`: cluster identity from `bareplane.yaml`.
- `provisioning-ready`: whether the configuration contains the fields required for Proxmox rendering and Terraform execution.
- `rendered`: whether `.bareplane/terraform` is a valid Bareplane-managed Terraform render.
- `terraform-state` and `terraform-state-backup`: whether regular persistent state files exist.
- `terraform-lock`: whether the persistent provider dependency lock exists.
- `saved-plan`: whether a saved Terraform plan exists.
- `plan-attestation`: whether a saved-plan manifest exists.
- `operation`: the current Bareplane Terraform operation lock, if any, including its operation name and recorded process ID.
- `next`: the safest normal next command for the observed lifecycle state.

## Saved plan states

A saved plan with a plan attestation is eligible for Bareplane's guarded apply path, subject to the full cryptographic attestation verification performed by `bareplane terraform apply`.

A saved plan without an attestation is diagnostic-only. This is an expected state after a failed or partial apply attempt because Bareplane invalidates the attestation before mutation. Run `bareplane terraform plan` again before any retry.

`status` does not claim that an attested plan is cryptographically current because doing so would require running Terraform to verify the current Terraform version. The apply command remains the authoritative verifier immediately before mutation.

## Operation locks

If an operation lock exists, `status` reports the recorded operation and process ID but does not remove the lock or assert that the process is still alive. If no Bareplane operation is running, inspect the recorded process and follow the manual stale-lock recovery procedure in the Terraform workspace documentation.

## Errors

Missing lifecycle artifacts are normal and do not make `status` fail. Invalid configuration, symlinked state artifacts, malformed operation-lock metadata, unmanaged generated Terraform, or other unsafe local workspace shapes return a nonzero status.
