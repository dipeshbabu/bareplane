"""Fail closed when any required CI dependency failed, skipped, or disappeared."""

import json
import os


REQUIRED = {'quality', 'host-prepare', 'join-topology'}


def verify(needs):
    if not isinstance(needs, dict) or set(needs) != REQUIRED:
        raise ValueError('Required acceptance dependency set is incomplete or changed')
    if any(not isinstance(value, dict) or value.get('result') != 'success' for value in needs.values()):
        raise ValueError('Every required quality and VM dependency must succeed; skips are not acceptance')


if __name__ == '__main__':
    verify(json.loads(os.environ['NEEDS_JSON']))
    print('All required automated v0.1 acceptance dependencies passed')
