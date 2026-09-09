#!/usr/local/bin/python3
"""Read-only initial local-volume checks; never create, format or remove data."""

import os
from pathlib import Path
import re
import stat
import sys


NAME = re.compile(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\Z')
GIB = 1024 ** 3


class Refusal(Exception):
    pass


def require(condition, reason):
    if not condition:
        raise Refusal(reason)


def mount_paths(data):
    require(len(data) <= 4 * 1024 * 1024, 'mount inventory exceeded its limit')
    paths = []
    for line in data.decode('utf-8', errors='strict').splitlines():
        fields = line.split(' ')
        require(len(fields) >= 10 and '-' in fields, 'mount inventory is malformed')
        paths.append(re.sub(r'\\([0-7]{3})', lambda match: chr(int(match[1], 8)), fields[4]))
    return paths


def inspect(root, cluster, volume, node, required_gib, mounts, owner=0):
    require(isinstance(cluster, str) and NAME.fullmatch(cluster) and isinstance(volume, str) and NAME.fullmatch(volume)
            and len(volume) <= 32 and isinstance(node, str) and NAME.fullmatch(node)
            and type(required_gib) is int and 1 <= required_gib <= 32768, 'local volume contract is invalid')
    root = Path(root)
    relative = ['var', 'lib', 'bareplane', 'local-volumes', cluster, volume, 'data']
    target = str(root.joinpath(*relative))
    for mount in mounts:
        require(not (mount.startswith(str(root) + '/') and (target == mount or target.startswith(mount.rstrip('/') + '/'))),
                'local volume crosses a nested or bind mount')
    descriptors, chain = [], []
    flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC
    try:
        parent = os.open(root, flags)
        descriptors.append(parent)
        root_info = os.fstat(parent)
        require(root_info.st_uid == owner and not root_info.st_mode & 0o022, 'host root ownership is unsafe')
        for name in relative:
            child = os.open(name, flags, dir_fd=parent)
            descriptors.append(child)
            info = os.fstat(child)
            require(stat.S_ISDIR(info.st_mode) and info.st_uid == owner and not info.st_mode & 0o022,
                    'local volume ancestors must be privately controlled directories')
            require(info.st_dev == root_info.st_dev, 'local volume is not on the declared root filesystem')
            chain.append((parent, name, child))
            parent = child
        with os.scandir(parent) as entries:
            require(next(entries, None) is None, 'initial local volume contains existing data')
        available = os.fstatvfs(parent)
        require(available.f_bavail * available.f_frsize >= (required_gib + 4) * GIB,
                'local volume capacity would consume the node reserve')
        # Keep every descriptor open and compare directory identities again so
        # a renamed/replaced path cannot be mistaken for the inspected path.
        for parent, name, child in chain:
            current = os.stat(name, dir_fd=parent, follow_symlinks=False)
            opened = os.fstat(child)
            require((current.st_dev, current.st_ino) == (opened.st_dev, opened.st_ino) and stat.S_ISDIR(current.st_mode),
                    'local volume path changed during inspection')
        etc = os.open('etc', flags, dir_fd=descriptors[0])
        descriptors.append(etc)
        hostname = os.open('hostname', os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC, dir_fd=etc)
        with os.fdopen(hostname, 'rb') as stream:
            info = os.fstat(stream.fileno())
            require(stat.S_ISREG(info.st_mode) and info.st_size <= 256, 'node hostname proof is invalid')
            require(stream.read(257).decode().strip() == node, 'local volume is scheduled on a different node')
    finally:
        for descriptor in reversed(descriptors):
            os.close(descriptor)


def main():
    try:
        require(len(sys.argv) == 5, 'local volume arguments are invalid')
        cluster, volume, node, capacity = sys.argv[1:]
        require(os.environ.get('NODE_NAME') == node, 'local volume node identity differs from its contract')
        with open('/proc/self/mountinfo', 'rb') as stream:
            mounts = mount_paths(stream.read(4 * 1024 * 1024 + 1))
        inspect('/host', cluster, volume, node, int(capacity), mounts)
        print('Initial local-volume directory, node, filesystem and capacity checks passed.')
        return 0
    except Refusal as error:
        print('Local storage refused: ' + str(error), file=sys.stderr)
    except (OSError, ValueError, TypeError, UnicodeError):
        print('Local storage refused: cannot safely verify the declared node directory.', file=sys.stderr)
    return 1


if __name__ == '__main__':
    raise SystemExit(main())
