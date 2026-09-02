# Proxmox

Bareplane's Proxmox runtime client is read only at this stage. It uses the native Proxmox HTTPS API for discovery and health information while infrastructure mutation remains out of scope until the provisioning layer is implemented.

## Endpoint

`spec.provider.endpoint` must be an HTTPS URL such as:

```yaml
provider:
  type: proxmox
  endpoint: https://proxmox.example.com:8006
```

Bareplane does not provide an insecure TLS bypass. The endpoint certificate must validate through the HTTP client's trust configuration.

## API token credentials

Secrets are never stored in `bareplane.yaml`. Set the Proxmox API token identity and secret in the environment used to run Bareplane:

```bash
export BAREPLANE_PROXMOX_TOKEN_ID='root@pam!bareplane'
export BAREPLANE_PROXMOX_TOKEN_SECRET='...'
```

Bareplane sends these using Proxmox's `PVEAPIToken` authorization header. The token secret is redacted from API error messages before they are returned to callers.

Use a dedicated API token with the minimum privileges required for the operation. The current client only exposes read operations.

## Read-only discovery

Bareplane can read cluster guest inventory from `/cluster/resources?type=vm`. The Proxmox runtime provider translates those resources into provider-neutral inventory containing:

- stable provider ID (`qemu/<vmid>` or `lxc/<vmid>`)
- guest name and kind
- current Proxmox host node and status
- configured CPU, memory, and disk capacity
- template state
- sorted guest tags

Memory and disk byte capacities are rounded up to GiB when translated into Bareplane inventory. API response ordering does not affect the resulting inventory order.

Discovery does **not** treat a guest as Bareplane-owned because its name resembles a generated Bareplane machine name. Tags are preserved as observed metadata, but ownership and destructive lifecycle rules require a separate explicit contract before Bareplane can mutate or delete existing guests.

## Client safety

The client uses context-aware requests, a default request timeout, a bounded response body, strict JSON envelope decoding, and typed errors for non-success responses. Tests use an injected TLS test server and never require real Proxmox infrastructure.
