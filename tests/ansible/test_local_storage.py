import importlib.util
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch


SOURCE = Path(__file__).resolve().parents[2] / 'internal/render/gitops/storage/check.py'
spec = importlib.util.spec_from_file_location('local_storage_check', SOURCE)
storage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(storage)


class LocalStoragePathTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.data = self.root / 'var/lib/bareplane/local-volumes/lab/first/data'
        self.data.mkdir(parents=True, mode=0o700)
        (self.root / 'etc').mkdir()
        (self.root / 'etc/hostname').write_text('lab-control-1\n')
        self.space = SimpleNamespace(f_bavail=20, f_frsize=storage.GIB)

    def inspect(self, **kwargs):
        with patch.object(storage.os, 'fstatvfs', return_value=self.space):
            storage.inspect(self.root, 'lab', 'first', 'lab-control-1', 1, kwargs.get('mounts', []), owner=os.getuid())

    def test_empty_owned_directory_is_checked_without_modification(self):
        before = self.data.stat()
        self.inspect()
        self.assertEqual(list(self.data.iterdir()), [])
        self.assertEqual(self.data.stat().st_mtime_ns, before.st_mtime_ns)

    def test_existing_data_is_preserved_and_refused(self):
        sentinel = self.data / 'unrelated'
        sentinel.write_text('keep')
        with self.assertRaises(storage.Refusal):
            self.inspect()
        self.assertEqual(sentinel.read_text(), 'keep')

    def test_symlink_ancestors_are_refused(self):
        parent = self.root / 'var/lib/bareplane/local-volumes/lab'
        actual = self.root / 'actual'
        parent.rename(actual)
        parent.symlink_to(actual, target_is_directory=True)
        with self.assertRaises((OSError, storage.Refusal)):
            self.inspect()
        self.assertTrue((actual / 'first/data').is_dir())

    def test_publicly_writable_mount_or_wrong_node_are_refused(self):
        self.data.chmod(0o777)
        with self.assertRaises(storage.Refusal):
            self.inspect()
        self.data.chmod(0o700)
        with self.assertRaises(storage.Refusal):
            self.inspect(mounts=[str(self.data)])
        with self.assertRaises(storage.Refusal):
            self.inspect(mounts=[str(self.root / 'var/lib')])
        (self.root / 'etc/hostname').write_text('foreign-node')
        with self.assertRaises(storage.Refusal):
            self.inspect()

    def test_capacity_reserve_and_wrong_owner_are_refused(self):
        self.space = SimpleNamespace(f_bavail=4, f_frsize=storage.GIB)
        with self.assertRaises(storage.Refusal):
            self.inspect()
        with self.assertRaises(storage.Refusal):
            storage.inspect(self.root, 'lab', 'first', 'lab-control-1', 1, [], owner=os.getuid() + 1)

    def test_mountinfo_escapes_are_decoded_and_malformed_input_is_refused(self):
        data = b'1 2 3:4 / /host/a\\040b rw - ext4 /dev/test rw\n'
        self.assertEqual(storage.mount_paths(data), ['/host/a b'])
        with self.assertRaises(storage.Refusal):
            storage.mount_paths(b'bad')
