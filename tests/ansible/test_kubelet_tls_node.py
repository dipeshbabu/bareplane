import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2] / 'internal/render/ansible/assets'


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / filename)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


@unittest.skipUnless(sys.platform == 'linux', 'private POSIX node transitions')
class ServingNodeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.owned = load('tls_node_owned', 'module_utils/bareplane_join_state.py')
        cls.state = load('tls_node_state', 'module_utils/bareplane_kubelet_tls_state.py')
        with patch.dict(sys.modules, {'ansible': types.ModuleType('ansible'), 'ansible.module_utils': types.ModuleType('ansible.module_utils'),
                                     'ansible.module_utils.bareplane_join_state': cls.owned,
                                     'ansible.module_utils.bareplane_kubelet_tls_state': cls.state}):
            cls.node = load('tls_node', 'library/bareplane_kubelet_tls_node.py')

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.config = self.root / 'config.yaml'
        self.original = b'kind: KubeletConfiguration\nserverTLSBootstrap: false\n'
        self.target = self.original.replace(b'false', b'true')
        self.config.write_bytes(self.original)
        self.config.chmod(0o600)
        self.identity = dict(node='lab-control-1', nodeUID='node-uid', caSHA256='a' * 64)
        self.record_path = self.root / 'kubelet-tls.json'
        self.restarts = []
        for name, value in [('CONFIG', str(self.config)), ('STATE', self.root)]:
            patcher = patch.object(self.node, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        patcher = patch.object(self.node, 'process_contract', lambda identity: None)
        patcher.start()
        self.addCleanup(patcher.stop)
        patcher = patch.object(self.owned, 'run', lambda argv: self.restarts.append(argv))
        patcher.start()
        self.addCleanup(patcher.stop)

    def record(self):
        return self.state.Record(self.record_path, self.identity)

    def test_transition_preserves_backup_and_unchanged_rerun_does_not_restart(self):
        self.assertTrue(self.node.configure(self.original, self.target, self.record(), self.identity))
        self.assertEqual(self.config.read_bytes(), self.target)
        self.assertEqual((self.root / 'kubelet-config-before-tls.yaml').read_bytes(), self.original)
        self.assertEqual(self.restarts, [['systemctl', 'restart', 'kubelet']])
        before = self.record_path.read_bytes()
        self.assertFalse(self.node.configure(self.target, self.target, self.record(), self.identity))
        self.assertEqual(self.record_path.read_bytes(), before)
        self.assertEqual(len(self.restarts), 1)

    def test_kubeadm_public_readable_config_mode_is_preserved_but_writable_mode_is_refused(self):
        self.config.chmod(0o644)
        self.node.configure(self.original, self.target, self.record(), self.identity)
        self.assertEqual(self.config.stat().st_mode & 0o777, 0o644)
        self.assertEqual((self.root / 'kubelet-config-before-tls.yaml').stat().st_mode & 0o777, 0o600)
        self.config.chmod(0o664)
        with self.assertRaises(ValueError):
            self.node.configuration()

    def test_interrupted_restart_resumes_recorded_target_without_losing_backup(self):
        with patch.object(self.owned, 'run', side_effect=subprocess.TimeoutExpired('systemctl', 15)):
            with self.assertRaises(subprocess.TimeoutExpired):
                self.node.configure(self.original, self.target, self.record(), self.identity)
        self.assertEqual(self.record().data['state']['stage'], 'configured')
        self.assertTrue(self.node.configure(self.target, self.target, self.record(), self.identity))
        self.assertEqual(self.record().data['state']['stage'], 'complete')
        self.assertEqual((self.root / 'kubelet-config-before-tls.yaml').read_bytes(), self.original)

    def test_unmanaged_enabled_configuration_or_unrelated_changes_are_refused(self):
        with self.assertRaises(ValueError):
            self.node.configure(self.original, self.target + b'tlsCertFile: /foreign\n', self.record(), self.identity)
        self.assertEqual(self.config.read_bytes(), self.original)
        self.config.write_bytes(self.target)
        with self.assertRaises(ValueError):
            self.node.configure(self.target, self.target, self.record(), self.identity)
        self.assertFalse(self.record_path.exists())

    def test_changed_configuration_or_target_after_intent_is_not_overwritten(self):
        self.node.configure(self.original, self.target, self.record(), self.identity)
        self.config.write_bytes(b'operator edit\n')
        with self.assertRaises(ValueError):
            self.node.configure(self.target, self.target, self.record(), self.identity)
        self.assertEqual(self.config.read_bytes(), b'operator edit\n')

    def test_restart_waits_for_exec_but_never_accepts_an_unreviewed_process(self):
        with patch.object(self.node, 'process_contract', side_effect=[self.node.NodeTLSRefusal('fork before exec'), None]) as verify:
            with patch.object(self.node.time, 'sleep'):
                self.node.wait_process_contract(self.identity)
            self.assertEqual(verify.call_count, 2)
        with patch.object(self.node, 'process_contract', side_effect=self.node.NodeTLSRefusal('foreign process')):
            with patch.object(self.node.time, 'monotonic', side_effect=[0, 11]):
                with self.assertRaises(self.node.NodeTLSRefusal):
                    self.node.wait_process_contract(self.identity)
