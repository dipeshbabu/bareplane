# Verified release publication

The `Verified release candidate` workflow separates signed candidate generation
from public releases. It accepts only the trusted default-branch dispatch commit,
an exact source SHA and a closed version such as `0.1.0-rc.1`. Inputs reach Python
through environment variables; they cannot choose a script or checkout ref.

## Build a candidate

Dispatch `.github/workflows/release.yml` from `main`, with `source_sha` equal to
that dispatch commit, `version` set to the intended version and `publish=false`.
The latest **push** CI run for that exact source must already have passed every
required job, including `v0.1 acceptance`. A previous passing attempt cannot
replace a newer failed or pending attempt.

The workflow builds and verifies all four platform archives, validates the SPDX
SBOM and runs the Linux CLI smoke check. GitHub OIDC signs build provenance for
every file and an SPDX attestation for the platform archives. The verified
candidate is retained as a workflow artifact for seven days. This path creates
no version tag or GitHub release and requires no physical lab claim.

Consumers can verify an archive against the repository and trusted workflow:

```sh
gh attestation verify bareplane_0.1.0-rc.1_linux_amd64.tar.gz \
  --repo dipeshbabu/bareplane \
  --signer-workflow dipeshbabu/bareplane/.github/workflows/release.yml \
  --source-digest EXACT_40_CHARACTER_SOURCE_SHA \
  --source-ref refs/heads/main --deny-self-hosted-runners
```

Add `--predicate-type https://spdx.dev/Document` to verify the associated SBOM
attestation. Provenance establishes where bytes came from; it does not establish
that the software passed physical Proxmox acceptance.

## Publish an approved release

Publication requires repository immutable releases to be enabled and a `release`
environment with an explicit required reviewer. Configure these repository
settings before requesting publication. The environment approval is a final
human review after candidate construction. No long-lived signing key is needed;
the signing identity is the trusted GitHub workflow.

The default `GITHUB_TOKEN` cannot read repository administration settings. Set a
repository secret named `RELEASE_SETTINGS_TOKEN` containing a fine-grained token
or short-lived GitHub App installation token restricted to this repository with
**Administration: read** (and Metadata: read). It is used only to read immutable
release/environment settings; `GITHUB_TOKEN` handles CI reads, attestations and
release writes. Candidate-only builds need no settings token. Missing or expired
settings access blocks publication rather than silently skipping these checks.

Complete the [release checklist](release-checklist.md) in a genuine authorized
Proxmox lab. Keep its detailed evidence privately. Dispatch with `publish=true`
and a sanitized `operator_evidence` JSON object containing exactly these fields:

| Field | Requirement |
| --- | --- |
| `source_sha` | Exact dispatch/build commit |
| `operator` | GitHub login of the dispatch actor |
| `lab_id` | Non-sensitive lab identifier, at most 64 characters |
| `tested_at` | UTC `YYYY-MM-DDTHH:MM:SSZ`, within seven days |
| `proxmox_version` | Reviewed compatibility reference, optionally with patch version |
| `first_run`, `rerun`, `failure_recovery` | Each `passed`, backed by the checklist evidence |
| `decision` | `approved` |

The workflow validates this operator assertion and records its canonical SHA-256
digest. It cannot independently prove that physical testing occurred. Hosted VM
checks and a local CUDA probe cannot supply this assertion. Credentials, state,
raw logs and private machine addresses do not belong in workflow inputs.

After environment approval, the publisher rechecks CI and operator evidence,
verifies build and SBOM signatures, and creates a new version tag pinned to the
source. It creates a private draft, uploads the complete set without overwrite,
and checks every server-reported asset size and SHA-256 digest. A final CI
attempt/evidence/tag check precedes publication. Publication must report the
release as immutable.

An existing tag or release/draft blocks the operation. Failed uploads or changed
evidence leave the draft private for inspection. Do not blindly retry, delete or
move a version: inspect the existing state and choose a new version when needed.
The workflow never removes an existing tag, release or asset to make a retry pass.

GitHub documents the [attestation action](https://github.com/actions/attest),
[verification options](https://cli.github.com/manual/gh_attestation_verify), and
[immutable release behavior](https://docs.github.com/en/code-security/how-tos/secure-your-supply-chain/establish-provenance-and-integrity/prevent-release-changes).
