"""Restricted plugin contract and real, throwaway age/PGP encrypted fixtures."""

import base64
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import unittest
from unittest.mock import patch


SOURCE = Path(__file__).resolve().parents[2] / 'internal/render/gitops/sops'
spec = importlib.util.spec_from_file_location('bareplane_sops_plugin', SOURCE / 'generate.py')
plugin = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plugin)
ENCRYPTED = 'ENC[AES256_GCM,data:YQ==,iv:YQ==,tag:YQ==,type:str]'
POLICY = dict(age=True, pgp=False, passphrase=False, namespaces=['workloads'])


def fixture():
    return dict(apiVersion='v1', kind='Secret', metadata=dict(name='sample', namespace='workloads'), type='Opaque',
                stringData=dict(password=ENCRYPTED), sops=dict(age=[dict(recipient='age1' + 'q' * 58, enc='cipher')],
                lastmodified='2026-09-09T00:00:00Z', mac=ENCRYPTED, encrypted_regex='^(data|stringData)$', version='3.13.3'))


class SOPSContractTests(unittest.TestCase):
    def test_main_suppresses_private_errors_and_emits_no_partial_secret(self):
        output, errors = io.BytesIO(), io.StringIO()
        stdout = io.TextIOWrapper(output)
        with patch.object(plugin, 'read_regular', return_value=json.dumps(POLICY).encode()), \
                patch.object(plugin, 'generate', side_effect=RuntimeError('sensitive-plaintext-error')), \
                patch.object(plugin.sys, 'stdout', stdout), patch.object(plugin.sys, 'stderr', errors), \
                patch.object(plugin.signal, 'signal'), patch.object(plugin.signal, 'alarm'), \
                patch.object(plugin.os, 'umask'), patch.object(plugin.resource, 'setrlimit'), \
                patch.dict(os.environ, {'ARGOCD_APP_NAMESPACE': 'workloads'}, clear=True):
            self.assertEqual(plugin.main(), 1)
        stdout.flush()
        self.assertEqual(output.getvalue(), b'')
        self.assertNotIn('sensitive-plaintext-error', errors.getvalue())
        self.assertIn('SOPS generation refused', errors.getvalue())

    def test_refuses_plaintext_and_ambiguous_or_unsafe_documents_before_decryption(self):
        cases = [
            lambda obj: obj['stringData'].update(password='plaintext'),
            lambda obj: obj['stringData'].update(password=''),
            lambda obj: obj['stringData'].update(password=42),
            lambda obj: obj.update(kind='List'),
            lambda obj: obj.update(type='kubernetes.io/service-account-token'),
            lambda obj: obj.update(immutable=True),
            lambda obj: obj.update(data={'other': ENCRYPTED}),
            lambda obj: obj['metadata'].update(namespace='argocd'),
            lambda obj: obj['metadata'].update(namespace='other'),
            lambda obj: obj['metadata'].update(annotations={'credential': 'plaintext'}),
            lambda obj: obj['sops'].update(mac_only_encrypted=True),
            lambda obj: obj['sops'].update(mac_only_encrypted=False),
            lambda obj: obj['sops'].update(unencrypted_suffix='_plain'),
            lambda obj: obj['sops'].update(encrypted_regex='password'),
            lambda obj: obj['sops'].update(key_groups=[]),
            lambda obj: obj['sops'].update(kms=[{'arn': 'untrusted'}]),
            lambda obj: obj['sops'].update(mac='invalid'),
            lambda obj: obj['sops'].update(age=[dict(recipient='age1plugin1abc', enc='cipher')]),
            lambda obj: obj['sops'].update(age=[]),
            lambda obj: obj['sops'].update(pgp=[dict(fp='short', enc='cipher', created_at='today')]),
        ]
        plugin.validate_ciphertext(fixture(), POLICY)
        for mutate in cases:
            with self.subTest(case=cases.index(mutate)):
                obj = fixture()
                mutate(obj)
                with self.assertRaises(plugin.Refusal):
                    plugin.validate_ciphertext(obj, POLICY)

    def test_duplicate_keys_and_non_json_constants_are_refused(self):
        for data in [b'{"sops":{},"sops":{}}', b'{"x":NaN}', b'{"x":Infinity}']:
            with self.assertRaises(plugin.Refusal):
                plugin.decode(data)

    def test_docker_secret_content_and_canonical_base64_are_validated_before_output(self):
        obj = fixture()
        obj.pop('sops')
        obj.update(type='kubernetes.io/dockerconfigjson', stringData={'.dockerconfigjson': '{"auths":{}}'})
        plugin.validate_secret(obj, POLICY['namespaces'], encrypted=False)
        for value in ['not-json', '[]', '{"auths":null}', '{"auths":{},"auths":{}}']:
            obj['stringData']['.dockerconfigjson'] = value
            with self.subTest(value=value), self.assertRaises((ValueError, plugin.Refusal)):
                plugin.validate_secret(obj, POLICY['namespaces'], encrypted=False)
        obj.update(type='Opaque', data={'password': 'YR=='})
        obj.pop('stringData')
        with self.assertRaises(plugin.Refusal):
            plugin.validate_secret(obj, POLICY['namespaces'], encrypted=False)

    def test_input_symlinks_fifos_and_size_are_refused_without_hanging(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'file').write_bytes(b'x' * 10)
            (root / 'link').symlink_to(root / 'file')
            os.mkfifo(root / 'fifo')
            for path, limit in [('file', 9), ('link', 100), ('fifo', 100)]:
                with self.subTest(path=path), self.assertRaises((OSError, plugin.Refusal)):
                    plugin.read_regular(root / path, limit)

    def test_failure_does_not_publish_partial_output_and_removes_private_workspace(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'age').mkdir()
            (root / 'age/key').write_bytes(b'deliberately-invalid')
            source = root / 'secret.sops.json'
            source.write_text(json.dumps(fixture()))
            with patch.object(plugin, 'run_private', side_effect=plugin.Refusal('private body')):
                with self.assertRaises(plugin.Refusal):
                    plugin.generate(source, POLICY, key_dir=root, temporary=directory)
            self.assertFalse(list(root.glob('bareplane-sops-*')))

    def test_decrypted_resource_is_validated_and_stringdata_is_converted(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'age').mkdir()
            (root / 'age/key').write_bytes(b'unit-test-key')
            source = root / 'secret.sops.json'
            source.write_text(json.dumps(fixture()))
            plain = fixture()
            plain.pop('sops')
            plain['stringData']['password'] = 'unicode:\u2603'

            def decrypt(args, environment, cwd, **kwargs):
                self.assertEqual(set(environment), {'PATH', 'HOME', 'GNUPGHOME', 'SOPS_AGE_KEY_FILE', 'SOPS_GPG_EXEC',
                    'BAREPLANE_SOPS_PASSPHRASE_FILE', 'TMPDIR', 'LANG'})
                Path(args[args.index('--output') + 1]).write_text(json.dumps(plain))

            with patch.object(plugin, 'run_private', side_effect=decrypt):
                result = json.loads(plugin.generate(source, POLICY, key_dir=root, temporary=directory))
                self.assertNotIn('stringData', result)
                self.assertEqual(base64.b64decode(result['data']['password']).decode(), plain['stringData']['password'])
                self.assertEqual(result['metadata']['labels'], {'bareplane.io/sops-managed': 'true'})
                plain['metadata']['namespace'] = 'other'
                with self.assertRaises(plugin.Refusal):
                    plugin.generate(source, POLICY, key_dir=root, temporary=directory)
            self.assertFalse(list(root.glob('bareplane-sops-*')))


