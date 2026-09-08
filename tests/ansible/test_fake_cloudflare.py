import json
import unittest
from urllib.request import Request, urlopen

from fake_cloudflare import FakeCloudflare, FakeCloudflareServer


class FakeCloudflareTests(unittest.TestCase):
    def setUp(self):
        self.model = FakeCloudflare()
        self.path = '/client/v4/zones/' + self.model.zone_id + '/dns_records'
        self.auth = 'Bearer ' + self.model.token

    def test_denied_credentials_and_wrong_zone_never_mutate_records(self):
        self.model.seed('unowned.apps.example.test', 'A', '192.0.2.10')
        before = self.model.snapshot()
        status, _ = self.model.handle('POST', self.path, 'Bearer invalid', dict(type='A', name='new.apps.example.test', content='192.0.2.11'))
        self.assertEqual(status, 403)
        status, _ = self.model.handle('GET', self.path.replace(self.model.zone_id, 'c' * 32), self.auth)
        self.assertEqual(status, 404)
        self.assertEqual(self.model.snapshot(), before)

    def test_batch_is_atomic_and_individual_record_updates_preserve_ids(self):
        good = dict(type='A', name='app.apps.example.test', content='192.0.2.11', ttl=300)
        status, _ = self.model.handle('POST', self.path + '/batch', self.auth, {'posts': [good, {'type': 'unknown'}]})
        self.assertEqual(status, 400)
        self.assertEqual(self.model.snapshot(), [])
        status, response = self.model.handle('POST', self.path, self.auth, good)
        self.assertEqual(status, 200)
        identifier = response['result']['id']
        status, response = self.model.handle('PUT', self.path + '/' + identifier, self.auth, dict(good, content='192.0.2.12'))
        self.assertEqual(status, 200)
        self.assertEqual(response['result']['id'], identifier)
        self.assertEqual(response['result']['content'], '192.0.2.12')

    def test_loopback_http_implements_cloudflare_response_shape(self):
        with FakeCloudflareServer(self.model) as server:
            request = Request(server.url + 'zones/' + self.model.zone_id, headers={'Authorization': self.auth})
            with urlopen(request, timeout=5) as response:
                value = json.load(response)
            self.assertTrue(value['success'])
            self.assertEqual(value['result']['name'], 'example.test')

    def test_non_disposable_external_binding_is_refused(self):
        with self.assertRaises(ValueError):
            FakeCloudflareServer(self.model, '0.0.0.0')
