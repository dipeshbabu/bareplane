import copy
import importlib.util
import io
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import urllib.error

from existing_storage_acceptance import replace_source

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


class RetrySourceOwnershipTests(unittest.TestCase):
    def setUp(self):
        self.original = dict(repoURL='https://github.com/example/git.git', targetRevision='a'*40, path='components/storage-existing')
        self.desired = dict(self.original, kustomize=dict(patches=[]))
        self.component = SimpleNamespace(name='lab-storage', app=dict(spec=dict(source=self.original)))

    def test_status_conflict_rereads_resource_version_without_weakening_ownership(self):
        calls = []
        def api(verb, *_args):
            if verb == 'get':
                return dict(metadata=dict(uid='owned',resourceVersion=str(len(calls)+1)),spec=dict(source=self.original))
            operations = json.loads(_args[_args.index('-p')+1])
            calls.append(operations)
            if len(calls) == 1:
                raise RuntimeError('status changed')
            return {}
        with patch('existing_storage_acceptance.time.sleep'):
            replace_source(api,self.component,'owned',self.desired)
        self.assertEqual(len(calls),2)
        self.assertEqual([op['value'] for op in calls[0] if op['path']=='/metadata/resourceVersion'],['1'])
        self.assertEqual([op['value'] for op in calls[1] if op['path']=='/metadata/resourceVersion'],['2'])
        for operations in calls:
            self.assertIn(dict(op='test',path='/metadata/uid',value='owned'),operations)
            self.assertIn(dict(op='test',path='/spec/source',value=self.original),operations)

    def test_replaced_or_redirected_application_is_never_patched(self):
        for metadata,source in [(dict(uid='other',resourceVersion='1'),self.original),
                                (dict(uid='owned',resourceVersion='1',deletionTimestamp='now'),self.original),
                                (dict(uid='owned',resourceVersion='1'),dict(self.original,path='unrelated'))]:
            calls=[]
            def api(verb,*_args):
                calls.append(verb)
                return dict(metadata=metadata,spec=dict(source=source))
            with self.assertRaises(RuntimeError):
                replace_source(api,self.component,'owned',self.desired)
            self.assertEqual(calls,['get'])

    def test_ambiguously_accepted_change_is_verified_before_retry(self):
        current = dict(metadata=dict(uid='owned',resourceVersion='1'),spec=dict(source=self.original))
        patches=[]
        def api(verb,*_args):
            if verb == 'get':return copy.deepcopy(current)
            patches.append(verb)
            current['spec']['source'] = self.desired
            raise RuntimeError('response lost')
        with patch('existing_storage_acceptance.time.sleep'):
            replace_source(api,self.component,'owned',self.desired)
        self.assertEqual(patches,['patch'])


if __name__=='__main__':unittest.main()
