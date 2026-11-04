"""Strict source, readiness, and root-write boundaries for core convergence."""

import copy
import json
import unittest
from unittest.mock import patch

from core_acceptance import application_diagnostics, converged, core_source, root_source_patch, sync_to_source


class CoreAcceptanceTests(unittest.TestCase):
    def setUp(self):
        self.source = core_source('https://github.com/dipeshbabu/bareplane.git', 'a' * 40)
        self.root = dict(kind='Application', metadata=dict(name='lab-root', namespace='argocd', uid='owned-root',
            annotations={'bareplane.io/cluster': 'lab', 'bareplane.io/handoff': 'b' * 32}),
            spec=dict(source=dict(repoURL='https://github.com/dipeshbabu/bareplane.git', targetRevision='main', path='examples/gitops/root')))
        self.ownership = dict(cluster='lab', uid='owned-root', rootName='lab-root', nonce='b' * 32, mode='following', complete=True)

    def test_every_child_is_pinned_to_the_same_exact_source(self):
        self.assertEqual(self.source['targetRevision'], 'a' * 40)
        patch = self.source['kustomize']['patches'][0]
        self.assertEqual(patch['target'], dict(group='argoproj.io', version='v1alpha1', kind='Application'))
        self.assertEqual(json.loads(patch['patch']), [
            dict(op='replace', path='/spec/source/repoURL', value='https://github.com/dipeshbabu/bareplane.git'),
            dict(op='replace', path='/spec/source/targetRevision', value='a' * 40)])
        for revision in ['main', 'a' * 39, 'g' * 40]:
            with self.assertRaises(RuntimeError):
                core_source('https://github.com/dipeshbabu/bareplane.git', revision)
        with self.assertRaises(RuntimeError):
            core_source('http://localhost/repo.git', 'a' * 40)

    def test_root_update_is_bound_to_existing_uid_source_and_ownership(self):
        operations = root_source_patch(self.root, self.source, self.ownership)
        self.assertEqual(operations[0], dict(op='test', path='/metadata/uid', value='owned-root'))
        self.assertEqual(operations[1], dict(op='test', path='/spec/source', value=self.root['spec']['source']))
        self.assertEqual(operations[2], dict(op='replace', path='/spec/source', value=self.source))
        for field, value in [('uid', ''), ('name', 'foreign'), ('namespace', 'other'), ('annotations', {})]:
            root = copy.deepcopy(self.root)
            root['metadata'][field] = value
            with self.subTest(field=field), self.assertRaises(RuntimeError):
                root_source_patch(root, self.source, self.ownership)
        for change in [dict(uid='foreign'), dict(nonce='c' * 32), dict(complete=False), dict(mode='pinned')]:
            with self.subTest(change=change), self.assertRaises(RuntimeError):
                root_source_patch(self.root, self.source, dict(self.ownership, **change))

    def test_recovery_rechecks_source_and_version_after_a_conflict(self):
        application = dict(metadata=dict(uid='owned', resourceVersion='1'), spec=dict(source=self.source))
        writes = []

        def api(*args):
            if args[0] == 'get':
                return copy.deepcopy(application)
            operations = json.loads(args[args.index('-p') + 1])
            writes.append(operations)
            if len(writes) == 1:
                application['metadata']['resourceVersion'] = '2'
                raise RuntimeError('Conflict')
            return {}

        with patch('core_acceptance.time.sleep'):
            sync_to_source(api, 'lab-observability', self.source, 'a' * 40, 'owned')
        self.assertEqual(len(writes), 2)
        self.assertEqual(writes[0][1]['value'], '1')
        self.assertEqual(writes[1][1]['value'], '2')
        self.assertEqual(writes[1][-1]['value'], dict(sync=dict(revision='a' * 40, prune=False)))
        application['metadata']['uid'] = 'foreign'
        with self.assertRaises(RuntimeError):
            sync_to_source(api, 'lab-observability', self.source, 'a' * 40, 'owned')
        self.assertEqual(len(writes), 2)

    def test_ambiguous_accepted_sync_is_acknowledged_without_repeating_write(self):
        application = dict(metadata=dict(uid='owned', resourceVersion='1'), spec=dict(source=self.source))
        writes = []

        def api(*args):
            if args[0] == 'get':
                return copy.deepcopy(application)
            writes.append(args)
            application['operation'] = dict(sync=dict(revision='a' * 40, prune=False))
            raise RuntimeError('Connection closed after write')

        with patch('core_acceptance.time.sleep'):
            sync_to_source(api, 'lab-observability', self.source, 'a' * 40, 'owned')
        self.assertEqual(len(writes), 1)

    def test_matching_automated_sync_is_observed_without_replacing_it(self):
        application = dict(metadata=dict(uid='owned', resourceVersion='1'), spec=dict(source=self.source),
                           operation=dict(initiatedBy=dict(automated=True), sync=dict(revision='a' * 40)))
        def api(*args):
            self.assertEqual(args[0], 'get')
            return copy.deepcopy(application)
        sync_to_source(api, 'lab-root', self.source, 'a' * 40, 'owned')
        application['operation']['sync']['manifests'] = ['unreviewed']
        with self.assertRaises(RuntimeError):
            sync_to_source(api, 'lab-root', self.source, 'a' * 40, 'owned')

    def test_diagnostics_report_categories_without_exposing_error_messages(self):
        app = dict(spec=dict(source=self.source), status=dict(sync=dict(status='OutOfSync', revision='b' * 40,
            comparedTo=dict(source={})), health=dict(status='Progressing'),
            conditions=[dict(type='ComparisonError', message='PRIVATE-SENTINEL invalid patch')]))
        summary = application_diagnostics('lab-root', app, self.source, 'a' * 40)
        self.assertNotIn('PRIVATE-SENTINEL', json.dumps(summary))
        self.assertEqual(summary['errorCategories'], ['patch', 'invalid'])
        self.assertFalse(summary['revisionMatches'])

    def test_success_requires_exact_source_commit_health_and_finished_operation(self):
        app = dict(status=dict(sync=dict(status='Synced', revision='a' * 40, comparedTo=dict(source=self.source)),
                               health=dict(status='Healthy'), operationState=dict(phase='Succeeded')))
        self.assertTrue(converged(app, self.source, 'a' * 40))
        variants = []
        for field, value in [('status', 'OutOfSync'), ('revision', 'b' * 40), ('comparedTo', dict(source={}))]:
            changed = copy.deepcopy(app)
            changed['status']['sync'][field] = value
            variants.append(changed)
        changed = copy.deepcopy(app)
        changed['status']['health']['status'] = 'Degraded'
        variants.append(changed)
        variants.append(dict(app, operation={'sync': {}}))
        for phase in ['Running', 'Failed', 'Error', 'Terminating']:
            changed = copy.deepcopy(app)
            changed['status']['operationState']['phase'] = phase
            variants.append(changed)
        changed = copy.deepcopy(app)
        changed['status']['conditions'] = [{'type': 'ComparisonError'}]
        variants.append(changed)
        for changed in variants:
            with self.subTest(changed=changed):
                self.assertFalse(converged(changed, self.source, 'a' * 40))


if __name__ == '__main__':
    unittest.main()
