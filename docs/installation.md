# Installing Bareplane

The repository currently provides source builds and verified CI candidates.
Published release binaries and a public Homebrew tap are available only after
the release approval process has completed. Do not substitute a CI preview for
a certified release or copy placeholder hashes into an installer.

## Build from source today

Use the Go version in `go.mod` or a supported newer compiler:

```sh
go build -o ./bareplane ./cmd/bareplane
./bareplane version
./bareplane init bareplane.yaml
./bareplane validate bareplane.yaml
```

Linux/macOS on amd64/arm64 are the binary packaging targets. On Windows, build
and run inside WSL for Kubernetes bootstrap operations. Portable macOS CLI
commands are supported; bootstrap execution requires the Linux/WSL controller.

## Install a published version

Installing a published binary requires Python 3.10+ and this reviewed repository's
`hack/install_release.py` and `hack/release_artifacts.py`; Go is not required.
Choose an explicit released version. Obtain its manifest and verify its provenance
as described in [release publication](release-publication.md), then record that
verified manifest's SHA-256 independently of the download being installed.

```sh
python3 hack/install_release.py \
  --version "$RELEASE_VERSION" \
  --manifest-sha256 "$VERIFIED_MANIFEST_SHA256" \
  --prefix "$HOME/.local"
```

The installer fetches only exact-version HTTPS release assets, verifies the
trusted manifest digest and the complete archive/SBOM/checksum set, and selects
the native platform. Downloads are bounded and archives cannot introduce links,
paths or extra files. The complete release is checked before the prefix changes.
Add `$HOME/.local/bin` to your shell's `PATH` if needed.

The version lives under `PREFIX/lib/bareplane/VERSION-SOURCE_SHA`, with license,
notices and an ownership receipt. `PREFIX/bin/bareplane` points to that version.
An existing unmanaged binary, foreign link, redirected store or modified managed
binary stops installation and is preserved. Activation uses a staged native
filesystem operation; interrupted staging does not replace the active binary.

For an upgrade, repeat the command with the new version/digest and `--upgrade`.
The previous version stays available for inspection or deliberate rollback.
Installing the same identity again is refused; inspect the existing receipt
instead of deleting it blindly. A failed activation may leave a complete unused
version directory, which can be reviewed before removal.

To uninstall, inspect `PREFIX/bin/bareplane` and its matching
`installation.json`, remove that managed link and the corresponding owned version
directories. Preserve unrelated prefix files and project `.bareplane` state.
Homebrew installations should be upgraded/uninstalled with Homebrew instead.

## Homebrew packaging

Maintainers generate a formula from a complete verified release directory:

```sh
python3 hack/homebrew_formula.py /path/to/verified-release > bareplane.rb
```

The generated formula selects all four Linux/macOS architectures, pins exact
versioned release URLs and SHA-256 values, installs notices and tests offline
initialization/validation. It has no Go dependency. Review it and place it in the
chosen tap's `Formula` directory; generation does not publish a tap. Until that
tap and an approved release exist, there is no public `brew install` command to
advertise.

Required CI jobs test native installation on Linux amd64/arm64 and macOS
amd64/arm64. Both macOS runners also install, test and uninstall the formula from
an isolated local tap backed by checksum-verified CI archives. These local
loopback URLs are only a test fixture; the public generator emits release URLs.
