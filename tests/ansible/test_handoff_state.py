"""Pure root-plan and health tests; no Kubernetes or repository mutation."""

import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

import yaml


ROOT = Path(__file__).resolve().parents[2]


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, ROOT / path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


git = load('handoff_git', 'internal/render/ansible/assets/module_utils/bareplane_git_repository.py')
with patch.dict(sys.modules, {'ansible': types.ModuleType('ansible'), 'ansible.module_utils': types.ModuleType('ansible.module_utils'),
                             'ansible.module_utils.bareplane_git_repository': git}):
    argo = load('handoff_argo', 'internal/render/ansible/assets/module_utils/bareplane_argocd_state.py')
    sys.modules['ansible.module_utils.bareplane_argocd_state'] = argo
    handoff = load('handoff_state', 'internal/render/ansible/assets/module_utils/bareplane_handoff_state.py')


class NewComponentOwnershipTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.resources = list(yaml.safe_load_all((ROOT / 'components/cert-manager/upstream.yaml').read_bytes()))
        cls.resources += list(yaml.safe_load_all((ROOT / 'components/cert-manager/issuers.yaml').read_bytes()))

    def setUp(self):
        self.observed = []
        self.existing = None

    def json(self, *args):
        self.observed.append(args)
        return {'metadata': {'uid': 'foreign'}} if args[1:3] == self.existing else {}

    def test_cold_component_checks_cluster_objects_without_writing(self):
        handoff.verify_new_component_absence(self, self.resources)
        self.assertTrue(self.observed)
        self.assertTrue(all(args[0] == 'get' for args in self.observed))
        self.assertIn(('get', 'Namespace', 'cert-manager', '--ignore-not-found', '-o', 'json'), self.observed)
        self.assertEqual(sum(args[1] == 'CustomResourceDefinition' for args in self.observed), 6)

    def test_sops_admission_policy_and_binding_must_both_be_absent(self):
        resources = list(yaml.safe_load_all((ROOT / 'components/secrets-sops/ownership.yaml').read_bytes()))
        handoff.verify_new_component_absence(self, resources)
        self.assertEqual({args[1] for args in self.observed}, {'ValidatingAdmissionPolicy', 'ValidatingAdmissionPolicyBinding'})
        for resource in resources:
            self.existing = (resource['kind'], resource['metadata']['name'])
            with self.subTest(kind=resource['kind']), self.assertRaises(git.GitOpsError):
                handoff.verify_new_component_absence(self, resources)

    def test_existing_namespace_crd_rbac_or_webhook_is_never_adopted(self):
        for resource in self.resources:
            if resource['kind'] not in {'Namespace', 'CustomResourceDefinition', 'ClusterRole', 'ClusterRoleBinding',
                                         'MutatingWebhookConfiguration', 'ValidatingWebhookConfiguration'}:
                continue
            self.existing = (resource['kind'], resource['metadata']['name'])
            with self.subTest(resource=self.existing), self.assertRaises(git.GitOpsError):
                handoff.verify_new_component_absence(self, self.resources)

    def test_unrelated_namespace_and_undeclared_custom_resources_are_refused(self):
        invalid = [
            dict(apiVersion='v1', kind='ConfigMap', metadata=dict(name='foreign', namespace='kube-system')),
            dict(apiVersion='unknown.io/v1', kind='Unknown', metadata=dict(name='foreign')),
            dict(apiVersion='cert-manager.io/v1', kind='Certificate', metadata=dict(name='foreign')),
        ]
        for resource in invalid:
            with self.subTest(resource=resource), self.assertRaises(git.GitOpsError):
                handoff.verify_new_component_absence(self, self.resources + [resource])

    def test_metrics_authentication_reader_is_a_new_exact_namespace_scoped_binding(self):
        resources = list(yaml.safe_load_all((ROOT / 'internal/render/gitops/assets/metrics-server/upstream.yaml').read_bytes()))
        handoff.verify_new_component_absence(self, resources)
        query = ('get', 'rolebindings.rbac.authorization.k8s.io', 'metrics-server-auth-reader', '-n', 'kube-system', '--ignore-not-found', '-o', 'json')
        self.assertIn(query, self.observed)
        self.existing = ('rolebindings.rbac.authorization.k8s.io', 'metrics-server-auth-reader')
        with self.assertRaises(git.GitOpsError):
            handoff.verify_new_component_absence(self, resources)
        self.existing = None
        for mutation in [lambda r: r['roleRef'].update(name='cluster-admin'),
                         lambda r: r['roleRef'].update(kind='ClusterRole'),
                         lambda r: r['subjects'][0].update(namespace='argocd'),
                         lambda r: r['metadata'].update(name='other'),
                         lambda r: r.update(apiVersion='foreign.io/v1')]:
            changed = copy.deepcopy(resources)
            binding = next(obj for obj in changed if obj['kind'] == 'RoleBinding')
            mutation(binding)
            with self.assertRaises(git.GitOpsError):
                handoff.verify_new_component_absence(self, changed)

    def test_dns_may_create_only_new_service_reader_permissions_in_an_existing_app_namespace(self):
        raw = (ROOT / 'internal/render/gitops/assets/external-dns/upstream.yaml').read_bytes()
        resources = list(yaml.safe_load_all(raw.replace(b'BAREPLANE_DNS_SOURCE_NAMESPACE', b'apps')))
        namespace_exists = True
        blocked = None
        queries = []

        class Client:
            def json(self, *args):
                queries.append(args)
                if args[:3] == ('get', 'namespace', 'apps'):
                    return {'metadata': {'uid': 'application-namespace'}} if namespace_exists else {}
                return {'metadata': {'uid': 'foreign'}} if args[1:3] == blocked else {}

        handoff.verify_new_component_absence(Client(), resources)
        self.assertEqual(sum(args[:3] == ('get', 'namespace', 'apps') for args in queries), 1)
        self.assertTrue(all(args[0] == 'get' for args in queries))
        for kind in ['roles', 'rolebindings']:
            blocked = (kind + '.rbac.authorization.k8s.io', 'bareplane-external-dns-reader')
            with self.assertRaises(git.GitOpsError):
                handoff.verify_new_component_absence(Client(), resources)
        blocked = None
        namespace_exists = False
        with self.assertRaises(git.GitOpsError):
            handoff.verify_new_component_absence(Client(), resources)
        namespace_exists = True
        for mutate in [lambda r: r['rules'][0]['resources'].append('secrets'),
                       lambda r: r['rules'][0]['verbs'].append('create'),
                       lambda r: r['metadata'].update(name='other')]:
            changed = copy.deepcopy(resources)
            mutate(next(obj for obj in changed if obj['kind'] == 'Role'))
            with self.assertRaises(git.GitOpsError):
                handoff.verify_new_component_absence(Client(), changed)


class HandoffPlanTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.files = {'bootstrap/lab-root-application.yaml': (ROOT / 'bootstrap/lab-root-application.yaml').read_bytes()}
        for directory in ['components/argocd', 'examples/gitops/root']:
            cls.files.update({p.relative_to(ROOT).as_posix(): p.read_bytes() for p in (ROOT / directory).rglob('*') if p.is_file()})

    def plan(self, files=None):
        return handoff.Plan(files or self.files, 'lab', 'https://github.com/dipeshbabu/bareplane.git', 'main', 'examples/gitops/root')

    def test_initial_root_and_every_child_are_pinned_to_one_verified_commit(self):
        plan = self.plan()
        pinned = plan.pinned_root('a' * 40, 'b' * 32)
        source = pinned['spec']['source']
        self.assertEqual(source['targetRevision'], 'a' * 40)
        self.assertEqual(pinned['metadata']['annotations'][handoff.HANDOFF], 'b' * 32)
        patches = source['kustomize']['patches']
        self.assertEqual(len(patches), len(plan.children))
        for item in patches:
            self.assertIn(item['target']['name'], plan.children)
            self.assertEqual(item['target']['kind'], 'Application')
            self.assertEqual(item['target']['namespace'], 'argocd')
            self.assertEqual(json.loads(item['patch']), [{'op': 'replace', 'path': '/spec/source/targetRevision', 'value': 'a' * 40}])
        following = plan.following_root('b' * 32)
        self.assertEqual(following['spec']['source']['targetRevision'], 'main')
        self.assertNotIn('kustomize', following['spec']['source'])
        self.assertNotIn(handoff.HANDOFF, plan.root['metadata']['annotations'])
        self.assertEqual(plan.children['lab-argocd']['spec']['source']['targetRevision'], 'main')

    def test_root_hash_ignores_only_controller_bookkeeping(self):
        root = self.plan().pinned_root('a' * 40, 'b' * 32)
        expected = handoff.root_hash(root)
        root['status'] = {'health': {'status': 'Progressing'}}
        root['operation'] = {'sync': {'revision': 'a' * 40}}
        root['metadata'].update(uid='fixture-uid', resourceVersion='2', generation=2)
        root['metadata']['annotations']['argocd.argoproj.io/refresh'] = 'normal'
        self.assertEqual(handoff.root_hash(root), expected)
        root['spec']['source']['targetRevision'] = 'other'
        self.assertNotEqual(handoff.root_hash(root), expected)

    def test_foreign_owners_finalizers_tracking_and_termination_are_refused(self):
        for field, value in [('ownerReferences', [{'uid': 'foreign'}]), ('finalizers', ['resources-finalizer.argocd.argoproj.io']), ('deletionTimestamp', 'fixture')]:
            root = self.plan().following_root('b' * 32)
            root['metadata'][field] = value
            with self.subTest(field=field), self.assertRaises(git.GitOpsError):
                handoff.root_hash(root)
        root = self.plan().following_root('b' * 32)
        root['metadata']['annotations']['argocd.argoproj.io/tracking-id'] = 'foreign'
        with self.assertRaises(git.GitOpsError):
            handoff.root_hash(root)

    def test_unrelated_child_repository_or_recursive_root_is_refused(self):
        for change in ['repository', 'name', 'kind', 'prune']:
            files = dict(self.files)
            name = 'examples/gitops/root/applications/argocd.yaml'
            child = yaml.safe_load(files[name])
            if change == 'repository':
                child['spec']['source']['repoURL'] = 'https://example.com/other.git'
            elif change == 'name':
                child['metadata']['name'] = 'lab-root'
            elif change == 'kind':
                child['kind'] = 'ApplicationSet'
            else:
                child['spec']['syncPolicy']['automated']['prune'] = True
            files[name] = yaml.safe_dump(child).encode()
            with self.subTest(change=change), self.assertRaises(git.GitOpsError):
                self.plan(files)

    def test_health_requires_current_source_commit_sync_and_readiness(self):
        app = self.plan().pinned_root('a' * 40, 'b' * 32)
        app['status'] = dict(sync=dict(status='Synced', revision='a' * 40, comparedTo=dict(source=copy.deepcopy(app['spec']['source']))),
                             health=dict(status='Healthy'), operationState=dict(phase='Succeeded'))
        self.assertTrue(handoff.application_health(app, 'a' * 40))
        self.assertFalse(handoff.application_health(app, 'c' * 40))
        for phase in ['Running', 'Terminating', 'Failed', 'Error']:
            changed = copy.deepcopy(app)
            changed['status']['operationState']['phase'] = phase
            self.assertFalse(handoff.application_health(changed, 'a' * 40))
        changed = copy.deepcopy(app)
        changed['spec']['source']['targetRevision'] = 'new'
        self.assertFalse(handoff.application_health(changed, 'a' * 40))
        changed = copy.deepcopy(app)
        changed['status']['conditions'] = [{'type': 'ComparisonError', 'message': 'DO-NOT-PRINT-PRIVATE-DIAGNOSTIC'}]
        self.assertFalse(handoff.application_health(changed))
        self.assertNotIn('DO-NOT-PRINT-PRIVATE-DIAGNOSTIC', handoff.health_summary(changed))
        changed['status']['health']['status'] = 'PRIVATE-SENTINEL'
        self.assertNotIn('PRIVATE-SENTINEL', handoff.health_summary(changed))

    @unittest.skipUnless(os.environ.get('BAREPLANE_TEST_KUBECTL'), 'opt-in pinned Kustomize integration')
    def test_real_kustomize_applies_root_source_patches_to_every_child(self):
        plan = self.plan()
        pinned = plan.pinned_root('a' * 40, 'b' * 32)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for name, data in self.files.items():
                target = root / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(data)
            path = root / 'examples/gitops/root/kustomization.yaml'
            document = yaml.safe_load(path.read_bytes())
            document['patches'] = pinned['spec']['source']['kustomize']['patches']
            path.write_text(yaml.safe_dump(document))
            output = subprocess.run([os.environ['BAREPLANE_TEST_KUBECTL'], 'kustomize', str(path.parent)],
                                    capture_output=True, check=True, timeout=60,
                                    env=dict(os.environ, KUBECTL_KUBERC='false')).stdout
            apps = list(yaml.safe_load_all(output))
            self.assertEqual({app['metadata']['name'] for app in apps}, set(plan.children))
            self.assertTrue(all(app['spec']['source']['targetRevision'] == 'a' * 40 for app in apps))

    def test_root_client_cannot_write_platform_resources_or_unversioned_updates(self):
        class Commands:
            def run(self, *args, **kwargs):
                raise AssertionError('unsafe write reached kubectl')

        client = handoff.RootClient(Commands(), '/private/admin.conf', 'lab-root', 'lab')
        with self.assertRaises(git.GitOpsError):
            client.create(dict(apiVersion='v1', kind='Namespace', metadata=dict(name='argocd')))
        with self.assertRaises(git.GitOpsError):
            client.replace(self.plan().following_root('b' * 32))
        with self.assertRaises(git.GitOpsError):
            client.write('delete', self.plan().following_root('b' * 32), False)

    def test_repository_probe_uses_owned_pod_and_empty_environment(self):
        class Commands:
            def run(self, argv, **kwargs):
                self.argv = argv
                return b'a' * 40 + b'\trefs/heads/main\n'

        class Client:
            kubeconfig = '/private/admin.conf'

            def __init__(self):
                self.commands = Commands()
                self.parent_uid = 'deployment-uid'

            def json(self, *args):
                if 'deployment' in args:
                    return {'metadata': {'uid': 'deployment-uid'}}
                if 'replicaset' in args:
                    return {'metadata': {'uid': 'replica-uid', 'ownerReferences': [{'controller': True, 'kind': 'Deployment', 'uid': self.parent_uid}]}}
                return {'items': [{'metadata': {'name': 'argocd-repo-server-fixture', 'ownerReferences': [{'controller': True, 'kind': 'ReplicaSet', 'name': 'replica', 'uid': 'replica-uid'}]},
                                   'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]},
                                   'spec': {'containers': [{'name': 'argocd-repo-server', 'image': 'quay.io/argoproj/argocd:v3.5.2'}]}}]}

        client = Client()
        receipt = types.SimpleNamespace(data={'resources': {'Deployment/argocd-repo-server': {'uid': 'deployment-uid'}}})
        handoff.repository_probe(client, self.plan().repository, receipt)
        self.assertIn('/usr/bin/env', client.commands.argv)
        self.assertIn('-i', client.commands.argv)
        self.assertIn('http.sslVerify=true', client.commands.argv)
        self.assertIn('GIT_CONFIG_GLOBAL=/dev/null', client.commands.argv)
        client.parent_uid = 'foreign'
        with self.assertRaises(git.GitOpsError):
            handoff.repository_probe(client, self.plan().repository, receipt)


