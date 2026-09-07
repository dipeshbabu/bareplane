"""Offline regression tests for join topology, ownership, and resume boundaries."""
import copy
import base64
import hashlib
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

source = Path(__file__).resolve().parents[2] / 'internal/render/ansible/assets/library/bareplane_join_state.py'
spec = importlib.util.spec_from_file_location('join_state', source)
join = importlib.util.module_from_spec(spec)
spec.loader.exec_module(join)


class JoinStateTests(unittest.TestCase):
    def setUp(self):
        self.ca = b'public test CA'
        self.identity = dict(cluster='lab', ca_sha256=hashlib.sha256(self.ca).hexdigest(),
                             name='lab-control-2', control_plane=True, version='1.36.4',
                             endpoint='192.0.2.100:6443', address='192.0.2.12')
        self.node = self.node_for(self.identity)

    def node_for(self, identity):
        control = identity['control_plane']
        return dict(metadata=dict(name=identity['name'], uid='uid-' + identity['name'],
                                  labels={join.CONTROL_ROLE: ''} if control else {}),
                    spec=dict(taints=[dict(key=join.CONTROL_ROLE, effect='NoSchedule')] if control else []),
                    status=dict(nodeInfo=dict(kubeletVersion='v' + identity['version']),
                                addresses=[dict(type='InternalIP', address=identity['address'])],
                                conditions=[dict(type='Ready', status='True')]))

    def intent(self, identity=None):
        return (join.digest(identity or self.identity) + '\n').encode()

    def test_single_primary_has_no_joins(self):
        desired = ['lab-control-1']
        self.assertEqual([name for name in sorted(desired) if name != desired[0]], [])

    def test_stale_primary_markers_cannot_adopt_a_different_ca(self):
        files = {'/etc/kubernetes/.bareplane-init-complete': b'lab\n', join.STATE + '/cilium-complete': b'complete', join.CA: self.ca}
        for record in [None, b'foreign\n']:
            files[join.STATE + '/init-ca-sha256'] = record
            with patch.object(join, 'read_file', side_effect=files.get), self.assertRaisesRegex(ValueError, 'Primary CA differs'):
                join.inspect_primary('lab')

    def test_primary_admin_config_must_select_the_owned_ca_and_vip(self):
        doc = dict(clusters=[dict(name='lab', cluster={'server': 'https://192.0.2.100:6443',
                   'certificate-authority-data': base64.b64encode(self.ca).decode()})],
                   users=[dict(name='admin', user={'client-certificate-data': 'private', 'client-key-data': 'private'})],
                   contexts=[dict(name='admin@lab', context=dict(cluster='lab', user='admin'))], **{'current-context': 'admin@lab'})
        join.validate_admin(doc, 'lab', self.identity['ca_sha256'], '192.0.2.100')
        for change in ['ca', 'endpoint', 'exec', 'context']:
            bad = copy.deepcopy(doc)
            if change == 'ca':
                bad['clusters'][0]['cluster']['certificate-authority-data'] = base64.b64encode(b'foreign').decode()
            elif change == 'endpoint':
                bad['clusters'][0]['cluster']['server'] = 'https://192.0.2.101:6443'
            elif change == 'exec':
                bad['users'][0]['user']['exec'] = dict(command='unmanaged')
            else:
                bad['current-context'] = 'other'
            with self.subTest(change=change), self.assertRaises(ValueError):
                join.validate_admin(bad, 'lab', self.identity['ca_sha256'], '192.0.2.100')

    def test_three_control_planes_and_mixed_workers_partial_rerun(self):
        controls = ['lab-control-1', 'lab-control-2', 'lab-control-3']
        workers = ['lab-worker-1', 'lab-worker-2']
        visited = []
        for name in sorted(controls[1:]) + sorted(workers):
            identity = dict(self.identity, name=name, control_plane=name in controls)
            node = self.node_for(identity)
            intent = self.intent(identity)
            complete = intent + (node['metadata']['uid'] + '\n').encode()
            with self.subTest(name=name):
                self.assertEqual(join.decide(identity, {}, None, None, None, False), 'fresh')
                self.assertEqual(join.decide(identity, node, intent, None, self.ca, True), 'finalize')
                self.assertEqual(join.decide(identity, node, intent, complete, self.ca, True), 'complete')
                join.verify_node(node, identity, ready=True)
            visited.append(name)
        self.assertEqual(visited, controls[1:] + workers)

    def test_expired_credential_is_not_part_of_persistent_identity(self):
        # Only configuration/CA identity is persisted; each fresh enrollment
        # creates a new TTL-bound token and certificate upload in enroll.yaml.
        self.assertNotIn('token', self.identity)
        self.assertNotIn('certificateKey', self.identity)
        self.assertEqual(join.decide(self.identity, {}, None, None, None, False), 'fresh')
        with self.assertRaisesRegex(ValueError, 'explicit recovery'):
            join.decide(self.identity, {}, self.intent(), None, None, False)

    def test_foreign_or_unmanaged_state_is_never_adopted(self):
        for node, ca, kubelet in [(self.node, None, False), ({}, self.ca, False), ({}, None, True)]:
            with self.subTest(node=node, ca=ca, kubelet=kubelet), self.assertRaisesRegex(ValueError, 'Unmanaged or foreign'):
                join.decide(self.identity, node, None, None, ca, kubelet)
        with self.assertRaisesRegex(ValueError, 'foreign'):
            join.decide(self.identity, self.node, self.intent(), None, b'foreign CA', True)

    def test_changed_configuration_and_node_uid_are_refused(self):
        complete = self.intent() + b'old-uid\n'
        with self.assertRaisesRegex(ValueError, 'node UID'):
            join.decide(self.identity, self.node, self.intent(), complete, self.ca, True)
        for field, value in [('cluster', 'other'), ('name', 'other'), ('version', '1.36.5'),
                             ('control_plane', False), ('endpoint', '192.0.2.101:6443'),
                             ('address', '192.0.2.30'), ('ca_sha256', 'different')]:
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, 'intent differs'):
                join.decide(dict(self.identity, **{field: value}), self.node, self.intent(), None, self.ca, True)

    def test_wrong_name_role_taint_version_address_and_readiness(self):
        bad_nodes = []
        for section, field, value in [('metadata', 'name', 'other'), ('metadata', 'uid', ''),
                                      ('metadata', 'labels', {}), ('spec', 'taints', []),
                                      ('status', 'nodeInfo', {'kubeletVersion': 'v1.35.8'}),
                                      ('status', 'addresses', []), ('status', 'conditions', [])]:
            node = copy.deepcopy(self.node)
            node[section][field] = value
            bad_nodes.append(node)
        for node in bad_nodes:
            with self.subTest(node=node), self.assertRaises(ValueError):
                join.verify_node(node, self.identity, ready=True)

    def test_incomplete_attempt_never_runs_join_again(self):
        for ca, kubelet, node in [(None, False, {}), (self.ca, False, {}), (self.ca, True, {})]:
            with self.subTest(ca=ca, kubelet=kubelet), self.assertRaises(ValueError):
                join.decide(self.identity, node, self.intent(), None, ca, kubelet)

    def test_local_kubelet_must_authenticate_to_matching_uid(self):
        files = {join.CA: self.ca, join.STATE + '/join-intent': self.intent(), '/etc/kubernetes/kubelet.conf': b'private'}
        with patch.object(join, 'checked_path', return_value=True), patch.object(join, 'private_path'), patch.object(join, 'read_file', side_effect=files.get), \
                patch.object(join, 'run', return_value=b'{"metadata":{"uid":"foreign"}}'), self.assertRaisesRegex(ValueError, 'different cluster'):
            join.inspect_node(self.identity, self.node)

    def test_leftover_credentials_block_completion(self):
        files = {join.CA: self.ca, join.STATE + '/join-intent': self.intent(),
                 '/etc/kubernetes/kubelet.conf': b'private', join.STATE + '/join.yaml': b'secret'}
        with patch.object(join, 'checked_path', return_value=True), patch.object(join, 'private_path'), \
                patch.object(join, 'read_file', side_effect=files.get), self.assertRaisesRegex(ValueError, 'Temporary join credentials remain'):
            join.inspect_node(self.identity, self.node)

    def test_insecure_state_permissions_are_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'state'
            path.write_bytes(b'private')
            path.chmod(0o644)
            with self.assertRaisesRegex(ValueError, 'owner-only'):
                join.private_path(str(path))
            path.chmod(0o600)
            join.private_path(str(path))

    def test_symlink_ancestors_and_file_types_are_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'real').mkdir()
            (root / 'link').symlink_to(root / 'real', target_is_directory=True)
            with self.assertRaisesRegex(ValueError, 'Redirected'):
                join.checked_path(str(root / 'link' / 'absent'))
            with self.assertRaises(ValueError):
                join.checked_path(str(root / 'real'))
            self.assertTrue(join.checked_path(str(root / 'real'), directory=True))
            self.assertFalse(join.checked_path(str(root / 'absent')))

    def test_reads_are_bounded(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'state'
            path.write_bytes(b'x' * 1048577)
            with self.assertRaisesRegex(ValueError, 'Oversized'):
                join.read_file(str(path))


if __name__ == '__main__':
    unittest.main()
