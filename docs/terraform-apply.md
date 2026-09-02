# Terraform apply

`bareplane terraform apply` is the first Bareplane workflow that can mutate infrastructure. It is intentionally narrower than the Terraform CLI: Bareplane applies only the exact saved plan produced and attested by `bareplane terraform plan`.

## Workflow

Use the two-step Terraform flow:

```bash
bareplane render
bareplane terraform plan

# Review the plan output carefully.
bareplane terraform apply --approve <cluster-name>
```

For a configuration stored somewhere other than `bareplane.yaml`, pass the same path to both commands:

```bash
bareplane terraform plan path/to/bareplane.yaml
bareplane terraform apply --approve my-cluster path/to/bareplane.yaml
```

The approval value must exactly equal `metadata.name`. Bareplane does not accept `yes`, `true`, `--auto-approve`, or arbitrary Terraform arguments as substitutes.

## Preconditions

Before Terraform apply can start, Bareplane requires all of the following:

- a provisioning-ready Bareplane configuration
- Proxmox API token components in the Bareplane environment variables
- Bareplane-managed generated Terraform
- a persistent provider dependency lock
- a regular saved Terraform plan
- a regular plan manifest produced by `bareplane terraform plan`
- the same Terraform version used when the plan was created
- SHA-256 matches for the configuration, generated Terraform, provider lock, and saved plan
- exact cluster-name approval

If any attested input changed after planning, Bareplane refuses to initialize or apply the plan and requires a new plan.

## Fixed Terraform command shape

Bareplane invokes Terraform directly without a shell. It does not forward user-supplied Terraform flags.

Immediately before apply, Bareplane restores the attested provider lock and runs initialization with the lock in read-only mode. The mutation command applies only the saved plan artifact. Local backend state and backup paths remain inside the persistent Bareplane state workspace.

The effective command family is restricted to:

```text
terraform version -json
terraform init -backend=false -input=false -no-color -lockfile=readonly
terraform apply -input=false -no-color -auto-approve \
  -state=<persistent-state> \
  -state-out=<persistent-state> \
  -backup=<persistent-backup> \
  <attested-saved-plan>
```

Terraform itself treats passing a saved plan file as approval of that exact plan. Bareplane adds the separate cluster-name approval requirement before it permits that command to run.

## Environment isolation

Bareplane does not place Proxmox credentials in generated Terraform or command arguments. It removes inherited Terraform CLI argument overrides, workspace overrides, Terraform variable overrides, Terraform logging paths, Bareplane token component variables, and uncontrolled `PROXMOX_VE_*` values from the child environment.

It then supplies only the controlled Terraform data directory and the combined Proxmox provider API token required by the provider.

This prevents environment variables such as `TF_CLI_ARGS_apply` from silently changing Bareplane's fixed command shape.

## Attestation invalidation

Bareplane verifies the plan manifest twice: once before Terraform initialization and again after initialization.

Immediately before the Terraform apply command begins, Bareplane deletes the plan manifest. This is deliberate. Once a mutation attempt starts, the saved plan is no longer considered safe to replay through Bareplane.

### Successful apply

After a successful apply:

- the plan manifest is already gone
- the consumed saved plan is deleted
- local Terraform state and backup files are retained with private permissions
- a new plan is required before another apply

### Failed or partial apply

Terraform can successfully change some resources before a later operation fails. Terraform does not automatically roll those changes back. Therefore Bareplane treats every failed apply attempt as potentially partial.

After a failed apply:

- the plan manifest remains deleted
- the saved binary plan is retained only as a diagnostic artifact
- state and backup files are retained and tightened to private permissions
- Bareplane refuses to replay the old plan
- the operator must inspect the failure and run a new `bareplane terraform plan` before retrying

This prevents an old plan from being reused after reality or Terraform state may have changed.

## What apply does not expose

Bareplane does not expose:

- `terraform destroy`
- `terraform import`
- arbitrary `terraform state` mutation
- arbitrary Terraform subcommands
- raw Terraform argument passthrough
- apply without an attested saved plan
- apply from freshly rendered configuration without a separate plan step

Future mutation capabilities should preserve the same issue, review, attestation, and explicit-approval model rather than widening this command into a generic Terraform wrapper.
