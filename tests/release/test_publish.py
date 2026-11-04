"""Publication refuses incomplete or changed evidence before exposing a release."""

from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'hack'))
import publish_release as publisher
from test_artifacts import FakeBuild, SOURCE, VERSION, release


class FakeAPI:
    def __init__(self, directory):
        self.directory = directory
        self.writes = []
        self.tag_exists = False
        self.release_exists = False
        self.wrong_digest = False
        self.tag_moved = False

    def request(self, method, path, value=None, missing=False):
        if method == 'GET':
            return {'object': {'type': 'commit', 'sha': SOURCE}} if self.tag_exists else None
        self.writes.append((method, path, value))
        if path == '/git/refs':
            return {'ref': value['ref']}
        if path == '/releases':
            return dict(value, id=27)
        if path == '/releases/27':
            return dict(value, immutable=True)
        raise AssertionError((method, path))

    def get(self, path):
        return {'object': {'type': 'commit', 'sha': 'b' * 40 if self.tag_moved else SOURCE}}

    def collection(self, path, key):
        if path == '/releases':
            return [{'tag_name': 'v' + VERSION}] if self.release_exists else []
        if path == '/releases/27/assets':
            return [dict(name=p.name, size=p.stat().st_size, state='uploaded',
                         digest='sha256:' + ('0' * 64 if self.wrong_digest else release.digest(p.read_bytes())))
                    for p in self.directory.iterdir()]
        raise AssertionError(path)


class PublisherTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='bareplane-publish-test-')
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        build = FakeBuild(root)
        self.output = root / 'release'
        release.build_release(build.root, self.output, VERSION, SOURCE, build.run)
        self.api = FakeAPI(self.output)
        self.calls = []
        self.policy = dict(publish=True, operator_evidence_sha256='c' * 64, source_sha=SOURCE,
                           version=VERSION, ci_run_id=42, ci_attempt=1, default_branch='main', prerelease=True)

    def publish(self, final=None, run_gh=None):
        with patch.object(publisher, 'verify_release_policy', side_effect=[self.policy, final or self.policy]):
            return publisher.publish(self.output, {}, self.api, run_gh or self.calls.append)

    def test_success_attests_every_file_and_sbom_then_publishes_complete_immutable_draft(self):
        result = self.publish()
        self.assertTrue(result['immutable'])
        self.assertEqual([method for method, _, _ in self.api.writes], ['POST', 'POST', 'PATCH'])
        self.assertTrue(self.api.writes[1][2]['draft'])
        self.assertFalse(self.api.writes[-1][2]['draft'])
        verifications = [call for call in self.calls if call[:2] == ['attestation', 'verify']]
        self.assertEqual(len(verifications), len(list(self.output.iterdir())) + 4)
        for call in verifications:
            self.assertIn(SOURCE, call)
            self.assertIn('--deny-self-hosted-runners', call)
            self.assertIn(publisher.REPOSITORY + '/' + publisher.WORKFLOW, call)
        self.assertEqual(sum('https://spdx.dev/Document' in call for call in verifications), 4)
        upload = [call for call in self.calls if call[:2] == ['release', 'upload']][0]
        self.assertNotIn('--clobber', upload)
        self.assertEqual(len(upload[3:-2]), len(list(self.output.iterdir())))

    def test_tampered_local_artifacts_stop_before_any_remote_operation(self):
        (self.output / 'SHA256SUMS').write_text('tampered')
        with self.assertRaises(publisher.ReleaseError):
            self.publish()
        self.assertEqual(self.api.writes, [])
        self.assertEqual(self.calls, [])

    def test_candidates_wrong_versions_and_sources_never_create_a_tag(self):
        for change in [dict(publish=False), dict(operator_evidence_sha256=''), dict(source_sha='b' * 40), dict(version='9.9.9')]:
            with self.subTest(change=change), patch.object(publisher, 'verify_release_policy', return_value=dict(self.policy, **change)):
                with self.assertRaises(publisher.ReleaseError):
                    publisher.publish(self.output, {}, self.api, self.calls.append)
                self.assertEqual(self.api.writes, [])

    def test_failed_attestation_cannot_create_any_tag_or_draft(self):
        def failed(_):
            raise publisher.ReleaseError('signature rejected')
        with self.assertRaises(publisher.ReleaseError):
            self.publish(run_gh=failed)
        self.assertEqual(self.api.writes, [])

    def test_existing_tags_and_drafts_are_never_overwritten(self):
        for field in ['tag_exists', 'release_exists']:
            self.api = FakeAPI(self.output)
            setattr(self.api, field, True)
            with self.subTest(field=field), self.assertRaises(publisher.ReleaseError):
                self.publish()
            self.assertEqual(self.api.writes, [])

    def test_partial_or_changed_uploads_preserve_private_draft(self):
        self.api.wrong_digest = True
        with self.assertRaises(publisher.ReleaseError):
            self.publish()
        self.assertEqual(len(self.api.writes), 2)
        self.assertTrue(self.api.writes[-1][2]['draft'])

    def test_changed_ci_attempt_evidence_or_tag_cannot_publish_draft(self):
        for change in [dict(ci_run_id=43), dict(ci_attempt=2), dict(operator_evidence_sha256='d' * 64), {}]:
            self.api = FakeAPI(self.output)
            self.api.tag_moved = not change
            with self.subTest(change=change), self.assertRaises(publisher.ReleaseError):
                self.publish(dict(self.policy, **change))
            self.assertEqual(len(self.api.writes), 2)
            self.assertTrue(self.api.writes[-1][2]['draft'])

    def test_upload_failure_never_publishes_or_deletes_existing_state(self):
        def fail_upload(call):
            if call[:2] == ['release', 'upload']:
                raise publisher.ReleaseError('upload failed')
        with self.assertRaises(publisher.ReleaseError):
            self.publish(run_gh=fail_upload)
        self.assertEqual([method for method, _, _ in self.api.writes], ['POST', 'POST'])


if __name__ == '__main__':
    unittest.main()
