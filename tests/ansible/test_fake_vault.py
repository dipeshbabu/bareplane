import json
from pathlib import Path
import ssl
import tempfile
import unittest
from urllib.error import URLError
from urllib.request import Request, urlopen

from fake_vault import FakeVault, FakeVaultServer


class FakeVaultTests(unittest.TestCase):
    def setUp(self):
        self.model = FakeVault(lambda token: token == 'valid-disposable-jwt')

    def login(self):
        code, response = self.model.handle('POST', '/v1/auth/kubernetes/login', {}, dict(role='workloads-reader', jwt='valid-disposable-jwt'))
        self.assertEqual(code, 200)
        return {'X-Vault-Token': response['auth']['client_token']}

    def test_authentication_scope_revocation_and_unavailability(self):
        for payload in [dict(role='other', jwt='valid-disposable-jwt'), dict(role='workloads-reader', jwt='wrong'), {'token': 'wrong'}]:
            self.assertEqual(self.model.handle('POST', '/v1/auth/kubernetes/login', {}, payload)[0], 403)
        headers = self.login()
        self.assertEqual(self.model.handle('GET', '/v1/kv/data/apps/database', headers)[0], 200)
        self.assertEqual(self.model.handle('GET', '/v1/kv/data/other', headers)[0], 404)
        self.assertEqual(self.model.handle('POST', '/v1/kv/data/apps/database', headers, {'data': {}})[0], 404)
        self.assertEqual(self.model.handle('PUT', '/v1/auth/token/revoke-self', headers)[0], 204)
        self.assertEqual(self.model.handle('GET', '/v1/kv/data/apps/database', headers)[0], 403)
        self.model.available = False
        self.assertEqual(self.model.handle('POST', '/v1/auth/kubernetes/login', {}, {})[0], 503)

    def test_tls_requires_the_supplied_ca_and_valid_ip_san(self):
        with tempfile.TemporaryDirectory() as directory:
            with FakeVaultServer(self.model, Path(directory) / 'tls') as server:
                request = Request(server.url + '/v1/auth/kubernetes/login', data=json.dumps(dict(role='workloads-reader', jwt='valid-disposable-jwt')).encode(),
                                  headers={'Content-Type': 'application/json'}, method='POST')
                with self.assertRaises(URLError):
                    urlopen(request, timeout=5)
                context = ssl.create_default_context(cadata=server.ca.decode())
                with urlopen(request, context=context, timeout=5) as response:
                    token = json.load(response)['auth']['client_token']
                self.assertTrue(token)
                self.assertEqual(self.model.logins, 1)

    def test_non_disposable_binding_is_refused_before_files_are_created(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'tls'
            with self.assertRaises(ValueError):
                FakeVaultServer(self.model, target, '0.0.0.0')
            self.assertFalse(target.exists())