@unittest.skipUnless(os.environ.get('BAREPLANE_TEST_SOPS') == '1', 'real SOPS fixtures require pinned sops and age-keygen')
class SOPSRuntimeTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='bareplane-sops-test-')
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.repository = self.root / 'repository'
        self.repository.mkdir()
        self.keys = self.root / 'keys'
        self.keys.mkdir()
        self.sops = os.environ.get('BAREPLANE_TEST_SOPS_BINARY', 'sops')
        self.keygen = os.environ.get('BAREPLANE_TEST_AGE_KEYGEN', 'age-keygen')
        self.secret = 'ephemeral-sops-test-' + secrets.token_hex(24)
        self.env = {'PATH': os.environ['PATH'], 'HOME': str(self.root), 'GNUPGHOME': str(self.root / 'gnupg')}
        (self.root / 'gnupg').mkdir(mode=0o700)
        self.config = self.root / 'config'
        self.config.mkdir()
        (self.config / 'gpg').write_bytes((SOURCE / 'gpg').read_bytes().replace(b'\r\n', b'\n'))
        (self.config / 'gpg').chmod(0o700)
        self.source = self.repository / 'secret.sops.json'

    def command(self, args, data=None):
        result = subprocess.run(args, input=data, capture_output=True, env=self.env, cwd=self.repository, timeout=60)
        if result.returncode:
            categories = [pattern for pattern in [b'filename', b'config', b'unknown', b'recipient', b'permission', b'JSON', b'flag'] if pattern in result.stderr]
            raise RuntimeError('Private SOPS fixture command failed; fixed categories: ' + str(categories))
        return result.stdout

    def age(self, name='age'):
        directory = self.keys / name
        directory.mkdir()
        self.command([self.keygen, '-o', str(directory / 'key')])
        return self.command([self.keygen, '-y', str(directory / 'key')]).decode().strip()

    def encrypt(self, recipients, modified=None):
        obj = dict(apiVersion='v1', kind='Secret', metadata=dict(name='sample', namespace='workloads'), type='Opaque', stringData=dict(password=self.secret))
        if modified:
            modified(obj)
        encrypted = self.command([self.sops, 'encrypt', '--input-type', 'json', '--output-type', 'json',
                                  '--filename-override', 'secret.sops.json', '--encrypted-regex', '^(data|stringData)$', *recipients], json.dumps(obj).encode())
        self.assertNotIn(self.secret.encode(), encrypted)
        self.source.write_bytes(encrypted)
        return encrypted

    def generate(self, policy=POLICY):
        return plugin.generate(self.source, policy, key_dir=self.keys, config_dir=self.config, temporary=str(self.root), sops=self.sops)

    def assert_plain(self, value):
        self.assertEqual(base64.b64decode(json.loads(value)['data']['password']).decode(), self.secret)
        self.assertFalse(list(self.root.glob('bareplane-sops-*')))

    def test_age_roundtrip_full_mac_tampering_wrong_key_rotation_and_git_history(self):
        recipient = self.age()
        encrypted = self.encrypt(['--age', recipient])
        self.assert_plain(self.generate())
        # Public metadata is authenticated too; this is not mac_only_encrypted.
        altered = json.loads(encrypted)
        altered['metadata']['name'] = 'tampered'
        self.source.write_text(json.dumps(altered))
        with self.assertRaises(plugin.Refusal):
            self.generate()
        self.source.write_bytes(encrypted)
        replacement = self.age('replacement')
        old_key = (self.keys / 'age/key').read_bytes()
        (self.keys / 'age/key').write_bytes((self.keys / 'replacement/key').read_bytes())
        with self.assertRaises(plugin.Refusal):
            self.generate()
        self.command(['git', 'init', '-q'])
        self.command(['git', 'config', 'user.name', 'Disposable SOPS test'])
        self.command(['git', 'config', 'user.email', 'sops@example.invalid'])
        self.command(['git', 'add', 'secret.sops.json'])
        self.command(['git', 'commit', '-qm', 'encrypted fixture'])
        self.encrypt(['--age', replacement])
        self.assert_plain(self.generate())
        self.command(['git', 'add', 'secret.sops.json'])
        self.command(['git', 'commit', '-qm', 'rotate encrypted fixture'])
        objects = self.command(['git', 'cat-file', '--batch-all-objects', '--batch'])
        for forbidden in [self.secret.encode(), old_key, (self.keys / 'replacement/key').read_bytes()]:
            self.assertNotIn(forbidden, objects)
        self.assertFalse(list(self.root.glob('bareplane-sops-*')))

    def test_pgp_passphrase_roundtrip_and_wrong_passphrase_refusal(self):
        directory = self.keys / 'pgp'
        directory.mkdir()
        passphrase = self.keys / 'passphrase'
        passphrase.mkdir()
        (passphrase / 'key').write_text('ephemeral-passphrase-' + secrets.token_hex(12))
        self.command(['/usr/bin/gpg', '--no-options', '--batch', '--pinentry-mode', 'loopback', '--passphrase-file', str(passphrase / 'key'),
                      '--quick-generate-key', 'Disposable SOPS <sops@example.invalid>', 'rsa2048', 'encr', '1d'])
        listing = self.command(['/usr/bin/gpg', '--no-options', '--batch', '--with-colons', '--list-secret-keys']).decode()
        fingerprint = next(line.split(':')[9] for line in listing.splitlines() if line.startswith('fpr:'))
        exported = self.command(['/usr/bin/gpg', '--no-options', '--batch', '--pinentry-mode', 'loopback', '--passphrase-file', str(passphrase / 'key'),
                                 '--armor', '--export-secret-keys', fingerprint])
        (directory / 'key').write_bytes(exported)
        self.encrypt(['--pgp', fingerprint])
        policy = dict(POLICY, age=False, pgp=True, passphrase=True)
        self.assert_plain(self.generate(policy))
        (passphrase / 'key').write_text('wrong-passphrase')
        with self.assertRaises(plugin.Refusal):
            self.generate(policy)
        self.command(['/usr/bin/gpgconf', '--kill', 'all'])
