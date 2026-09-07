"""Argo ownership/resume tests with a fake Kubernetes API, never a live cluster."""

import copy
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

import yaml


ROOT = Path(__file__).resolve().parents[2]


def load(name, relative):
    spec = importlib.util.spec_from_file_location(name, ROOT / relative)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


git = load('argo_git', 'internal/render/ansible/assets/module_utils/bareplane_git_repository.py')
with patch.dict(sys.modules, {'ansible': types.ModuleType('ansible'), 'ansible.module_utils': types.ModuleType('ansible.module_utils'),
                             'ansible.module_utils.bareplane_git_repository': git}):
    argo = load('argo_state', 'internal/render/ansible/assets/module_utils/bareplane_argocd_state.py')


class FakeClient:
    def __init__(self):
        self.objects = {}
        self.creates = []
        self.fail_after_create = None
        self.healthy = True
        self.handed_off = False
        self.cilium_tracked = False

    def get(self, obj):
        return copy.deepcopy(self.objects.get(argo.identity(obj), {}))

    def create(self, obj, dry_run=False):
        result = copy.deepcopy(obj)
        key = argo.identity(result)
        metadata = result['metadata']
        metadata.update(uid='uid-' + str(len(self.creates)), generation=1, resourceVersion='1', creationTimestamp='fixture')
        if result['kind'] in {'Deployment', 'StatefulSet'}:
            result['spec'].setdefault('replicas', 1)
            result['status'] = dict(observedGeneration=1, replicas=1, readyReplicas=1, updatedReplicas=1,
                                    availableReplicas=1, currentReplicas=1, currentRevision='same', updateRevision='same') if self.healthy else {}
        if result['kind'] == 'CustomResourceDefinition':
            result['status'] = dict(conditions=[dict(type='Established', status='True')])
        if result['kind'] == 'Service':
            # Dry-run allocation need not be the address assigned at creation.
            address = '10.96.0.10' if dry_run else '10.96.0.11'
            result['spec'].update(type='ClusterIP', clusterIP=address, clusterIPs=[address])
        if not dry_run:
            if key in self.objects:
                raise git.GitOpsError('already exists')
            if result['kind'] == 'CustomResourceDefinition':
                metadata['finalizers'] = ['customresourcecleanup.apiextensions.k8s.io']
            if result['kind'] == 'Deployment':
                metadata['annotations']['deployment.kubernetes.io/revision'] = '1'
            if result['kind'] == 'Secret':
                result['data'] = {'server.secretkey': 'DO-NOT-RECORD-RUNTIME-SECRET'}
            self.creates.append(key)
            self.objects[key] = copy.deepcopy(result)
            if key == self.fail_after_create:
                raise git.GitOpsError('ambiguous create timeout')
        return result

    def json(self, *args, **kwargs):
        if 'cilium' in args:
            return {'metadata': {'annotations': {'argocd.argoproj.io/tracking-id': 'foreign'} if self.cilium_tracked else {}}}
        return {'items': [{}] if self.handed_off else []}


