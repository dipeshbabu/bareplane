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
3. `terraform` is available in `PATH`.
4. `ansible-playbook` is available in `PATH`.
5. `kubectl` is available in `PATH`.
6. `helm` is available in `PATH` as an optional troubleshooting tool.

Required failures make the command exit non-zero. Warnings are reported but do not fail preflight.

## Not checked yet

Doctor intentionally does not contact Proxmox, Kubernetes, DNS providers, or secret stores yet. Network reachability and credentials will be added alongside those integrations so checks use the same production clients rather than duplicate connectivity logic.
