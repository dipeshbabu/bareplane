# Terraform workspace lifecycle

Bareplane separates replaceable generated configuration from persistent Terraform execution state.

For a project whose configuration is `bareplane.yaml`, the local layout is:

```text
.bareplane/
  terraform/                  # generated configuration; replaceable by render
    .bareplane-generated.json
    main.tf.json
  state/
    terraform/                # persistent execution workspace; never replaced by render
      data/                   # TF_DATA_DIR for Terraform execution
      terraform.tfstate
      terraform.tfstate.backup
      .terraform.lock.hcl
      terraform.tfplan
```

## Generated configuration

`.bareplane/terraform` is owned by `bareplane render`. It is intentionally disposable and can be atomically replaced whenever the desired infrastructure changes.

No Terraform state, provider data, saved plan, or canonical dependency lock that must survive rendering may be stored there.

## Persistent workspace

`.bareplane/state/terraform` is reserved for Terraform execution state. Bareplane creates the state and data directories with private permissions where the operating system supports POSIX modes.

Workspace creation is idempotent. Bareplane refuses a symlink or non-directory at its `.bareplane`, `state`, Terraform state, or Terraform data directory boundaries rather than following an unexpected workspace redirect.

`bareplane terraform plan` uses this workspace directly. `TF_DATA_DIR` points to `data/`, the local state path is passed explicitly to Terraform, and the saved plan is written to `terraform.tfplan`.

The dependency lock file has a persistent canonical path in the state workspace. Before Terraform initialization, Bareplane copies that lock into the generated configuration directory when one exists and uses read-only lock selection. After a successful first initialization, Bareplane persists the generated lock back into the state workspace.

## Lifecycle rule

The invariant for Terraform execution is:

> rendering may replace generated configuration, but it must never delete, replace, or silently reinitialize persistent Terraform state.

The current execution layer is still read-only with respect to infrastructure: it supports Terraform initialization and planning, but not apply, destroy, import, or Terraform state mutation commands.