class FakeRootClient:
    def __init__(self):
        self.object = None
        self.calls = []
        self.revision = 0
        self.ambiguous_create = False
        self.interrupt_create = False
        self.conflict = False

    def get(self, obj):
        self.calls.append('get')
        return copy.deepcopy(self.object or {})

    def defaulted(self, obj):
        value = copy.deepcopy(obj)
        value['spec'].setdefault('revisionHistoryLimit', 10)
        value['metadata'].update(uid='fixture-root-uid', resourceVersion=str(self.revision + 1), generation=self.revision + 1)
        return value

    def create(self, obj, dry_run=False):
        self.calls.append('create-dry' if dry_run else 'create')
        if self.object:
            raise git.GitOpsError('already exists')
        if obj['metadata'].get('uid'):
            raise AssertionError('dry-run UID must not be sent to create')
        result = self.defaulted(obj)
        if not dry_run:
            self.object = copy.deepcopy(result)
            self.revision += 1
            if self.interrupt_create:
                raise handoff.HandoffInterrupted('interrupted')
            if self.ambiguous_create:
                raise git.GitOpsError('ambiguous timeout')
        return result

    def replace(self, obj, dry_run=False):
        self.calls.append('replace-dry' if dry_run else 'replace')
        if obj['metadata']['uid'] != self.object['metadata']['uid']:
            raise AssertionError('missing UID precondition')
        if self.conflict and not dry_run:
            self.conflict = False
            self.revision += 1
            self.object['metadata']['resourceVersion'] = str(self.revision)
            raise git.GitOpsError('concurrent status update')
        if obj['metadata']['resourceVersion'] != self.object['metadata']['resourceVersion']:
            raise git.GitOpsError('resourceVersion conflict')
        result = self.defaulted(obj)
        if not dry_run:
            self.object = copy.deepcopy(result)
            self.revision += 1
        return result