@unittest.skipUnless(sys.platform == 'linux', 'Argo controller state requires Unix permissions')
class ArgoStateTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        assets = ROOT / 'internal/render/gitops/assets/argocd'
        cls.resources = list(yaml.safe_load_all((assets / 'upstream.yaml').read_text().replace('BAREPLANE_CLUSTER_NAME', 'lab')))
        cls.resources += [yaml.safe_load((assets / 'namespace.yaml').read_text().replace('BAREPLANE_CLUSTER_NAME', 'lab'))]

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.client = FakeClient()

    def receipt(self, **kwargs):
        return argo.Receipt(self.directory, kwargs.get('cluster', 'lab'), kwargs.get('ca', 'a' * 64),
                            kwargs.get('contract', 'b' * 64), 'c' * 40)

    def install(self):
        return argo.Installer(self.client, self.receipt(), self.resources, 'lab').install('c' * 40)

    def test_first_install_and_read_only_idempotent_rerun(self):
        self.assertTrue(self.install())
        self.assertEqual(set(self.client.creates), argo.EXPECTED)
        self.assertEqual(len(self.client.creates), len(argo.EXPECTED))
        receipt = self.receipt()
        self.assertEqual(receipt.data['stage'], 'ready')
        self.assertEqual(receipt.path.stat().st_mode & 0o777, 0o600)
        self.assertNotIn('DO-NOT-RECORD-RUNTIME-SECRET', receipt.path.read_text())
        original = receipt.path.read_bytes()
        self.assertFalse(self.install())
        self.assertEqual(len(self.client.creates), len(argo.EXPECTED))
        self.assertEqual(original, receipt.path.read_bytes())

    def test_preexisting_unmanaged_resource_blocks_every_creation(self):
        for candidate in ['Namespace/argocd', 'ClusterRole/argocd-server', 'CustomResourceDefinition/applications.argoproj.io']:
            obj = next(obj for obj in self.resources if argo.identity(obj) == candidate)
            self.client.objects = {candidate: copy.deepcopy(obj)}
            with self.subTest(candidate=candidate), self.assertRaisesRegex(git.GitOpsError, 'unmanaged'):
                self.install()
            self.assertEqual(self.client.creates, [])
            self.assertFalse((self.directory / 'argocd-ownership.json').exists())

    def test_ambiguous_create_resumes_only_the_nonce_and_hash_matched_resource(self):
        self.client.fail_after_create = 'ConfigMap/argocd-cm'
        with self.assertRaisesRegex(git.GitOpsError, 'ambiguous'):
            self.install()
        record = self.receipt().data
        self.assertEqual(record['pending'], 'ConfigMap/argocd-cm')
        self.assertEqual(record['resources'][record['pending']]['uid'], '')
        self.client.fail_after_create = None
        self.assertTrue(self.install())
        self.assertEqual(len(self.client.creates), len(argo.EXPECTED))

    def test_foreign_ca_contract_or_cluster_cannot_reuse_ownership(self):
        self.install()
        for kwargs in [dict(cluster='other'), dict(ca='d' * 64), dict(contract='e' * 64)]:
            with self.subTest(kwargs=kwargs), self.assertRaisesRegex(git.GitOpsError, 'another cluster'):
                self.receipt(**kwargs)

    def test_uid_content_secret_keys_and_nonce_drift_are_refused_without_writes(self):
        self.install()
        baseline = copy.deepcopy(self.client.objects)
        for change in ['uid', 'spec', 'nonce', 'missing', 'secret-key', 'finalizer', 'owner', 'terminating']:
            self.client.objects = copy.deepcopy(baseline)
            obj = self.client.objects['Deployment/argocd-server']
            if change == 'uid':
                obj['metadata']['uid'] = 'replaced'
            elif change == 'spec':
                obj['spec']['template']['spec']['containers'][0]['image'] = 'unreviewed:latest'
            elif change == 'nonce':
                obj['metadata']['annotations'][argo.INSTALLATION] = '0' * 32
            elif change == 'missing':
                del self.client.objects['Deployment/argocd-server']
            elif change == 'secret-key':
                self.client.objects['Secret/argocd-secret']['data']['foreign-password'] = 'PRIVATE-SENTINEL'
            elif change == 'finalizer':
                obj['metadata']['finalizers'] = ['foreign/finalizer']
            elif change == 'owner':
                obj['metadata']['ownerReferences'] = [{'uid': 'foreign'}]
            else:
                obj['metadata']['deletionTimestamp'] = 'fixture'
            with self.subTest(change=change), self.assertRaises(git.GitOpsError) as error:
                self.install()
            self.assertNotIn('PRIVATE-SENTINEL', str(error.exception))
            self.assertEqual(len(self.client.creates), len(argo.EXPECTED))

    def test_not_ready_never_publishes_ready_and_retry_does_not_recreate(self):
        self.client.healthy = False
        with patch.object(argo.time, 'monotonic', side_effect=[0, 601]), self.assertRaisesRegex(git.GitOpsError, 'timed out'):
            self.install()
        self.assertEqual(self.receipt().data['stage'], 'installing')
        for obj in self.client.objects.values():
            if obj['kind'] in {'Deployment', 'StatefulSet'}:
                obj['status'] = dict(observedGeneration=1, replicas=1, readyReplicas=1, updatedReplicas=1,
                                     availableReplicas=1, currentReplicas=1, currentRevision='same', updateRevision='same')
        self.assertFalse(self.install())
        self.assertEqual(len(self.client.creates), len(argo.EXPECTED))

    def test_cilium_tracking_and_existing_handoff_block_installation_success(self):
        self.install()
        for option in ['handed_off', 'cilium_tracked']:
            setattr(self.client, option, True)
            with self.subTest(option=option), self.assertRaises(git.GitOpsError):
                self.install()
            setattr(self.client, option, False)
        self.assertEqual(len(self.client.creates), len(argo.EXPECTED))

    def test_redirected_or_noncanonical_receipt_is_preserved(self):
        self.install()
        receipt = self.receipt()
        original = receipt.path.read_bytes()
        receipt.path.write_bytes(original + b' ')
        with self.assertRaises(git.GitOpsError):
            self.receipt()
        self.assertEqual(receipt.path.read_bytes(), original + b' ')
        receipt.path.unlink()
        target = self.directory / 'operator-file'
        target.write_bytes(b'private operator content')
        receipt.path.symlink_to(target)
        with self.assertRaises(git.GitOpsError):
            self.receipt()
        self.assertEqual(target.read_bytes(), b'private operator content')

    def test_payload_cannot_introduce_optional_or_bootstrap_resources(self):
        resources = copy.deepcopy(self.resources)
        resources[0]['metadata']['name'] = 'cilium.io'
        with self.assertRaisesRegex(git.GitOpsError, 'reviewed minimal'):
            argo.Installer(self.client, self.receipt(), resources, 'lab')
        self.assertEqual(self.client.creates, [])


if __name__ == '__main__':
    unittest.main()
