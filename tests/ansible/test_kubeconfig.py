"""Private kubeconfig publication tests; all credentials are ephemeral fixtures."""
import base64
import copy
import hashlib
import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml

source = Path(__file__).resolve().parents[2] / 'internal/render/ansible/assets/module_utils/bareplane_kubeconfig.py'
spec = importlib.util.spec_from_file_location('kubeconfig', source)
kubeconfig = importlib.util.module_from_spec(spec)
spec.loader.exec_module(kubeconfig)


class KubeconfigTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.credentials = tempfile.TemporaryDirectory()
        root = Path(cls.credentials.name)
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256',
                        '-nodes', '-keyout', str(root / 'key.pem'), '-out', str(root / 'cert.pem'),
                        '-subj', '/CN=bareplane-test', '-days', '1'], check=True, capture_output=True)
        cls.ca = (root / 'cert.pem').read_bytes()
        cls.ca_sha256 = hashlib.sha256(cls.ca).hexdigest()
        cls.key = (root / 'key.pem').read_bytes()
        cls.document = dict(apiVersion='v1', kind='Config', preferences={}, clusters=[dict(name='lab', cluster={
            'server': 'https://192.0.2.11:6443', 'certificate-authority-data': base64.b64encode(cls.ca).decode(),
        })], users=[dict(name='admin', user={
            'client-certificate-data': base64.b64encode(cls.ca).decode(), 'client-key-data': base64.b64encode(cls.key).decode(),
        })], contexts=[dict(name='admin@lab', context=dict(cluster='lab', user='admin'))], **{'current-context': 'admin@lab'})

    @classmethod
    def tearDownClass(cls):
        cls.credentials.cleanup()

    def setUp(self):
        self.workspace = tempfile.TemporaryDirectory()
        self.addCleanup(self.workspace.cleanup)
        self.root = Path(self.workspace.name)
        self.path = self.root / '.bareplane/state/bootstrap/admin.conf'

    def publish(self, document=None, **kwargs):
        data = yaml.safe_dump(document or self.document).encode()
        return kubeconfig.publish(str(self.path), data, kwargs.pop('cluster', 'lab'),
                                  kwargs.pop('ca_sha256', self.ca_sha256), kwargs.pop('vip', '192.0.2.100'), **kwargs)

    def test_first_write_private_permissions_and_unchanged_rerun(self):
        self.assertTrue(self.publish())
        original = self.path.read_bytes()
        modified = self.path.stat().st_mtime_ns
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.path.parent.stat().st_mode & 0o777, 0o700)
        self.assertEqual(self.path.parent.parent.stat().st_mode & 0o777, 0o700)
        self.assertFalse(self.publish())
        self.assertEqual(self.path.read_bytes(), original)
        self.assertEqual(self.path.stat().st_mtime_ns, modified)
        self.assertFalse(list(self.path.parent.glob('.admin-stage-*')))
        doc = kubeconfig.decode(original.split(b'\n', 1)[1])
        self.assertEqual(doc['clusters'][0]['cluster']['server'], 'https://192.0.2.100:6443')

    def test_wrong_cluster_and_wrong_ca_do_not_write(self):
        for kwargs in [dict(cluster='other'), dict(ca_sha256='0' * 64)]:
            with self.subTest(kwargs=kwargs), self.assertRaises(ValueError):
                self.publish(**kwargs)
            self.assertFalse(self.path.exists())

    def test_changed_vip_preserves_existing_credentials(self):
        self.publish()
        original = self.path.read_bytes()
        with self.assertRaisesRegex(ValueError, 'identity or VIP differs'):
            self.publish(vip='192.0.2.101')
        self.assertEqual(self.path.read_bytes(), original)

    def test_unmanaged_modified_and_insecure_files_are_refused(self):
        self.publish()
        original = self.path.read_bytes()
        for content in [b'unmanaged', original + b'# modified\n']:
            self.path.write_bytes(content)
            with self.subTest(content=content[:20]), self.assertRaisesRegex(ValueError, 'unmanaged, modified'):
                self.publish()
            self.assertEqual(self.path.read_bytes(), content)
        self.path.write_bytes(original)
        self.path.chmod(0o644)
        with self.assertRaisesRegex(ValueError, 'owner-only'):
            self.publish()
        self.path.chmod(0o600)
        self.path.parent.chmod(0o755)
        with self.assertRaisesRegex(ValueError, 'owner-only'):
            self.publish()

    def test_symlink_and_dangling_symlink_destinations_are_refused(self):
        self.publish()
        original = self.path.read_bytes()
        self.path.unlink()
        target = self.root / 'untouched'
        target.write_bytes(b'operator config')
        for destination in [target, self.root / 'missing']:
            self.path.symlink_to(destination)
            with self.assertRaisesRegex(ValueError, 'symlinks'):
                self.publish()
            self.path.unlink()
        self.assertEqual(target.read_bytes(), b'operator config')
        self.path.write_bytes(original)

    def test_symlink_ancestor_and_symlink_before_dotdot_are_refused(self):
        target = self.root / 'real'
        target.mkdir()
        (self.root / '.bareplane').symlink_to(target, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, 'symlinks'):
            self.publish()
        (self.root / '.bareplane').unlink()
        (self.root / '.bareplane').mkdir()
        (self.root / '.bareplane/bootstrap').symlink_to(target, target_is_directory=True)
        path = self.root / '.bareplane/bootstrap/../state/bootstrap/admin.conf'
        with self.assertRaisesRegex(ValueError, 'symlinks'):
            kubeconfig.check_destination(str(path))

    def test_global_kubeconfig_is_never_a_supported_destination(self):
        with self.assertRaisesRegex(ValueError, 'private Bareplane project state'):
            kubeconfig.check_destination(str(self.root / '.kube/config'))

    def test_check_mode_never_creates_state(self):
        self.assertTrue(self.publish(check=True))
        self.assertFalse((self.root / '.bareplane').exists())

    def test_exec_auth_file_references_and_insecure_tls_are_refused(self):
        cases = []
        for field, value in [('exec', dict(command='malicious')), ('token', 'secret'), ('client-key', '/private/key')]:
            doc = copy.deepcopy(self.document)
            doc['users'][0]['user'][field] = value
            cases.append(doc)
        for field, value in [('insecure-skip-tls-verify', True), ('proxy-url', 'https://evil'), ('certificate-authority', '/private/ca')]:
            doc = copy.deepcopy(self.document)
            doc['clusters'][0]['cluster'][field] = value
            cases.append(doc)
        for doc in cases:
            with self.subTest(fields=doc.keys()), self.assertRaisesRegex(ValueError, 'unsupported fields'):
                self.publish(doc)

    def test_malformed_structure_aliases_duplicate_keys_and_bounded_input(self):
        for data in [b'', b'x' * 65537, b'apiVersion: v1\napiVersion: v2', b'a: &value {}\nb: *value', b'a: !!str unsafe', b'[]']:
            with self.subTest(data=data[:30]), self.assertRaises(ValueError):
                kubeconfig.decode(data)
        for field, value in [('clusters', []), ('current-context', 'other')]:
            doc = copy.deepcopy(self.document)
            doc[field] = value
            with self.assertRaises(ValueError):
                self.publish(doc)

    def test_invalid_and_mismatched_credentials_do_not_leak(self):
        sentinel = 'DO-NOT-PRINT-PRIVATE-KEY'
        doc = copy.deepcopy(self.document)
        doc['users'][0]['user']['client-key-data'] = base64.b64encode(sentinel.encode()).decode()
        with self.assertRaises(ValueError) as error:
            self.publish(doc)
        self.assertNotIn(sentinel, str(error.exception))
        self.assertNotIn(doc['users'][0]['user']['client-key-data'], str(error.exception))
        self.assertFalse(self.path.exists())

    def test_client_signed_by_a_different_ca_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(['openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256',
                            '-nodes', '-keyout', str(root / 'key.pem'), '-out', str(root / 'cert.pem'),
                            '-subj', '/CN=foreign-client', '-days', '1'], check=True, capture_output=True)
            doc = copy.deepcopy(self.document)
            doc['users'][0]['user'] = {
                'client-certificate-data': base64.b64encode((root / 'cert.pem').read_bytes()).decode(),
                'client-key-data': base64.b64encode((root / 'key.pem').read_bytes()).decode(),
            }
            with self.assertRaisesRegex(ValueError, 'Cannot validate'):
                self.publish(doc)
            self.assertFalse(self.path.exists())

    def test_plain_https_and_canonical_ipv6_endpoint(self):
        for server in ['http://192.0.2.11:6443', 'https://user:secret@host', 'https://host/path', 'https://host:bad']:
            doc = copy.deepcopy(self.document)
            doc['clusters'][0]['cluster']['server'] = server
            with self.subTest(server=server), self.assertRaises(ValueError):
                self.publish(doc)
        self.assertEqual(kubeconfig.endpoint('2001:db8::100', 6443), 'https://[2001:db8::100]:6443')


if __name__ == '__main__':
    unittest.main()
