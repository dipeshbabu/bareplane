#!/usr/bin/env python3
"""Generate a checksum-pinned formula from a complete verified release set."""

import argparse
from pathlib import Path

from release_artifacts import REPOSITORY, ReleaseError, verify_release


def formula(manifest, base=None):
    base = base or 'https://github.com/' + REPOSITORY + '/releases/download/v' + manifest['version']
    lines = ['class Bareplane < Formula', '  desc "Declarative Kubernetes platform on Proxmox"',
             '  homepage "https://github.com/' + REPOSITORY + '"', '  version "' + manifest['version'] + '"',
             '  license "MIT"', '']
    for target_os, block in [('darwin', 'on_macos'), ('linux', 'on_linux')]:
        lines.append('  ' + block + ' do')
        for arch, condition in [('arm64', 'on_arm'), ('amd64', 'on_intel')]:
            artifact = next(item for item in manifest['artifacts'] if (item['os'], item['arch']) == (target_os, arch))
            lines += ['    ' + condition + ' do', '      url "' + base + '/' + artifact['filename'] + '"',
                      '      sha256 "' + artifact['sha256'] + '"', '    end']
        lines += ['  end', '']
    lines += ['  def install', '    bin.install "bareplane"', '    pkgshare.install "LICENSE", "THIRD_PARTY_NOTICES.txt"',
              '  end', '', '  test do', '    assert_match version.to_s, shell_output("#{bin}/bareplane version")',
              '    system bin/"bareplane", "init", testpath/"bareplane.yaml"',
              '    assert_match "is valid", shell_output("#{bin}/bareplane validate #{testpath}/bareplane.yaml")',
              '  end', 'end', '']
    return '\n'.join(lines)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    args = parser.parse_args()
    try:
        print(formula(verify_release(args.directory)), end='')
    except (ReleaseError, OSError, ValueError, KeyError, TypeError) as error:
        raise SystemExit(str(error))
