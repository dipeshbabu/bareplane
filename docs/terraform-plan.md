# Terraform plan

Bareplane exposes two different read-only planning paths.

`bareplane plan` uses Bareplane's provider-native Proxmox discovery and ownership model. It compares desired machines with observed guests without invoking Terraform.

`bareplane terraform plan [path]` runs Terraform against the deterministic configuration produced by `bareplane render`. It does not apply changes.

## Prerequisites

1. Complete the Proxmox provisioning block in `bareplane.yaml`.
2. Run `bareplane render [path]` so `.bareplane/terraform` contains Bareplane-managed generated configuration.
3. Install Terraform.
4. Export the Proxmox API token components:

```bash
export BAREPLANE_PROXMOX_TOKEN_ID='user@pve!bareplane'
export BAREPLANE_PROXMOX_TOKEN_SECRET='...'
```

Bareplane combines those values only in the controlled child Terraform process environment as `PROXMOX_VE_API_TOKEN`. The token is not placed in generated Terraform or command arguments.

## Execution

```bash
bareplane terraform plan
```

Bareplane invokes Terraform directly without a shell. The Terraform operations used by this workflow are a read-only version probe, initialization, and planning:

```text
terraform version -json
terraform init -backend=false -input=false -no-color
terraform plan -input=false -no-color -detailed-exitcode ...
```

The plan uses the persistent workspace defined in [terraform-workspace.md](terraform-workspace.md):

```text
.bareplane/state/terraform/
  data/
  terraform.tfstate
  terraform.tfstate.backup
  .terraform.lock.hcl
  terraform.tfplan
  terraform.tfplan.json
```

`TF_DATA_DIR` points to the private `data/` directory. Local state is passed explicitly with `-state`, and the saved plan is written explicitly under the persistent state directory.

## Dependency lock lifecycle

If a persistent `.terraform.lock.hcl` already exists, Bareplane copies it into the generated Terraform directory and initializes Terraform with `-lockfile=readonly`. After a successful first initialization, the generated lock is copied back to the persistent workspace with private permissions.

This keeps provider selection stable across `bareplane render`, which may replace the generated Terraform directory.

## Plan attestation

After Terraform returns a successful detailed plan status and the saved plan artifact exists, Bareplane writes `terraform.tfplan.json` beside the plan with owner-only permissions where POSIX modes are available.

The manifest records only:

- manifest format version
- Bareplane cluster name
- Terraform version
- SHA-256 of the selected `bareplane.yaml`
- SHA-256 of the Bareplane-generated Terraform file set
- SHA-256 of the persistent provider dependency lock
- SHA-256 of the saved binary plan

It does not record environment variables, API tokens, private keys, or credential values.

Verification recomputes every digest and checks the Terraform version. Editing the configuration, rerendering Terraform, changing the provider lock, modifying the saved plan, or changing Terraform versions makes the plan stale.

Bareplane removes any previous plan manifest before a new plan starts. Therefore a failed replan cannot leave an older plan marked as valid.

The attestation is the authorization boundary for [Terraform apply](terraform-apply.md). `bareplane terraform apply` refuses to mutate infrastructure unless the manifest still matches the exact current project context and the operator supplies the exact cluster name as approval.

## Plan results

Bareplane uses Terraform's detailed plan exit status:

- `0`: plan succeeded and no changes are present.
- `2`: plan succeeded and changes are present.
- any other nonzero exit: plan failed.

Both `0` and `2` are successful Bareplane plan outcomes.

## Sensitive artifacts

Treat `.bareplane/state/terraform` as sensitive local state. Terraform state and saved plan files can contain infrastructure configuration and values that should not be published. Bareplane creates the persistent workspace with private permissions and tightens the saved plan, plan manifest, and canonical dependency lock to owner-only access on POSIX systems.

The entire `.bareplane` tree is ignored by this repository's default `.gitignore`.

## Review before apply

A successful Terraform plan is still only a preview. Review the plan output before running `bareplane terraform apply --approve <cluster-name>`. Apply consumes only the saved attested plan; it never creates an implicit replacement plan.
