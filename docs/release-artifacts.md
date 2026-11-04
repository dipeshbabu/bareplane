# Reproducible release artifacts

`hack/release_artifacts.py` builds a complete artifact set from a clean, exact
source commit. It never creates a tag, GitHub release, installer publication or
Homebrew tap. Publication and installation have separate gates.

```sh
python hack/release_artifacts.py build \
  --version 0.1.0-rc.1 \
  --source-sha "$(git rev-parse HEAD)" \
  --output /path/to/a/new/release-directory
python hack/release_artifacts.py verify /path/to/a/new/release-directory
```

Use an exact `MAJOR.MINOR.PATCH` version or a numbered `alpha`, `beta` or `rc`
prerelease. Versions, source SHA and output names are closed contracts; they are
never evaluated by a shell. Existing output directories are preserved. A source
change during the build prevents manifest creation and leaves partial diagnostic
output that cannot pass verification.

## Contents

The complete set contains four executable archives for Linux/macOS on amd64/arm64,
an SPDX 2.3 SBOM, a versioned manifest, and `SHA256SUMS`. Each archive contains only:

- `bareplane`, with executable mode `0755`;
- the project `LICENSE`;
- `THIRD_PARTY_NOTICES.txt`, including the Go license, linked module notices, and
  vendored payload licenses/notices.

The manifest binds version, source commit/date, actual Go toolchain, platform,
archive size/digest and SBOM size/digest. The SBOM is generated from the modules
actually linked into the binaries, not every test-only module in `go.sum`.
License conclusions that have not been separately audited remain `NOASSERTION`;
the full notice text is distributed rather than guessed.

## Build and verification boundary

Builds disable CGO, workspace/config overlays, automatic toolchain switching and
ambient execution overrides. Public modules are downloaded through the checksum-
verified Go proxy and verified before compiling. Local module replacements are
refused. `-trimpath`, fixed version/commit/date values, an empty linker build ID,
and source-commit archive timestamps remove working-directory and wall-clock
inputs. Reproducibility is checked with the same selected toolchain.

CI pins the release compiler in `.release-go-version`; the normal Go quality gate
continues to check the module's minimum toolchain. The required **Release artifacts**
job builds all four platforms twice, compares the complete outputs, verifies the
manifest/SBOM/archive boundaries, and runs the native Linux CLI's version, help,
initialization and validation commands. CI preview artifacts are retained for
seven days and are not certified releases.

The verifier refuses missing or unexpected files, platform substitutions,
checksum changes, redirected files, archive links/traversal, invalid permissions,
oversized payloads and an SBOM that does not describe the actual artifact set.
Archive verification does not execute downloaded foreign binaries.

macOS binaries support the CLI's portable operations. Kubernetes bootstrap
execution still requires the supported Linux/WSL controller; packaging does not
change that runtime guard.

## Release decision

A passing artifact build is one prerequisite. It does not certify a physical
Proxmox lab or authorize stable publication. The
[release checklist](release-checklist.md), exact-source CI gate, provenance policy
and operator evidence remain mandatory for the release decision. Never create
successful hardware evidence from mocked tests, hosted Kubernetes VMs or a local
CUDA smoke test.
