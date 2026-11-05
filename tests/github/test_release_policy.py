"""No candidate, signature, or local GPU probe substitutes for release approval."""

import copy
import datetime as dt
import importlib.util
import json
from pathlib import Path
import sys
import unittest


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / '.github/scripts'))
spec = importlib.util.spec_from_file_location('release_policy', ROOT / '.github/scripts/release_policy.py')
policy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(policy)
import verify_candidate

SOURCE = 'a' * 40
NOW = dt.datetime(2026, 11, 4, 12, 0, tzinfo=dt.timezone.utc)


def values():
    return dict(GITHUB_ACTIONS='true', GITHUB_EVENT_NAME='workflow_dispatch', GITHUB_REPOSITORY=policy.REPOSITORY,
                GITHUB_REF='refs/heads/main', GITHUB_WORKFLOW_REF=policy.REPOSITORY + '/' + policy.WORKFLOW + '@refs/heads/main',
                GITHUB_SHA=SOURCE, SOURCE_SHA=SOURCE, RELEASE_VERSION='0.1.0-rc.1', RELEASE_PUBLISH='false',
                RELEASE_OPERATOR_EVIDENCE='', GITHUB_ACTOR='maintainer')


def evidence():
    return dict(source_sha=SOURCE, operator='maintainer', lab_id='disposable-lab', tested_at='2026-11-04T11:00:00Z',
                proxmox_version='9.2.1', first_run='passed', rerun='passed', failure_recovery='passed', decision='approved')


class FakeAPI:
    def __init__(self):
        self.run = dict(id=42, head_sha=SOURCE, head_branch='main', event='push', run_attempt=1,
                        status='completed', conclusion='success')
        self.jobs = [dict(name=name, status='completed', conclusion='success') for name in verify_candidate.REQUIRED]
        self.immutable = True
        self.reviewers = [{'reviewer': {'login': 'maintainer'}, 'type': 'User'}]
        self.changed_attempt = False

    def get(self, path):
        if path == '':
            return dict(default_branch='main')
        if path.startswith('/compare/'):
            return dict(status='identical', behind_by=0)
        if path == '/actions/runs/42':
            return dict(self.run, run_attempt=2) if self.changed_attempt else copy.deepcopy(self.run)
        if path == '/immutable-releases':
            return dict(enabled=self.immutable)
        if path == '/environments/release':
            return dict(protection_rules=[dict(type='required_reviewers', reviewers=self.reviewers)])
        raise AssertionError(path)

    def collection(self, path, key):
        if key == 'workflow_runs':
            return [copy.deepcopy(self.run)]
        if key == 'jobs':
            return copy.deepcopy(self.jobs)
        raise AssertionError(path)


class ReleasePolicyTests(unittest.TestCase):
    def test_candidate_is_bound_to_dispatch_source_and_never_authorizes_publication(self):
        result = policy.verify_release_policy(FakeAPI(), values(), NOW)
        self.assertFalse(result['publish'])
        self.assertFalse(result['publishing_performed'])
        self.assertEqual(result['ci_run_id'], 42)
        self.assertEqual(result['operator_evidence_sha256'], '')

    def test_untrusted_context_refs_versions_and_provenance_source_mismatches_fail(self):
        for change in [dict(GITHUB_EVENT_NAME='pull_request'), dict(GITHUB_REPOSITORY='other/repo'),
                       dict(GITHUB_REF='refs/heads/topic'), dict(SOURCE_SHA='b' * 40), dict(RELEASE_VERSION='latest'),
                       dict(RELEASE_PUBLISH='yes'), dict(GITHUB_WORKFLOW_REF='unreviewed/workflow')]:
            with self.subTest(change=change), self.assertRaises(ValueError):
                policy.verify_release_policy(FakeAPI(), dict(values(), **change), NOW)

    def test_publication_requires_exact_genuine_operator_evidence(self):
        request = dict(values(), RELEASE_PUBLISH='true', RELEASE_OPERATOR_EVIDENCE=json.dumps(evidence()))
        api = FakeAPI()
        result = policy.verify_release_policy(api, request, NOW, settings_api=api)
        self.assertTrue(result['publish'])
        self.assertEqual(len(result['operator_evidence_sha256']), 64)
        for change in [dict(source_sha='b' * 40), dict(operator='other'), dict(first_run='mocked'),
                       dict(rerun='failed'), dict(failure_recovery='skipped'), dict(decision='candidate'),
                       dict(proxmox_version='8.4.0'), dict(tested_at='2026-10-01T00:00:00Z'),
                       dict(tested_at='2026-11-05T00:00:00Z'), dict(token='PRIVATE-SENTINEL')]:
            bad = dict(request, RELEASE_OPERATOR_EVIDENCE=json.dumps(dict(evidence(), **change)))
            with self.subTest(change=change), self.assertRaises(ValueError):
                policy.verify_release_policy(FakeAPI(), bad, NOW)
        with self.assertRaises(ValueError):
            policy.verify_release_policy(FakeAPI(), dict(request, RELEASE_OPERATOR_EVIDENCE=''), NOW)

    def test_publication_requires_immutable_assets_and_manual_release_review(self):
        request = dict(values(), RELEASE_PUBLISH='true', RELEASE_OPERATOR_EVIDENCE=json.dumps(evidence()))
        for field, value in [('immutable', False), ('reviewers', [])]:
            api = FakeAPI()
            setattr(api, field, value)
            with self.subTest(field=field), self.assertRaises(ValueError):
                policy.verify_release_policy(api, request, NOW, settings_api=api)
        with self.assertRaisesRegex(ValueError, 'release-settings token'):
            policy.verify_release_policy(FakeAPI(), request, NOW)

    def test_failed_missing_pending_skipped_and_changed_attempts_block_release(self):
        for state in ['failure', 'skipped', 'cancelled', None]:
            api = FakeAPI()
            api.jobs[0]['conclusion'] = state
            with self.subTest(state=state), self.assertRaises(ValueError):
                policy.verify_release_policy(api, values(), NOW)
        api = FakeAPI()
        api.jobs.pop()
        with self.assertRaises(ValueError):
            policy.verify_release_policy(api, values(), NOW)
        api = FakeAPI()
        api.changed_attempt = True
        with self.assertRaises(ValueError):
            policy.verify_release_policy(api, values(), NOW)

    def test_settings_credential_reads_only_immutable_settings(self):
        api = FakeAPI()
        paths = []
        class SettingsAPI:
            def get(self, path):
                paths.append(path)
                if path != '/immutable-releases':
                    raise AssertionError('Administration credential was used beyond its setting')
                return dict(enabled=True)
        request = dict(values(), RELEASE_PUBLISH='true', RELEASE_OPERATOR_EVIDENCE=json.dumps(evidence()))
        self.assertTrue(policy.verify_release_policy(api, request, NOW, settings_api=SettingsAPI())['publish'])
        self.assertEqual(paths, ['/immutable-releases'])

    def test_invalid_timestamp_never_echoes_private_input(self):
        request = dict(values(), RELEASE_PUBLISH='true',
                       RELEASE_OPERATOR_EVIDENCE=json.dumps(dict(evidence(), tested_at='PRIVATE-SENTINEL')))
        with self.assertRaises(ValueError) as raised:
            policy.verify_release_policy(FakeAPI(), request, NOW)
        self.assertNotIn('PRIVATE-SENTINEL', str(raised.exception))


if __name__ == '__main__':
    unittest.main()
