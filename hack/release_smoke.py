#!/usr/bin/env python3
"""Verify a native archive and exercise its offline CLI; no infrastructure access."""

import argparse
import json
import os
from pathlib import Path
import platform
import subprocess
import tarfile
import tempfile

from release_artifacts import ReleaseError, build_environment, require, verify_release


def smoke(directory):
    directory = Path(directory)
    manifest = verify_release(directory)
    target_os = dict(Linux='linux', Darwin='darwin').get(platform.system())
    arch = {'x86_64': 'amd64', 'AMD64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get(platform.machine())
    require(target_os is not None and arch is not None, 'Native release smoke tests require a supported Linux/macOS host')
    artifact = next(item for item in manifest['artifacts'] if (item['os'], item['arch']) == (target_os, arch))
    with tempfile.TemporaryDirectory(prefix='bareplane-release-smoke-') as temporary:
        root = Path(temporary).resolve()
        require(root.parent == Path(tempfile.gettempdir()).resolve(), 'Smoke workspace escaped its temporary directory')
        binary = root / 'bareplane'
        with tarfile.open(directory / artifact['filename']) as archive:
            binary.write_bytes(archive.extractfile('bareplane').read())
        binary.chmod(0o755)
        environment = build_environment()

        def run(*args):
            result = subprocess.run([str(binary), *map(str, args)], cwd=root, env=environment,
                                    capture_output=True, timeout=30, check=False)
            require(result.returncode == 0 and not result.stderr, 'Native release CLI smoke test failed')
            return result.stdout.decode()

        version = run('version')
        require(all(value in version for value in [manifest['version'], manifest['sourceCommit'], manifest['sourceDate']]),
                'Release version output does not match its source manifest')
        require('Usage:' in run('init', '--help') and not (root / '--help').exists(), 'Release help attempted to create a project')
        config = root / 'project/bareplane.yaml'
        run('init', config)
        require(config.is_file() and 'is valid' in run('validate', config), 'Release cannot initialize and validate an offline project')
    return dict(version=manifest['version'], source_commit=manifest['sourceCommit'], os=target_os, arch=arch,
                native_smoke='passed', publishing_performed=False)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    args = parser.parse_args()
    try:
        print(json.dumps(smoke(args.directory), sort_keys=True))
    except (ReleaseError, OSError, ValueError, subprocess.SubprocessError) as error:
        raise SystemExit(str(error))
