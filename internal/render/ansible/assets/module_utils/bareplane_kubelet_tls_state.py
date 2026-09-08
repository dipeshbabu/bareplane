"""Private crash-safe serving-TLS receipts; callers hold the operation lock."""

import json
import os
from pathlib import Path
import stat
import tempfile


LIMIT = 65536


def private_read(path):
    path = Path(path)
    if not path.is_absolute():
        raise ValueError('Serving TLS state must use an absolute private path')
    for parent in reversed(path.parents):
        info = parent.lstat()
        if not stat.S_ISDIR(info.st_mode):
            raise ValueError('Serving TLS state has redirected ancestors')
    parent_info = path.parent.lstat()
    if parent_info.st_uid != os.geteuid() or parent_info.st_mode & 0o077:
        raise ValueError('Serving TLS state directory must be owner-only')
    try:
        info = path.lstat()
    except FileNotFoundError:
        return None
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077 or info.st_nlink != 1 or info.st_size > LIMIT:
        raise ValueError('Serving TLS state is not a bounded private regular file')
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, 'rb') as stream:
        opened = os.fstat(stream.fileno())
        if (opened.st_dev, opened.st_ino, opened.st_size) != (info.st_dev, info.st_ino, info.st_size):
            raise ValueError('Serving TLS state changed while opening')
        data = stream.read(LIMIT + 1)
    if len(data) > LIMIT:
        raise ValueError('Serving TLS state exceeds its size limit')
    return data


def publish(path, data, previous=None):
    path = Path(path)
    if not isinstance(data, bytes) or not 0 < len(data) <= LIMIT or private_read(path) != previous:
        raise ValueError('Serving TLS state changed before publication')
    descriptor, temporary = tempfile.mkstemp(prefix='.kubelet-tls-', dir=path.parent)
    try:
        with os.fdopen(descriptor, 'wb') as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        if private_read(path) != previous:
            raise ValueError('Serving TLS state changed during publication')
        if previous is None:
            os.link(temporary, path)
        else:
            os.replace(temporary, path)
        # Remove the temporary link before exposing a valid single-link receipt.
        if os.path.exists(temporary):
            os.unlink(temporary)
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


class Record:
    def __init__(self, path, identity):
        self.path = Path(path)
        self.original = private_read(self.path)
        if self.original is None:
            self.data = dict(version=1, identity=identity, state={})
        else:
            self.data = json.loads(self.original)
            if (set(self.data) != {'version', 'identity', 'state'} or type(self.data['version']) is not int or self.data['version'] != 1
                    or self.data['identity'] != identity or not isinstance(self.data['state'], dict)
                    or self.encode() != self.original):
                raise ValueError('Serving TLS receipt belongs to different ownership or is not canonical')

    def encode(self):
        return (json.dumps(self.data, sort_keys=True, separators=(',', ':')) + '\n').encode()

    def save(self, state):
        self.data['state'] = state
        data = self.encode()
        publish(self.path, data, self.original)
        self.original = data
