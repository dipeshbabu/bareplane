#!/usr/bin/env python3
"""Install an explicit verified release without Go or unmanaged file replacement."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import tarfile
import tempfile
import urllib.request

from release_artifacts import DIGEST, MAX_ARCHIVE, MAX_BINARY, MAX_TEXT, REPOSITORY, SHA, VERSION, ReleaseError, require, verify_release


def native_target():
    target = ({'Linux': 'linux', 'Darwin': 'darwin'}.get(platform.system()),
              {'x86_64': 'amd64', 'AMD64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get(platform.machine()))
    require(None not in target, 'Use a supported Linux/macOS host, or run this installer inside WSL')
    return target


def read_asset(url, limit):
    with urllib.request.urlopen(url, timeout=120) as response:
        require(response.geturl().startswith('https://'), 'Release download redirected outside HTTPS')
        data = response.read(limit + 1)
    require(0 < len(data) <= limit, 'Release download is empty or exceeded its size limit')
    return data


def download_release(version, manifest_sha256, directory, fetch=read_asset):
    require(VERSION.fullmatch(version) is not None and DIGEST.fullmatch(manifest_sha256) is not None,
            'Installation requires an explicit closed version and trusted manifest SHA-256')
    directory = Path(directory)
    require(directory.is_dir() and not any(directory.iterdir()), 'Downloads require an empty owned staging directory')
    base = 'https://github.com/' + REPOSITORY + '/releases/download/v' + version + '/'
    name = 'bareplane_' + version + '_manifest.json'
    data = fetch(base + name, MAX_TEXT)
    require(hashlib.sha256(data).hexdigest() == manifest_sha256, 'Release manifest differs from the trusted SHA-256')
    manifest = json.loads(data)
    require(manifest.get('version') == version, 'Downloaded manifest names a different version')
    # Derive the closed filename inventory locally, never from untrusted URLs.
    from release_artifacts import TARGETS
    names = ['bareplane_' + version + '_' + target_os + '_' + arch + '.tar.gz' for target_os, arch in TARGETS]
    names += ['bareplane_' + version + '.spdx.json', 'bareplane_' + version + '_installer.pyz', 'SHA256SUMS']
    (directory / name).write_bytes(data)
    for filename in names:
        (directory / filename).write_bytes(fetch(base + filename, MAX_ARCHIVE if filename.endswith('.tar.gz') else MAX_TEXT))
    return verify_release(directory)


def install(directory, prefix, upgrade=False, target=None):
    directory = Path(directory).resolve()
    manifest = verify_release(directory)
    target = native_target() if target is None else target
    artifact = next((item for item in manifest['artifacts'] if (item['os'], item['arch']) == target), None)
    require(artifact is not None, 'No release archive matches this native platform')
    # Refuse redirected managed directories before creating or replacing anything.
    prefix = Path(prefix).expanduser().absolute()
    current = prefix
    while current != current.parent:
        require(not current.is_symlink(), 'Installation prefix contains a redirected directory')
        current = current.parent
    binary_dir, store = prefix / 'bin', prefix / 'lib/bareplane'
    for path in [binary_dir, prefix / 'lib', store]:
        require(not path.is_symlink() and (not path.exists() or path.is_dir()), 'Managed install directory is redirected or not a directory')
    link = binary_dir / 'bareplane'
    previous = None
    if link.is_symlink():
        previous = link.resolve()
        require(previous.parent.parent == store.resolve() and previous.name == 'bareplane', 'Existing binary link is unmanaged')
        receipt = previous.parent / 'installation.json'
        require(receipt.is_file() and not receipt.is_symlink() and receipt.stat().st_size <= MAX_TEXT, 'Existing installation has no bounded ownership receipt')
        record = json.loads(receipt.read_bytes())
        require(set(record) == {'version', 'sourceCommit', 'binarySHA256', 'os', 'arch'}
                and VERSION.fullmatch(record['version']) and SHA.fullmatch(record['sourceCommit'])
                and previous.parent.name == record['version'] + '-' + record['sourceCommit']
                and (record['os'], record['arch']) == target
                and previous.is_file() and not previous.is_symlink()
                and previous.stat().st_size <= MAX_BINARY
                and hashlib.sha256(previous.read_bytes()).hexdigest() == record['binarySHA256'],
                'Existing managed installation changed; preserve it for inspection')
        require(upgrade, 'A managed installation exists; use --upgrade explicitly')
    else:
        require(not link.exists(), 'Existing bin/bareplane is unmanaged and will not be overwritten')
    identity = manifest['version'] + '-' + manifest['sourceCommit']
    destination = store / identity
    require(not destination.exists() and not destination.is_symlink(), 'Version directory already exists; inspect it before retrying')
    binary_dir.mkdir(parents=True, exist_ok=True)
    store.mkdir(parents=True, exist_ok=True)
    # Stage all bytes and ownership metadata on the destination filesystem.
    stage = Path(tempfile.mkdtemp(prefix='.install-', dir=store)).resolve()
    temporary_link = None
    try:
        require(stage.parent == store.resolve(), 'Installation staging escaped its owned store')
        with tarfile.open(directory / artifact['filename']) as archive:
            for name in ['bareplane', 'LICENSE', 'THIRD_PARTY_NOTICES.txt']:
                path = stage / name
                path.write_bytes(archive.extractfile(name).read())
                path.chmod(0o755 if name == 'bareplane' else 0o644)
        record = dict(version=manifest['version'], sourceCommit=manifest['sourceCommit'], os=target[0], arch=target[1],
                      binarySHA256=hashlib.sha256((stage / 'bareplane').read_bytes()).hexdigest())
        (stage / 'installation.json').write_text(json.dumps(record, sort_keys=True) + '\n')
        stage.chmod(0o755)
        # rename is create-only on supported POSIX systems for this nonempty directory.
        stage.rename(destination)
        temporary_link = binary_dir / ('.bareplane-' + stage.name)
        temporary_link.symlink_to(Path('../lib/bareplane') / identity / 'bareplane')
        if previous is None:
            # Hard-linking the symlink is create-only: a concurrent unmanaged
            # destination is preserved rather than silently overwritten.
            os.link(temporary_link, link, follow_symlinks=False)
        else:
            require(link.is_symlink() and link.resolve() == previous, 'Active installation changed during upgrade')
            os.replace(temporary_link, link)
        return dict(version=manifest['version'], source_commit=manifest['sourceCommit'], executable=str(link), installed=True)
    finally:
        if temporary_link is not None and temporary_link.is_symlink():
            temporary_link.unlink()
        if stage.exists():
            require(stage.resolve().parent == store.resolve() and stage.name.startswith('.install-'), 'Refusing cleanup outside owned staging')
            shutil.rmtree(stage)


def main(expected_version=None, expected_source=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', required=True)
    parser.add_argument('--manifest-sha256', required=True, help='Trusted SHA-256 from an independently verified release manifest')
    parser.add_argument('--prefix', type=Path, default=Path.home() / '.local')
    parser.add_argument('--upgrade', action='store_true')
    args = parser.parse_args()
    require(expected_version is None or args.version == expected_version, 'Use the installer published for this exact version')
    native_target()
    with tempfile.TemporaryDirectory(prefix='bareplane-install-download-') as temporary:
        directory = Path(temporary)
        manifest = download_release(args.version, args.manifest_sha256, directory)
        require(expected_source is None or manifest['sourceCommit'] == expected_source, 'Installer and binary source commits differ')
        print(json.dumps(install(directory, args.prefix, args.upgrade), sort_keys=True))


def cli(expected_version=None, expected_source=None):
    try:
        main(expected_version, expected_source)
    except (ReleaseError, OSError, ValueError, KeyError, TypeError, tarfile.TarError) as error:
        raise SystemExit(str(error))


if __name__ == '__main__':
    cli()
