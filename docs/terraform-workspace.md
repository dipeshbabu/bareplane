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
      .operation.lock/        # present only while a Bareplane Terraform operation owns the project
        owner.json
      data/                   # TF_DATA_DIR for Terraform execution
      terraform.tfstate
      terraform.tfstate.backup
      .terraform.lock.hcl
      terraform.tfplan
      terraform.tfplan.json
```

## Generated configuration

`.bareplane/terraform` is owned by `bareplane render`. It is intentionally disposable and can be atomically replaced whenever the desired infrastructure changes.

No Terraform state, provider data, saved plan, plan attestation, or canonical dependency lock that must survive rendering may be stored there.

## Persistent workspace

`.bareplane/state/terraform` is reserved for Terraform execution state. Bareplane creates the state and data directories with private permissions where the operating system supports POSIX modes.

Workspace creation is idempotent. Bareplane refuses a symlink or non-directory at its `.bareplane`, `state`, Terraform state, or Terraform data directory boundaries rather than following an unexpected workspace redirect.

`bareplane terraform plan` uses this workspace directly. `TF_DATA_DIR` points to `data/`, the local state path is passed explicitly to Terraform, and the saved plan is written to `terraform.tfplan`.

The dependency lock file has a persistent canonical path in the state workspace. Before Terraform initialization, Bareplane copies that lock into the generated configuration directory when one exists and uses read-only lock selection. After a successful first initialization, Bareplane persists the generated lock back into the state workspace.

## Operation serialization

Bareplane serializes operations that change Terraform project artifacts. `bareplane render` and `bareplane terraform plan` acquire the same project-scoped operation lock before changing generated configuration, saved plans, provider locks, or related state metadata.

Acquisition uses an atomic directory creation under `.bareplane/state/terraform`. Lock metadata contains only a format version, operation name, process ID, and random ownership token. The directory is private and the metadata file is owner-only on POSIX systems.

Bareplane never automatically deletes an existing lock. An existing lock may represent an active process or a process that terminated unexpectedly. The CLI reports the recorded operation and PID when readable. If a lock remains after a confirmed process exit, inspect the project and remove `.bareplane/state/terraform/.operation.lock` manually before retrying.

A lock instance verifies its ownership token before release, so one operation cannot deliberately release a lock created by another.

## Lifecycle rule

The invariant for Terraform execution is:

> rendering may replace generated configuration, but it must never delete, replace, or silently reinitialize persistent Terraform state.

In addition, Bareplane operations that mutate local Terraform project artifacts must hold the project operation lock for their critical section. This prevents a render from replacing generated Terraform while a Terraform plan is constructing or attesting a saved plan.

This layer remains read-only with respect to external infrastructure: it supports Terraform initialization and planning, but not apply, destroy, import, or Terraform state mutation commands.
