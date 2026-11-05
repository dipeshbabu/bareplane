import copy
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import urllib.error

SOURCE = Path(__file__).resolve().parents[2] / 'internal/render/gitops/storage/class_check.py'
spec = importlib.util.spec_from_file_location('existing_storage_check', SOURCE)
storage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(storage)


def valid_class():
    return dict(apiVersion='storage.k8s.io/v1', kind='StorageClass', metadata=dict(name='fast-data', uid='existing-uid'),
                provisioner='example.test/driver', reclaimPolicy='Retain', allowVolumeExpansion=True,
                volumeBindingMode='WaitForFirstConsumer')


class ExistingStorageContractTests(unittest.TestCase):
    def test_valid_contract_does_not_change_the_object_or_claim_backend_health(self):
        value = valid_class()
        before = copy.deepcopy(value)
        result = storage.inspect(value, 'fast-data')
        self.assertEqual(value, before)
        self.assertTrue(result['retained'])
        self.assertFalse(result['backend_health_verified'])
        self.assertFalse(result['provisioning_performed'])

    def test_missing_static_deleting_and_unsafe_contracts_are_refused(self):
        for change in [dict(provisioner='kubernetes.io/no-provisioner'), dict(reclaimPolicy='Delete'),
                       dict(allowVolumeExpansion=False), dict(allowVolumeExpansion='true'),
                       dict(volumeBindingMode='other'), dict(metadata=dict(name='other',uid='existing-uid')),
                       dict(metadata=dict(name='fast-data',uid='existing-uid',deletionTimestamp='now'))]:
            with self.subTest(change=change), self.assertRaises(storage.Refusal):
                storage.inspect(dict(valid_class(), **change), 'fast-data')
        for name in ['', '../other', 'Other', 'a..b', 'x' * 254]:
            with self.subTest(name=name), self.assertRaises(storage.Refusal):
                storage.inspect(valid_class(), name)

    def test_fetch_is_bounded_and_uses_only_one_get_with_projected_tls(self):
        with tempfile.TemporaryDirectory() as temporary:
            account = Path(temporary)
            (account/'token').write_text('PRIVATE-TOKEN')
            (account/'ca.crt').write_text('public test CA')
            calls=[]
            class Response:
                def __enter__(self):return self
                def __exit__(self,*_args):pass
                def read(self,limit):
                    self.limit=limit
                    return json.dumps(valid_class()).encode()[:limit]
            def opener(request, **kwargs):
                calls.append((request,kwargs))
                return Response()
            with patch.object(storage.ssl,'create_default_context',return_value=object()) as context:
                result=storage.fetch('fast-data',dict(KUBERNETES_SERVICE_HOST='10.96.0.1'),account,opener)
            self.assertEqual(len(calls),1)
            self.assertEqual(calls[0][0].get_method(),'GET')
            self.assertEqual(calls[0][0].full_url,'https://10.96.0.1:443/apis/storage.k8s.io/v1/storageclasses/fast-data')
            self.assertEqual(calls[0][1],dict(timeout=10))
            context.assert_called_once_with(cafile=str(account/'ca.crt'))
            self.assertNotIn('PRIVATE-TOKEN',json.dumps(result))
            (account/'token').write_bytes(b'x'*16385)
            with self.assertRaises(storage.Refusal):
                storage.fetch('fast-data',dict(KUBERNETES_SERVICE_HOST='10.96.0.1'),account,opener)
            self.assertEqual(len(calls),1)

    def test_api_errors_and_redirects_do_not_expose_credentials_or_bodies(self):
        error=urllib.error.HTTPError('https://api/',403,'PRIVATE-SENTINEL',{},io.BytesIO(b'PRIVATE-SENTINEL'))
        with patch.object(storage.sys,'argv',['check','fast-data']), patch.object(storage,'fetch',side_effect=error), patch.object(storage.sys,'stderr',new_callable=io.StringIO) as output:
            self.assertEqual(storage.main(),1)
            self.assertNotIn('PRIVATE-SENTINEL',output.getvalue())
        with self.assertRaises(storage.Refusal) as raised:
            storage.NoRedirect().redirect_request(None,None,302,'PRIVATE-SENTINEL',{},'https://other/PRIVATE-SENTINEL')
        self.assertNotIn('PRIVATE-SENTINEL',str(raised.exception))


if __name__=='__main__':unittest.main()
