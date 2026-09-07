"""Reset refusal boundaries without executing destructive operations locally."""
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import types
import unittest
from unittest.mock import patch

root = Path(__file__).resolve().parents[2] / 'internal/render/ansible/assets'


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


reset = load('reset', root / 'library/bareplane_reset.py')
helpers = load('join_helpers', root / 'module_utils/bareplane_join_state.py')


class ResetTests(unittest.TestCase):
    def test_direct_reset_requires_approval_before_any_inspection(self):
        with patch.object(reset, 'inspect') as inspect, self.assertRaisesRegex(ValueError, 'explicit approval'):
            reset.reset_node(dict(approved=False), helpers)
        inspect.assert_not_called()

    def test_application_and_unmanaged_pods_are_refused(self):
        pod = dict(metadata=dict(name='coredns-fixture', namespace='kube-system'), spec=dict(nodeName='cp1'))
        reset.bootstrap_pods_only([pod], ['cp1'])
        for name, namespace, node in [('application', 'default', 'cp1'), ('application', 'kube-system', 'cp1'), ('coredns-fixture', 'kube-system', 'foreign')]:
            with self.subTest(name=name, namespace=namespace, node=node), self.assertRaises(ValueError):
                reset.bootstrap_pods_only([dict(metadata=dict(name=name, namespace=namespace), spec=dict(nodeName=node))], ['cp1'])

    def test_reset_tree_refuses_symlinks_and_nested_mounts(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            (path / 'data').write_text('owned fixture')
            reset.inspect_tree(directory, helpers)
            (path / 'redirect').symlink_to(path / 'data')
            with self.assertRaisesRegex(ValueError, 'Redirected'):
                reset.inspect_tree(directory, helpers)
            (path / 'redirect').unlink()
            with patch.object(reset.os.path, 'ismount', return_value=True), self.assertRaisesRegex(ValueError, 'mounts'):
                reset.inspect_tree(directory, helpers)

    def test_api_storage_guard_refuses_persistent_volumes(self):
        fake = types.SimpleNamespace(read_file=lambda path: b'public CA', checked_path=lambda path: True, validate_admin=lambda *args: None)

        def command(args):
            if 'view' in args:
                return b'{}'
            if '--raw=/readyz' in args:
                return b'ok'
            return json.dumps(dict(items=[dict(metadata=dict(name='application-volume'))])).encode()

        with patch.object(reset, 'command', side_effect=command), self.assertRaisesRegex(ValueError, 'Persistent application storage'):
            reset.guard_api(dict(cluster='lab', vip='192.0.2.100', allow_unavailable_api=False, nodes=['cp1']), fake)

    def test_unavailable_api_only_allowed_for_explicitly_supported_initial_state(self):
        fake = types.SimpleNamespace(read_file=lambda path: None, checked_path=lambda path: False)
        with self.assertRaisesRegex(ValueError, 'API credentials'):
            reset.guard_api(dict(allow_unavailable_api=False), fake)
        reset.guard_api(dict(allow_unavailable_api=True), fake)

    def test_foreign_admin_identity_is_never_treated_as_an_unavailable_api(self):
        def foreign(*args):
            raise ValueError('foreign CA')
        fake = types.SimpleNamespace(read_file=lambda path: b'public CA', checked_path=lambda path: True, validate_admin=foreign)
        with patch.object(reset, 'command', return_value=b'{}'), self.assertRaisesRegex(ValueError, 'foreign CA'):
            reset.guard_api(dict(cluster='lab', vip='192.0.2.100', allow_unavailable_api=True), fake)


if __name__ == '__main__':
    unittest.main()
