import copy
import importlib.util
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[2]


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / '.github/scripts' / (name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


gate = load('ci_acceptance_gate')
candidate = load('verify_candidate')


class AcceptanceGateTests(unittest.TestCase):
    def test_dependency_gate_requires_every_exact_job_success(self):
        needs = {name: {'result': 'success'} for name in gate.REQUIRED}
        gate.verify(needs)
        for status in ['failure', 'cancelled', 'skipped', None]:
            changed = copy.deepcopy(needs)
            changed['quality']['result'] = status
            with self.subTest(status=status), self.assertRaises(ValueError):
                gate.verify(changed)
        with self.assertRaises(ValueError):
            gate.verify({'quality': {'result': 'success'}})

    def test_candidate_uses_exact_default_branch_source_and_latest_run(self):
        commit = 'a' * 40
        successful = dict(id=1, head_sha=commit, head_branch='main', event='push', status='completed', conclusion='success')
        self.assertEqual(candidate.verify_runs([successful], commit, 'main'), successful)
        for overrides in [dict(head_sha='b' * 40), dict(head_branch='topic'), dict(event='pull_request'), dict(status='in_progress'), dict(conclusion='failure')]:
            with self.subTest(overrides=overrides), self.assertRaises(ValueError):
                candidate.verify_runs([dict(successful, **overrides)], commit, 'main')
        with self.assertRaises(ValueError):
            candidate.verify_runs([successful, dict(successful, id=2, conclusion='failure')], commit, 'main')

    def test_candidate_refuses_skipped_missing_duplicate_or_additional_failed_jobs(self):
        jobs = [dict(name=name, status='completed', conclusion='success') for name in sorted(candidate.REQUIRED)]
        candidate.verify_jobs(jobs)
        for bad in [jobs[:-1], jobs + [jobs[0]], jobs + [dict(name='extra', status='completed', conclusion='failure')]]:
            with self.assertRaises(ValueError):
                candidate.verify_jobs(bad)
        for status in ['failure', 'cancelled', 'skipped', None]:
            changed = copy.deepcopy(jobs)
            changed[0]['conclusion'] = status
            with self.subTest(status=status), self.assertRaises(ValueError):
                candidate.verify_jobs(changed)

    def test_candidate_attempt_cannot_change_during_verification(self):
        run = dict(id=1, head_sha='a' * 40, run_attempt=1, status='completed', conclusion='success')
        candidate.verify_same_attempt(run, dict(run))
        for change in [dict(run_attempt=2), dict(status='in_progress'), dict(conclusion='failure'), dict(head_sha='b' * 40)]:
            with self.subTest(change=change), self.assertRaises(ValueError):
                candidate.verify_same_attempt(run, dict(run, **change))


if __name__ == '__main__':
    unittest.main()
