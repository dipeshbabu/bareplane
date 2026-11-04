"""Rerun acceptance must allow branch progress while preserving ownership."""

import copy
import unittest

from argo_receipt import verify_installation_rerun


class ArgoReceiptTests(unittest.TestCase):
    def setUp(self):
        self.original = dict(version=1, cluster='lab', caSHA256='a' * 64,
                             contractSHA256='b' * 64, installation='c' * 32,
                             stage='ready', pending=None, repositoryCommit='d' * 40,
                             resources={'Deployment/argocd-server': dict(uid='same-uid', sha256='e' * 64)})

    def test_unchanged_receipt_and_advancing_branch_preserve_ownership(self):
        verify_installation_rerun(self.original, copy.deepcopy(self.original))
        current = dict(self.original, repositoryCommit='f' * 40)
        verify_installation_rerun(self.original, current)

    def test_every_ownership_field_stays_bound_when_branch_advances(self):
        for field in self.original.keys() - {'repositoryCommit', 'resources'}:
            with self.subTest(field=field):
                current = dict(self.original, repositoryCommit='f' * 40)
                current[field] = 'changed'
                with self.assertRaisesRegex(RuntimeError, 'identities or content'):
                    verify_installation_rerun(self.original, current)
        for field in ('uid', 'sha256'):
            with self.subTest(field=field):
                current = copy.deepcopy(self.original)
                current['repositoryCommit'] = 'f' * 40
                current['resources']['Deployment/argocd-server'][field] = 'changed'
                with self.assertRaisesRegex(RuntimeError, 'identities or content'):
                    verify_installation_rerun(self.original, current)

    def test_missing_or_invalid_commit_is_refused(self):
        for commit in (None, '', 'main', 'g' * 40, 'a' * 39, 123):
            for first in (True, False):
                with self.subTest(commit=commit, first=first):
                    invalid = dict(self.original, repositoryCommit=commit)
                    with self.assertRaisesRegex(RuntimeError, 'invalid observed repository commit'):
                        verify_installation_rerun(invalid if first else self.original,
                                                  self.original if first else invalid)


if __name__ == '__main__':
    unittest.main()
