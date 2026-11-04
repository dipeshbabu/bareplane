"""Compare immutable Argo ownership across a live branch observation."""

import re


def verify_installation_rerun(original, current):
    for receipt in (original, current):
        commit = receipt.get('repositoryCommit') if isinstance(receipt, dict) else None
        if not isinstance(commit, str) or not re.fullmatch(r'[0-9a-f]{40}', commit):
            raise RuntimeError('Argo rerun has an invalid observed repository commit')
    # A branch may advance between installs. Its resolved commit is diagnostic
    # metadata; every cluster, contract, resource UID, and content hash must stay
    # identical even when that observation legitimately changes.
    before = {key: value for key, value in original.items() if key != 'repositoryCommit'}
    after = {key: value for key, value in current.items() if key != 'repositoryCommit'}
    if after != before:
        raise RuntimeError('Argo rerun changed owned resource identities or content')
