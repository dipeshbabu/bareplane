"""Public Git prerequisite tests; no real repository or credentials are used."""

import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import time
import unittest
from unittest.mock import patch


source = Path(__file__).resolve().parents[2] / 'internal/render/ansible/assets/module_utils/bareplane_git_repository.py'
spec = importlib.util.spec_from_file_location('git_repository', source)
repository = importlib.util.module_from_spec(spec)
spec.loader.exec_module(repository)

ROOT = 'clusters/lab'
URL = 'https://git.example.com/team/repo.git'
COMMIT = '1' * 40
KUSTOMIZATION = b'apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: [applications/argocd.yaml]\n'


class FakeCommands:
    def __init__(self, directory):
        self.directory = directory
        self.calls = []
        self.root_mode = '040000 tree '
        self.file_mode = '100644 blob '
        self.content = KUSTOMIZATION
        self.commit = COMMIT
        self.fail_fetch = False

    def run(self, argv, **kwargs):
        self.calls.append(argv)
        if 'fetch' in argv and self.fail_fetch:
            raise repository.GitOpsError('fetch failed')
        if 'rev-parse' in argv:
            return self.commit.encode()
        if 'ls-tree' in argv:
            filename = argv[-1]
            mode = self.root_mode if filename == ROOT else self.file_mode
            return (mode + COMMIT + '\t' + filename + '\0').encode()
        if 'cat-file' in argv:
            return str(len(self.content)).encode() if '-s' in argv else self.content
        return b''


class GitRepositoryTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.commands = FakeCommands(temporary.name)

    def test_resolves_explicit_commit_without_checkout_credentials_or_hooks(self):
        self.assertEqual(repository.inspect_repository(self.commands, URL, 'release/v1', ROOT), COMMIT)
        self.assertFalse(list(Path(self.commands.directory).iterdir()))
        for argv in self.commands.calls:
            self.assertIn('credential.helper=', argv)
            self.assertIn('core.hooksPath=/dev/null', argv)
            self.assertIn('http.sslVerify=true', argv)
            self.assertIn('protocol.allow=never', argv)
            self.assertNotIn('checkout', argv)
            self.assertNotIn('clone', argv)
        fetch = next(argv for argv in self.commands.calls if 'fetch' in argv)
        self.assertEqual(fetch[-2:], [URL, 'release/v1'])
        self.assertIn('--depth=1', fetch)
        self.assertIn('--no-recurse-submodules', fetch)

    def test_unsafe_contracts_fail_before_commands(self):
        for url in ['file:///tmp/repo', 'http://host/repo', 'https://user:PRIVATE-SECRET@host/repo', 'https://@host/repo',
                    'https://git.example.com/repo?token=PRIVATE-SECRET', 'https://git.example.com/repo#', 'https://git.example.com/a/../repo']:
            with self.subTest(url=url), self.assertRaises(repository.GitOpsError) as error:
                repository.inspect_repository(self.commands, url, 'main', ROOT)
            self.assertNotIn('PRIVATE-SECRET', str(error.exception))
        for revision in ['', '--upload-pack=bad', 'a..b', 'a.lock', 'a/.b', 'a//b']:
            with self.subTest(revision=revision), self.assertRaises(repository.GitOpsError):
                repository.inspect_repository(self.commands, URL, revision, ROOT)
        for root in ['', '../escape', '/absolute', '.bareplane/state', 'a\\b', 'a//b', 'a/..']:
            with self.subTest(root=root), self.assertRaises(repository.GitOpsError):
                repository.inspect_repository(self.commands, URL, 'main', root)
        self.assertEqual(self.commands.calls, [])

    def test_symlink_submodule_executable_or_missing_root_is_refused(self):
        for mode in ['120000 blob ', '160000 commit ', '100644 blob ', '']:
            self.commands.root_mode = mode
            with self.subTest(mode=mode), self.assertRaises(repository.GitOpsError):
                repository.inspect_repository(self.commands, URL, 'main', ROOT)
        self.commands.root_mode = '040000 tree '
        for mode in ['120000 blob ', '100755 blob ', '160000 commit ', '']:
            self.commands.file_mode = mode
            with self.subTest(mode=mode), self.assertRaises(repository.GitOpsError):
                repository.inspect_repository(self.commands, URL, 'main', ROOT)
        self.assertFalse(list(Path(self.commands.directory).iterdir()))

    def test_invalid_yaml_and_oversized_root_never_escape_or_leave_git_state(self):
        for content in [b'', b'x' * 65537, b'[]', b'kind: Secret', b'a: &anchor {}\nb: *anchor', b'a: !!str value',
                        b'kind: [BROKEN-PRIVATE-CONTENT', b'---\nkind: Kustomization\n---\nkind: Secret',
                        KUSTOMIZATION + b'kind: Kustomization\n', KUSTOMIZATION.replace(b'applications/argocd.yaml', b'https://example.com/unreviewed.yaml')]:
            self.commands.content = content
            with self.subTest(size=len(content)), self.assertRaises(repository.GitOpsError) as error:
                repository.inspect_repository(self.commands, URL, 'main', ROOT)
            self.assertNotIn('BROKEN-PRIVATE-CONTENT', str(error.exception))
            self.assertFalse(list(Path(self.commands.directory).iterdir()))
        self.commands.fail_fetch = True
        with self.assertRaises(repository.GitOpsError):
            repository.inspect_repository(self.commands, URL, 'main', ROOT)
        self.assertFalse(list(Path(self.commands.directory).iterdir()))

    @unittest.skipUnless(sys.platform == 'linux' and os.environ.get('BAREPLANE_TEST_PUBLIC_GIT') == '1', 'opt-in public read-only Git integration')
    def test_real_anonymous_https_repository_at_immutable_commit(self):
        commit = '50561433e64dc3a0d395f0f1ea64d627eb2ff725'
        actual = repository.inspect_repository(repository.Commands(self.commands.directory),
                                               'https://github.com/dipeshbabu/bareplane.git', commit,
                                               'internal/render/gitops/assets/argocd')
        self.assertEqual(actual, commit)
        self.assertFalse(list(Path(self.commands.directory).iterdir()))

    @unittest.skipUnless(sys.platform == 'linux', 'controller process isolation requires Linux')
    def test_commands_drop_credentials_and_bound_time_and_output(self):
        commands = repository.Commands(self.commands.directory, timeout=30)
        with patch.dict(os.environ, {'PROXMOX_API_TOKEN': 'PRIVATE-SECRET', 'GIT_CONFIG_COUNT': '1',
                                     'GIT_TRACE': '1', 'KUBECONFIG': '/foreign/config', 'SSH_AUTH_SOCK': '/foreign/agent'}):
            output = commands.run([sys.executable, '-c', 'import os,json; print(json.dumps(dict(os.environ)))'])
        environment = json.loads(output)
        for key in ['PROXMOX_API_TOKEN', 'GIT_CONFIG_COUNT', 'GIT_TRACE', 'KUBECONFIG', 'SSH_AUTH_SOCK']:
            self.assertNotIn(key, environment)
        self.assertEqual(environment['GIT_CONFIG_GLOBAL'], '/dev/null')
        self.assertEqual(environment['GIT_TERMINAL_PROMPT'], '0')
        with self.assertRaises(repository.GitOpsError):
            commands.run([sys.executable, '-c', 'print("x" * 1024)'], limit=64)
        started = time.monotonic()
        with self.assertRaises(repository.GitOpsError):
            commands.run([sys.executable, '-c', 'import time; time.sleep(60)'], timeout=0.1)
        self.assertLess(time.monotonic() - started, 5)


if __name__ == '__main__':
    unittest.main()
