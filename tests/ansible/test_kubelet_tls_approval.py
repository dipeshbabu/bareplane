import base64
import copy
import hashlib
import importlib.util
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

from cryptography.hazmat.primitives.asymmetric import ec
import test_kubelet_tls_policy as fixtures
import test_kubelet_tls_config as config_fixtures


ROOT = Path(__file__).resolve().parents[2] / 'internal/render/ansible/assets'


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / filename)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


modules = {'ansible': types.ModuleType('ansible'), 'ansible.module_utils': types.ModuleType('ansible.module_utils')}
for name in ['bareplane_kubeconfig', 'bareplane_kubelet_tls_policy', 'bareplane_git_repository', 'bareplane_kubelet_tls_config', 'bareplane_kubelet_tls_state']:
    modules['ansible.module_utils.' + name] = load('approval_' + name, 'module_utils/' + name + '.py')
with patch.dict(sys.modules, modules):
    approval = load('kubelet_tls_approval', 'library/bareplane_kubelet_tls.py')


class Record:
    def __init__(self, identity):
        self.data = dict(identity=copy.deepcopy(identity), state={})
        self.original = None
        self.saves = []

    def save(self, state):
        self.data['state'] = copy.deepcopy(state)
        self.saves.append(copy.deepcopy(state))
        self.original = b'saved'


class ApprovalTests(unittest.TestCase):
    def test_opaque_plan_survives_ansible_no_log_output_redaction(self):
        from ansible.module_utils.common.parameters import remove_values

        raw = config_fixtures.VALID
        while len(raw) % 3:
            raw += b'# padding\n'
        encoded = base64.b64encode(raw).decode()
        previous = base64.b64encode(approval.enable_serving_bootstrap(raw)).decode()
        self.assertNotEqual(remove_values({'target': previous}, {encoded})['target'], previous)
        planned = approval.plan_configuration(encoded)
        self.assertEqual(remove_values(planned, {encoded}), planned)
        self.assertEqual(bytes.fromhex(planned['target_hex']), approval.enable_serving_bootstrap(raw))

    def setUp(self):
        self.fixture = fixtures.ServingPolicyTests()
        self.fixture.key = ec.generate_private_key(ec.SECP256R1())
        self.resource = self.fixture.request()
        self.pem, self.ca = self.fixture.certificates()
        self.identity = dict(cluster='lab', node='lab-control-1', nodeUID='node-uid', address='192.0.2.11',
                             caSHA256=hashlib.sha256(self.ca).hexdigest(), machineID='a' * 32)
        self.node = dict(metadata=dict(name='lab-control-1', uid='node-uid'), status=dict(addresses=[
            dict(type='Hostname', address='lab-control-1'), dict(type='InternalIP', address='192.0.2.11')]))
        self.record = Record(self.identity)
        self.instance = approval.Approval(None, '/private/admin.conf', self.record, self.ca)
        self.instance.api = self.api
        self.instance.live_certificate = lambda request: approval.policy.validate_certificate(self.pem, self.ca, self.identity['caSHA256'], request)
        self.writes = []
        self.ambiguous = False
        self.extra = []

    def api(self, *args, data=None):
        if args[:2] == ('get', 'node'):
            return copy.deepcopy(self.node)
        if '--field-selector=spec.signerName=' + approval.policy.SIGNER in args:
            return {'items': [copy.deepcopy(self.resource)] + copy.deepcopy(self.extra)}
        if args[:2] == ('get', 'csr'):
            return copy.deepcopy(self.resource)
        self.assertEqual(args[0], 'replace')
        self.assertEqual(args[1], '--raw=/apis/certificates.k8s.io/v1/certificatesigningrequests/' + self.resource['metadata']['name'] + '/approval')
        self.assertEqual(self.record.data['state']['stage'], 'pending', 'approval intent must precede the API update')
        self.assertEqual(data['metadata']['resourceVersion'], '123')
        self.writes.append(copy.deepcopy(data))
        self.resource = copy.deepcopy(data)
        self.resource['metadata']['resourceVersion'] = '124'
        self.resource['status']['certificate'] = base64.b64encode(self.pem).decode()
        if self.ambiguous:
            raise approval.GitOpsError('acknowledgement lost')
        return copy.deepcopy(self.resource)

    def test_exact_approval_and_verified_serving_certificate_then_read_only_rerun(self):
        self.assertTrue(self.instance.reconcile())
        self.assertEqual(len(self.writes), 1)
        self.assertEqual(self.record.data['state']['stage'], 'ready')
        saves = len(self.record.saves)
        self.assertFalse(self.instance.reconcile())
        self.assertEqual(len(self.record.saves), saves)
        self.assertEqual(len(self.writes), 1)

    def test_lost_acknowledgement_is_recovered_by_exact_identity_without_second_approval(self):
        self.ambiguous = True
        self.assertTrue(self.instance.reconcile())
        self.assertEqual(len(self.writes), 1)
        self.assertEqual(self.record.data['state']['stage'], 'ready')

    def test_foreign_nodes_wrong_sans_and_multiple_pending_requests_are_not_approved(self):
        for mutation in [lambda: self.node['metadata'].update(uid='foreign'),
                         lambda: self.resource['spec'].update(usages=['client auth']),
                         lambda: self.extra.append(copy.deepcopy(self.resource))]:
            self.setUp()
            mutation()
            with self.assertRaises(ValueError):
                self.instance.reconcile()
            self.assertEqual(self.writes, [])

    def test_renewal_revalidates_a_new_key_and_preserves_read_only_steady_state(self):
        self.instance.reconcile()
        previous = self.record.data['state']['request']['publicKeySHA256']
        self.fixture.key = ec.generate_private_key(ec.SECP256R1())
        self.resource = self.fixture.request()
        self.resource['metadata'].update(name='csr-renewal', uid='csr-renewal-uid')
        self.pem, ca = self.fixture.certificates()
        self.assertEqual(ca, self.ca)
        self.assertTrue(self.instance.reconcile())
        self.assertNotEqual(self.record.data['state']['request']['publicKeySHA256'], previous)
        self.assertEqual(len(self.writes), 2)

    def test_changed_approval_condition_is_not_treated_as_our_ambiguous_write(self):
        real_api = self.api

        def changed(*args, **kwargs):
            value = real_api(*args, **kwargs)
            if args[0] == 'replace':
                self.resource['status']['conditions'][0]['reason'] = 'ForeignApproval'
            return value

        self.instance.api = changed
        with self.assertRaises(ValueError):
            self.instance.reconcile()
        self.assertNotEqual(self.record.data['state']['stage'], 'ready')
