"""Cleanup safety tests use local bare repositories and fake GitHub metadata."""

import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('cleanup', ROOT / '.github/scripts/cleanup_merged_branches.py')
cleanup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cleanup)
REPOSITORY = 'owner/project'
SHA = 'a' * 40


def pull(name='topic', sha=SHA, base='main', head_repo=REPOSITORY, merged=True):
    return {'merged_at': 'fixture' if merged else None,
            'head': {'ref': name, 'sha': sha, 'repo': {'full_name': head_repo}},
            'base': {'ref': base, 'repo': {'full_name': REPOSITORY}}}


class CleanupTests(unittest.TestCase):
    def test_only_unchanged_same_repo_default_branch_merges_are_candidates(self):
        rows = [pull(), pull(), pull('advanced'), pull('missing'), pull('main'), pull('master'),
                pull('protected'), pull('active'), pull('unmerged', merged=False), pull('fork', head_repo='other/project'), pull('release-target', base='release')]
        heads = {'refs/heads/' + row['head']['ref']: SHA for row in rows if row['head']['ref'] != 'missing'}
        heads['refs/heads/advanced'] = 'b' * 40
        self.assertEqual(cleanup.candidates(rows, REPOSITORY, 'main', heads, {'protected', 'active'}), [('refs/heads/topic', SHA)])
        self.assertEqual(cleanup.candidates([pull('trunk', base='trunk')], REPOSITORY, 'trunk', {'refs/heads/trunk': SHA}, set()), [])

    def test_open_and_protected_branches_are_filtered_before_deletion(self):
        class API:
            def get(self, endpoint):
                return {'default_branch': 'main'}

            def pages(self, endpoint, **parameters):
                if endpoint == '/branches':
                    return [{'name': 'protected'}]
                if parameters['state'] == 'open':
                    return [pull('active', merged=False)]
                return [pull(), pull('protected'), pull('active')]

        class Git:
            def heads(self):
                return {'refs/heads/' + name: SHA for name in ['topic', 'active', 'protected']}

            def delete(self, ref, expected):
                self.deleted = (ref, expected)
                return 'deleted'

        git = Git()
        self.assertEqual(cleanup.cleanup(API(), git, REPOSITORY), [{'ref': 'refs/heads/topic', 'result': 'deleted'}])
        self.assertEqual(git.deleted, ('refs/heads/topic', SHA))

    def test_inspection_failure_does_not_delete_anything(self):
        class API:
            def get(self, endpoint):
                return {'default_branch': 'main'}

            def pages(self, endpoint, **parameters):
                raise cleanup.CleanupError('inspection failed')

        class Git:
            def delete(self, *args):
                raise AssertionError('deleted after failed inspection')

        with self.assertRaises(cleanup.CleanupError):
            cleanup.cleanup(API(), Git(), REPOSITORY)

    def test_credentials_are_environment_only_and_overrides_are_dropped(self):
        with patch.dict(os.environ, {'GH_TOKEN': 'PRIVATE-SENTINEL', 'GIT_TRACE': '1', 'GIT_CONFIG_COUNT': '999',
                                     'GIT_CONFIG_KEY_99': 'malicious', 'SSH_AUTH_SOCK': '/foreign/agent'}):
            env = cleanup.git_environment('PRIVATE-SENTINEL')
        for key in ['GH_TOKEN', 'GIT_TRACE', 'GIT_CONFIG_KEY_99', 'SSH_AUTH_SOCK']:
            self.assertNotIn(key, env)
        args = cleanup.deletion_arguments('https://github.com/owner/project.git', 'refs/heads/topic', SHA)
        self.assertNotIn('PRIVATE-SENTINEL', repr(args))
        self.assertIn('--force-with-lease=refs/heads/topic:' + SHA, args)
        self.assertEqual(args[-1], ':refs/heads/topic')
        self.assertEqual(env['GIT_TERMINAL_PROMPT'], '0')
        self.assertEqual(env['GIT_CONFIG_GLOBAL'], os.devnull)

    def test_cleanup_workflow_executes_only_trusted_default_branch_code(self):
        text = (ROOT / '.github/workflows/cleanup-branches.yml').read_text()
        self.assertIn('ref: ${{ github.event.repository.default_branch }}', text)
        self.assertIn('persist-credentials: false', text)
        self.assertNotIn('ref: ${{ github.event.pull_request.head', text)
        self.assertNotIn('gh api --method DELETE', text)

    def test_successful_deletions_are_reported_before_a_later_failure(self):
        class API:
            def get(self, endpoint):
                return {'default_branch': 'main'}

            def pages(self, endpoint, **parameters):
                return [pull('a'), pull('b')] if parameters.get('state') == 'closed' else []

        class Git:
            def heads(self):
                return {'refs/heads/a': SHA, 'refs/heads/b': SHA}

            def delete(self, ref, expected):
                if ref == 'refs/heads/b':
                    raise cleanup.CleanupError('permission denied')
                return 'deleted'

        reported = []
        with self.assertRaises(cleanup.CleanupError):
            cleanup.cleanup(API(), Git(), REPOSITORY, report=reported.append)
        self.assertEqual(reported, [{'ref': 'refs/heads/a', 'result': 'deleted'}])


class AtomicLeaseTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='bareplane-cleanup-test-')
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        remote, sender = root / 'remote.git', root / 'sender.git'
        remote.mkdir()
        sender.mkdir()
        self.remote = cleanup.Git(str(remote), str(remote))
        self.sender = cleanup.Git(str(sender), str(remote))
        tree = self.remote.run(['mktree']).stdout.decode().strip()
        args = ['-c', 'user.name=bareplane-test', '-c', 'user.email=test@example.invalid', 'commit-tree', tree]
        self.first = self.remote.run(args + ['-m', 'merged']).stdout.decode().strip()
        self.second = self.remote.run(args + ['-p', self.first, '-m', 'new unmerged commit']).stdout.decode().strip()
        self.ref = 'refs/heads/topic'
        self.remote.run(['update-ref', self.ref, self.first])

    def test_successful_delete_and_already_deleted_race_are_idempotent(self):
        self.assertEqual(self.sender.delete(self.ref, self.first), 'deleted')
        self.assertNotIn(self.ref, self.sender.heads())
        # A second worker held the same expected SHA before the first delete.
        self.assertEqual(self.sender.delete(self.ref, self.first), 'already-deleted')

    def test_advanced_branch_is_preserved_atomically(self):
        observed = self.sender.heads()[self.ref]
        self.remote.run(['update-ref', self.ref, self.second, self.first])
        self.assertEqual(self.sender.delete(self.ref, observed), 'advanced-preserved')
        self.assertEqual(self.sender.heads()[self.ref], self.second)

    def test_real_rejection_is_not_silently_ignored(self):
        self.remote.run(['config', 'receive.denyDeletes', 'true'])
        with self.assertRaisesRegex(cleanup.CleanupError, 'still exists'):
            self.sender.delete(self.ref, self.first)
        self.assertEqual(self.sender.heads()[self.ref], self.first)

    def test_invalid_ref_or_expected_oid_cannot_delete(self):
        for ref, sha in [('refs/tags/topic', self.first), ('refs/heads/topic:main', self.first), (self.ref, 'not-an-oid')]:
            with self.subTest(ref=ref), self.assertRaises(cleanup.CleanupError):
                self.sender.delete(ref, sha)
        self.assertEqual(self.sender.heads()[self.ref], self.first)

    def test_shell_metacharacters_are_literal_ref_names(self):
        ref = "refs/heads/topic;echo-owned'$value"
        self.remote.run(['update-ref', ref, self.first])
        self.assertEqual(self.sender.delete(ref, self.first), 'deleted')
        self.assertNotIn(ref, self.sender.heads())
        self.assertEqual(self.sender.heads()[self.ref], self.first)


if __name__ == '__main__':
    unittest.main()