@unittest.skipUnless(sys.platform == 'linux', 'private handoff receipts require Unix permissions')
class RootTransactionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        HandoffPlanTests.setUpClass()
        cls.files = HandoffPlanTests.files

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = temporary.name
        self.plan = handoff.Plan(self.files, 'lab', 'https://github.com/dipeshbabu/bareplane.git', 'main', 'examples/gitops/root')
        self.client = FakeRootClient()
        self.record = self.read_record()
        self.writer = handoff.RootWriter(self.client, self.record, self.plan)

    def read_record(self, **kwargs):
        return handoff.RootRecord(self.directory, 'lab', kwargs.get('ca', 'a' * 64), 'b' * 32, 'c' * 64, 'lab-root', 'd' * 40)

    def test_pinned_then_verified_following_and_complete(self):
        self.writer.write('pinned', 'd' * 40)
        self.assertEqual(self.client.object['spec']['revisionHistoryLimit'], 10)
        self.assertFalse(self.record.data['pending'])
        with self.assertRaises(git.GitOpsError):
            self.writer.write('following', 'd' * 40)
        self.record.verified()
        with self.assertRaises(git.GitOpsError):
            self.writer.write('following', 'e' * 40)
        self.writer.write('following', 'd' * 40)
        self.assertEqual(self.client.object['spec']['source']['targetRevision'], 'main')
        self.record.complete()
        self.assertTrue(self.read_record().data['complete'])
        with self.assertRaisesRegex(git.GitOpsError, 'read-only'):
            self.writer.write('following', 'd' * 40)

    def test_ambiguous_create_is_acknowledged_without_duplicate_write(self):
        self.client.ambiguous_create = True
        self.writer.write('pinned', 'd' * 40)
        self.assertEqual(self.client.calls.count('create'), 1)
        self.assertFalse(self.read_record().data['pending'])
        self.writer.write('pinned', 'd' * 40)
        self.assertEqual(self.client.calls.count('create'), 1)

    def test_cancellation_stops_commands_and_later_retry_recovers_intent(self):
        self.client.interrupt_create = True
        with self.assertRaises(handoff.HandoffInterrupted):
            self.writer.write('pinned', 'd' * 40)
        self.assertEqual(self.client.calls, ['get', 'create-dry', 'create'])
        record = self.read_record()
        self.assertTrue(record.data['pending'])
        self.client.interrupt_create = False
        handoff.RootWriter(self.client, record, self.plan).write('pinned', 'd' * 40)
        self.assertEqual(self.client.calls.count('create'), 1)

    def test_update_retries_only_after_fresh_uid_and_version_verification(self):
        self.writer.write('pinned', 'd' * 40)
        self.record.verified()
        self.client.conflict = True
        self.writer.write('following', 'd' * 40)
        self.assertEqual(self.client.calls.count('replace'), 2)
        self.assertFalse(self.read_record().data['pending'])

    def test_new_reviewed_commit_replaces_pin_and_requires_a_new_gate(self):
        self.writer.write('pinned', 'd' * 40)
        self.record.verified()
        self.writer.write('pinned', 'e' * 40)
        self.assertFalse(self.record.data['verified'])
        self.assertEqual(self.client.object['spec']['source']['targetRevision'], 'e' * 40)
        with self.assertRaises(git.GitOpsError):
            self.writer.write('following', 'e' * 40)

    def test_unrelated_root_missing_root_and_drift_are_never_replaced(self):
        self.client.object = self.client.defaulted(self.plan.following_root('f' * 32))
        with self.assertRaisesRegex(git.GitOpsError, 'unrelated'):
            self.writer.write('pinned', 'd' * 40)
        self.assertFalse(Path(self.directory, 'handoff.json').exists())
        self.client.object = None
        self.writer.write('pinned', 'd' * 40)
        original = copy.deepcopy(self.client.object)
        for change in ['uid', 'spec', 'missing']:
            self.client.object = copy.deepcopy(original)
            if change == 'uid':
                self.client.object['metadata']['uid'] = 'foreign'
            elif change == 'spec':
                self.client.object['spec']['project'] = 'foreign'
            else:
                self.client.object = None
            writes = self.client.calls.count('create') + self.client.calls.count('replace')
            with self.subTest(change=change), self.assertRaises(git.GitOpsError):
                self.writer.write('pinned', 'd' * 40)
            self.assertEqual(writes, self.client.calls.count('create') + self.client.calls.count('replace'))
        with self.assertRaisesRegex(git.GitOpsError, 'different cluster'):
            self.read_record(ca='f' * 64)


if __name__ == '__main__':
    unittest.main()
