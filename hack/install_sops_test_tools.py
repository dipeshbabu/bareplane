#!/usr/bin/env python3
"""Install checksum-pinned public fixture tools; never accepts private keys."""

import hashlib
import io
import os
from pathlib import Path
import platform
import sys
import tarfile
import time
import urllib.error
import urllib.request


def download(url, digest, limit):
    for attempt in range(3):
        try:
            with urllib.request.urlopen(url, timeout=90) as response:
                data = response.read(limit + 1)
            break
        except (urllib.error.URLError, TimeoutError):
            if attempt == 2:
                raise
            time.sleep(attempt + 1)
    if len(data) > limit or hashlib.sha256(data).hexdigest() != digest:
        raise RuntimeError('SOPS fixture tool checksum or size mismatch')
    return data


def install(destination):
    if sys.platform != 'linux' or platform.machine() != 'x86_64':
        raise RuntimeError('Disposable fixture tools require Linux amd64')
    destination = destination.resolve(strict=True)
    if not destination.is_dir():
        raise RuntimeError('Fixture tool destination must already be a directory')
    sops = download('https://github.com/getsops/sops/releases/download/v3.13.3/sops-v3.13.3.linux.amd64',
                    'e5bec3346a873ae91d871550f3e698c1aad962aff462a080e40f25fde17fef6b', 64 * 1024 * 1024)
    age = download('https://github.com/FiloSottile/age/releases/download/v1.3.2/age-v1.3.2-linux-amd64.tar.gz',
                   'cbe24006683f8eb669266162894b9a522a1af52f2665fbc63a4bb032ed26ac10', 32 * 1024 * 1024)
    with tarfile.open(fileobj=io.BytesIO(age), mode='r:gz') as archive:
        member = archive.getmember('age/age-keygen')
        if not member.isfile() or member.size > 32 * 1024 * 1024:
            raise RuntimeError('Unexpected age fixture binary')
        keygen = archive.extractfile(member).read(32 * 1024 * 1024 + 1)
    for name, data in [('sops', sops), ('age-keygen', keygen)]:
        path = destination / name
        # Refuse to overwrite a pre-existing tool or follow a redirected path.
        descriptor = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o700)
        with os.fdopen(descriptor, 'wb') as stream:
            stream.write(data)
    print('Installed checksum-verified SOPS 3.13.3 and age-keygen 1.3.2 fixture tools.', flush=True)


if __name__ == '__main__':
    if len(sys.argv) != 2:
        raise SystemExit('usage: install_sops_test_tools.py existing-private-tool-directory')
    install(Path(sys.argv[1]))
