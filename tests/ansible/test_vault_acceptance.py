import copy
import json
from types import SimpleNamespace
import unittest
from unittest.mock import Mock

from vault_acceptance import patch_application_source


class VaultApplicationUpdateTests(unittest.TestCase):
    def test_source_patch_preserves_status_and_tests_identity_and_previous_intent(self):
        original = dict(repoURL='https://github.com/example/repo.git', path='components/vault', targetRevision='a' * 40)
        desired = dict(original, kustomize={'patches': []})
        component = SimpleNamespace(name='lab-vault', api=Mock(), app={'spec': {'source': copy.deepcopy(original)}})
        patch_application_source(component, 'owned-uid', desired)
        args = component.api.call_args.args
        operations = json.loads(args[args.index('-p') + 1])
        self.assertEqual(operations, [dict(op='test', path='/metadata/uid', value='owned-uid'),
            dict(op='test', path='/spec/source', value=original), dict(op='replace', path='/spec/source', value=desired)])
        self.assertEqual(component.app['spec']['source'], desired)
        self.assertNotIn('replace', args)

    def test_failed_conditional_patch_does_not_change_the_expected_source(self):
        original = {'path': 'components/vault'}
        component = SimpleNamespace(name='lab-vault', api=Mock(side_effect=RuntimeError('refused')),
                                    app={'spec': {'source': copy.deepcopy(original)}})
        with self.assertRaises(RuntimeError):
            patch_application_source(component, 'owned-uid', {'path': 'changed'})
        self.assertEqual(component.app['spec']['source'], original)
