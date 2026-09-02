# Doctor

`bareplane doctor` runs deterministic preflight checks before infrastructure operations.

```bash
bareplane doctor
bareplane doctor clusters/dev/bareplane.yaml
```

## Current checks

Bareplane checks, in order:

1. The configuration file exists and passes the strict Bareplane schema validator.
2. The configured infrastructure provider resolves through the builtin provider registry and passes provider-specific validation.
3. The provider runtime probe runs. For Proxmox, this loads the API token environment variables and calls the read-only version endpoint over HTTPS to verify credentials and reachability.
4. `terraform` is available in `PATH`.
5. `ansible-playbook` is available in `PATH`.
6. `kubectl` is available in `PATH`.
7. `helm` is available in `PATH` as an optional troubleshooting tool.

Required failures make the command exit non-zero. Warnings are reported but do not fail preflight.

For Proxmox, set `BAREPLANE_PROXMOX_TOKEN_ID` and `BAREPLANE_PROXMOX_TOKEN_SECRET` before running doctor. Token values are not printed, and the Proxmox client redacts the token secret from server-supplied API error messages.

## Not checked yet

Doctor does not yet verify Terraform state, Kubernetes API connectivity, DNS provider credentials, secret-store connectivity, storage health, or available Proxmox capacity. Those checks will be added alongside the corresponding production integrations so diagnostics reuse the same clients and state model.
