#!/usr/bin/env python3
"""Exercise the installed native binary and an isolated Homebrew formula."""

import argparse
import functools
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading

from homebrew_formula import formula
from install_release import install, native_target
from release_artifacts import ReleaseError, require, verify_release


def command(args, **kwargs):
    result = subprocess.run(list(map(str, args)), check=True, capture_output=True, timeout=600, **kwargs)
    return result.stdout.decode()


def smoke(directory, brew=False):
    directory = Path(directory).resolve()
    manifest = verify_release(directory)
    native_target()
    with tempfile.TemporaryDirectory(prefix='bareplane-installed-smoke-') as temporary:
        root = Path(temporary).resolve()
        require(root.parent == Path(tempfile.gettempdir()).resolve(), 'Install smoke workspace escaped its temporary root')
        installed = install(directory, root / 'prefix')
        # Run the shipped standalone application outside any repository checkout.
        require('--manifest-sha256' in command([sys.executable, directory / manifest['installer']['filename'], '--help'], cwd=root),
                'Standalone installer could not run without the source checkout')
        binary = installed['executable']
        # The installed binary must work without the Go compiler on PATH.
        environment = dict(os.environ, PATH='/usr/bin:/bin:/usr/sbin:/sbin')
        version = command([binary, 'version'], cwd=root, env=environment)
        require(manifest['version'] in version and manifest['sourceCommit'] in version, 'Installed CLI source/version differs')
        require('Usage:' in command([binary, 'init', '--help'], cwd=root, env=environment)
                and not (root / '--help').exists(), 'Installed help attempted to create a project')
        command([binary, 'init', root / 'project/bareplane.yaml'], cwd=root, env=environment)
        require('is valid' in command([binary, 'validate', root / 'project/bareplane.yaml'], cwd=root, env=environment),
                'Installed native CLI could not validate its initialized project')
        if brew:
            require(shutil.which('brew') is not None, 'This native formula smoke job requires Homebrew')
            existing = subprocess.run(['brew', 'list', '--versions', 'bareplane'], capture_output=True, check=False)
            require(not existing.stdout.strip(), 'Preserve an existing Homebrew installation')
            tap = 'bareplane/ci'
            # CI-only native smoke uses a fresh local tap and a loopback server.
            # The public generator always produces immutable release URLs.
            environment = dict(os.environ, HOMEBREW_NO_AUTO_UPDATE='1', HOMEBREW_NO_INSTALL_FROM_API='1')
            command(['brew', 'tap-new', '--no-git', tap], env=environment)
            repository = Path(command(['brew', '--repository', tap], env=environment).strip())
            handler = functools.partial(SimpleHTTPRequestHandler, directory=str(directory))
            server = ThreadingHTTPServer(('127.0.0.1', 0), handler)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                (repository / 'Formula/bareplane.rb').write_text(formula(manifest, 'http://127.0.0.1:' + str(server.server_port)))
                command(['brew', 'install', '--formula', tap + '/bareplane'], env=environment)
                command(['brew', 'test', tap + '/bareplane'], env=environment)
                prefix = Path(command(['brew', '--prefix', tap + '/bareplane'], env=environment).strip())
                require(manifest['sourceCommit'] in command([prefix / 'bin/bareplane', 'version']), 'Homebrew installed a different source')
                command(['brew', 'uninstall', '--formula', tap + '/bareplane'], env=environment)
                require(not (prefix / 'bin/bareplane').exists(), 'Homebrew uninstall left the version executable')
            finally:
                server.shutdown()
                server.server_close()
                thread.join(timeout=10)
    return dict(version=manifest['version'], source_commit=manifest['sourceCommit'], native_install='passed',
                homebrew='passed' if brew else 'not selected', publishing_performed=False)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    parser.add_argument('--homebrew', action='store_true')
    args = parser.parse_args()
    try:
        print(json.dumps(smoke(args.directory, args.homebrew), sort_keys=True))
    except (ReleaseError, OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        raise SystemExit(str(error))
