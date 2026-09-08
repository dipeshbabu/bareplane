import importlib.util
import os
from pathlib import Path
import sys
import tempfile
import unittest


source = Path(__file__).resolve().parents[2] / 'internal/render/ansible/assets/module_utils/bareplane_kubelet_tls_state.py'
spec = importlib.util.spec_from_file_location('kubelet_tls_state', source)
state = importlib.util.module_from_spec(spec)
spec.loader.exec_module(state)


@unittest.skipUnless(sys.platform == 'linux', 'private POSIX receipt semantics')
class ServingStateTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.path = Path(self.temporary.name) / 'receipt.json'
        self.identity = dict(cluster='lab', node='lab-control-1', nodeUID='node-uid', caSHA256='a' * 64)

    def test_private_first_write_reopen_and_conditional_update(self):
        record = state.Record(self.path, self.identity)
        record.save({'stage': 'prepared'})
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.path.stat().st_nlink, 1)
        reopened = state.Record(self.path, self.identity)
        reopened.save({'stage': 'ready'})
        self.assertEqual(state.Record(self.path, self.identity).data['state']['stage'], 'ready')
        with self.assertRaises(ValueError):
            record.save({'stage': 'stale'})

    def test_wrong_cluster_ca_uid_and_modified_or_nonprivate_state_are_refused(self):
        record = state.Record(self.path, self.identity)
        record.save({'stage': 'prepared'})
        for field in self.identity:
            with self.subTest(field=field), self.assertRaises(ValueError):
                state.Record(self.path, dict(self.identity, **{field: 'foreign'}))
        self.path.chmod(0o644)
        with self.assertRaises(ValueError):
            state.Record(self.path, self.identity)

    def test_symlinks_hardlinks_and_oversized_files_are_refused_without_overwrite(self):
        outside = self.path.parent / 'unrelated'
        outside.write_bytes(b'preserve')
        outside.chmod(0o600)
        self.path.symlink_to(outside)
        with self.assertRaises(ValueError):
            state.publish(self.path, b'overwrite')
        self.assertEqual(outside.read_bytes(), b'preserve')
        self.path.unlink()
        os.link(outside, self.path)
        with self.assertRaises(ValueError):
            state.private_read(self.path)
        self.path.unlink()
        self.path.write_bytes(b'A' * (state.LIMIT + 1))
        self.path.chmod(0o600)
        with self.assertRaises(ValueError):
            state.private_read(self.path)
