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
      data/                   # future TF_DATA_DIR
      terraform.tfstate
      terraform.tfstate.backup
      .terraform.lock.hcl
      terraform.tfplan
```

## Generated configuration

`.bareplane/terraform` is owned by `bareplane render`. It is intentionally disposable and can be atomically replaced whenever the desired infrastructure changes.

No Terraform state, provider cache, saved plan, or dependency lock that must survive rendering may be stored there.

## Persistent workspace

`.bareplane/state/terraform` is reserved for Terraform execution state. Bareplane creates the state and data directories with private permissions where the operating system supports POSIX modes.

Workspace creation is idempotent. Bareplane refuses a symlink or non-directory at its `.bareplane`, `state`, Terraform state, or Terraform data directory boundaries rather than following an unexpected workspace redirect.

The dependency lock file has a persistent canonical path in the state workspace. A future Terraform runner may copy or synchronize that lock into the generated configuration directory when Terraform requires it, but rendering must not own the canonical copy.

## Lifecycle rule

The invariant for future execution commands is:

> rendering may replace generated configuration, but it must never delete, replace, or silently reinitialize persistent Terraform state.

This issue defines only the path and filesystem contract. It does not execute Terraform and does not mutate infrastructure.
