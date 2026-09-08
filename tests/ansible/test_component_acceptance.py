import copy
import os
from pathlib import Path
import unittest
from unittest.mock import Mock, patch

from component_acceptance import ComponentAcceptance


class ComponentAcceptanceTests(unittest.TestCase):
    def component(self):
        # Pure method tests: no constructor guard bypass is used to run a VM or
        # contact an API. Every external operation is replaced by a Mock.
        component = ComponentAcceptance.__new__(ComponentAcceptance)
        component.name, component.namespace, component.revision = 'lab-metrics-server', 'metrics-server', 'a' * 40
        component.app = dict(spec=dict(source=dict(repoURL='https://github.com/example/repo.git', path='components/metrics-server', targetRevision='a' * 40)))
        component.api, component.command = Mock(), Mock()
        return component

    def healthy(self, component):
        return dict(metadata=dict(annotations={}), spec=component.app['spec'], status=dict(
            sync=dict(status='Synced', revision=component.revision, comparedTo=dict(source=component.app['spec']['source'])),
            health=dict(status='Healthy'), reconciledAt='new', operationState=dict(phase='Succeeded')))

    def test_constructor_refuses_non_disposable_context_before_external_calls(self):
        with patch.dict(os.environ, {'GITHUB_ACTIONS': 'false'}), patch('component_acceptance.subprocess.run') as run:
            with self.assertRaises(RuntimeError):
                ComponentAcceptance([], Path('/unused'), 'metrics-server')
            run.assert_not_called()

    def test_healthy_current_source_is_required_not_cached_or_failed_status(self):
        component = self.component()
        current = self.healthy(component)
        component.api.return_value = current
        self.assertEqual(component.wait_application(), current)
        for mutation in [lambda r: r['status']['sync'].update(revision='b' * 40),
                         lambda r: r['status']['sync']['comparedTo'].update(source={}),
                         lambda r: r['status']['operationState'].update(phase='Failed'),
                         lambda r: r.update(operation={'sync': {}}),
                         lambda r: r['metadata']['annotations'].update({'argocd.argoproj.io/refresh': 'hard'})]:
            changed = copy.deepcopy(current)
            mutation(changed)
            component.api.return_value = changed
            with patch('component_acceptance.time.monotonic', side_effect=[0, 601]):
                with self.assertRaises(RuntimeError):
                    component.wait_application()

    def test_existing_namespace_is_not_adopted(self):
        component = self.component()
        component.api.return_value = {'metadata': {'uid': 'foreign'}}
        with self.assertRaises(RuntimeError):
            component.install()
        self.assertEqual(component.api.call_count, 1)

    def test_fresh_refresh_waits_for_a_new_reconciliation(self):
        component = self.component()
        current = self.healthy(component)
        component.api.return_value = current
        with patch('component_acceptance.time.monotonic', side_effect=[0, 601]):
            with self.assertRaises(RuntimeError):
                component.wait_application('new')
