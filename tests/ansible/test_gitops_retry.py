"""Never turn unrelated handoff failures into automatic acceptance retries."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from gitops_retry import ADVANCED, run_handoff_with_branch_retry


class HandoffBranchRetryTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.state = Path(temporary.name)
        (self.state / 'logs').mkdir()
        self.record = dict(mode='pinned', complete=False, commit='a' * 40)
        (self.state / 'handoff.json').write_text(json.dumps(self.record))
        environment = patch.dict(os.environ, GITHUB_ACTIONS='true', BAREPLANE_DISPOSABLE_VM='1')
        environment.start()
        self.addCleanup(environment.stop)
        self.calls = 0
        self.log_sequence = 0

    def fail_handoff(self, reason=ADVANCED, new_log=True):
        self.calls += 1
        if new_log:
            self.log_sequence += 1
            log = '[ERROR]: Task failed: Module failed: ' + reason + '\nfatal: [localhost]: ' + reason
            (self.state / 'logs' / ('handoff-' + str(self.log_sequence) + '.log')).write_text(log)
        raise subprocess.CalledProcessError(1, ['bareplane', 'gitops', 'handoff'])

    def test_branch_advance_retries_the_same_guarded_command(self):
        command = ['bareplane', 'gitops', 'handoff', '--approve', 'lab']

        def run(args):
            self.assertEqual(args, command)
            if not self.calls:
                self.fail_handoff()
            self.calls += 1
            return 'verified'

        self.assertEqual(run_handoff_with_branch_retry(command, self.state, run), 'verified')
        self.assertEqual(self.calls, 2)
        self.assertEqual(json.loads((self.state / 'handoff.json').read_text()), self.record)
        self.assertTrue((self.state / 'logs/handoff-1.log').exists())

    def test_repeated_branch_advance_is_bounded(self):
        with self.assertRaises(subprocess.CalledProcessError):
            run_handoff_with_branch_retry([], self.state, lambda args: self.fail_handoff())
        self.assertEqual(self.calls, 3)

    def test_unrelated_errors_and_stale_logs_are_not_retried(self):
        for reason, new_log in [('foreign ownership', True), ('network failure', True),
                                ('foreign ownership\nContext: ' + ADVANCED, True), (ADVANCED, False)]:
            with self.subTest(reason=reason, new_log=new_log):
                self.calls = 0
                (self.state / 'logs/handoff-stale.log').write_text(ADVANCED)
                with self.assertRaises(subprocess.CalledProcessError):
                    run_handoff_with_branch_retry([], self.state, lambda args: self.fail_handoff(reason, new_log))
                self.assertEqual(self.calls, 1)

    def test_following_complete_or_invalid_receipts_are_not_retried(self):
        for change in [dict(mode='following'), dict(complete=True), dict(commit='main')]:
            with self.subTest(change=change):
                self.calls = 0
                (self.state / 'handoff.json').write_text(json.dumps(dict(self.record, **change)))
                with self.assertRaises(subprocess.CalledProcessError):
                    run_handoff_with_branch_retry([], self.state, lambda args: self.fail_handoff())
                self.assertEqual(self.calls, 1)

    def test_timeout_and_non_disposable_execution_are_not_retried(self):
        def timeout(args):
            self.calls += 1
            raise subprocess.TimeoutExpired(args, 1)

        with self.assertRaises(subprocess.TimeoutExpired):
            run_handoff_with_branch_retry([], self.state, timeout)
        self.assertEqual(self.calls, 1)
        with patch.dict(os.environ, BAREPLANE_DISPOSABLE_VM='0'), self.assertRaises(RuntimeError):
            run_handoff_with_branch_retry([], self.state, timeout)
        self.assertEqual(self.calls, 1)


if __name__ == '__main__':
    unittest.main()
