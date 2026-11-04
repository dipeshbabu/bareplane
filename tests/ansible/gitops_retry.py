"""Bounded live-branch retries only inside disposable VM acceptance."""

import json
import os
from pathlib import Path
import re
import subprocess


ADVANCED = 'Configured revision advanced during initial verification; the root remains pinned; retry the new reviewed snapshot'
LIMIT = 4 * 1024 * 1024


def retryable_branch_advance(state, previous_logs):
    logs = set((state / 'logs').glob('handoff-*.log')) - previous_logs
    if len(logs) != 1:
        return False
    try:
        with logs.pop().open('rb') as stream:
            log = stream.read(LIMIT + 1)
        with (state / 'handoff.json').open('rb') as stream:
            receipt = stream.read(65537)
        if len(log) > LIMIT or len(receipt) > 65536:
            return False
        record = json.loads(receipt)
        failures = [line for line in log.decode(errors='replace').splitlines()
                    if line.startswith('[ERROR]') or line.startswith('fatal:')]
        return (bool(failures) and all(ADVANCED in line for line in failures) and record.get('mode') == 'pinned'
                and record.get('complete') is False and isinstance(record.get('commit'), str)
                and re.fullmatch(r'[0-9a-f]{40}', record['commit']) is not None)
    except (OSError, ValueError, TypeError, AttributeError):
        return False


def run_handoff_with_branch_retry(command, state, run):
    if os.environ.get('GITHUB_ACTIONS') != 'true' or os.environ.get('BAREPLANE_DISPOSABLE_VM') != '1':
        raise RuntimeError('Live branch retries are restricted to disposable GitHub VM acceptance')
    state = Path(state)
    for attempt in range(3):
        previous_logs = set((state / 'logs').glob('handoff-*.log'))
        try:
            return run(command)
        except subprocess.CalledProcessError:
            if attempt == 2 or not retryable_branch_advance(state, previous_logs):
                raise
