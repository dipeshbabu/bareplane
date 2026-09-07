#!/usr/bin/env python3
"""Delete unchanged same-repository topic refs using an atomic expected-OID lease."""

import base64
import json
import os
import re
import subprocess
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request


class CleanupError(RuntimeError):
    pass


def git_environment(token=None):
    allowed = {'PATH', 'SYSTEMROOT', 'WINDIR', 'TEMP', 'TMP', 'LANG', 'LC_ALL', 'LC_CTYPE'}
    env = {key: value for key, value in os.environ.items() if key.upper() in allowed}
    settings = [('credential.helper', ''), ('core.hooksPath', os.devnull)]
    if token:
        authorization = base64.b64encode(('x-access-token:' + token).encode()).decode()
        settings.append(('http.https://github.com/.extraheader', 'Authorization: basic ' + authorization))
    env.update(GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull,
               GIT_TERMINAL_PROMPT='0', GIT_CONFIG_COUNT=str(len(settings)))
    for index, (key, value) in enumerate(settings):
        env['GIT_CONFIG_KEY_' + str(index)] = key
        env['GIT_CONFIG_VALUE_' + str(index)] = value
    return env


def deletion_arguments(remote, ref, expected):
    if not isinstance(ref, str) or not ref.startswith('refs/heads/') or not re.fullmatch(r'[0-9a-f]{40}', expected):
        raise CleanupError('Invalid expected branch identity')
    return ['push', '--porcelain', '--force-with-lease=' + ref + ':' + expected, remote, ':' + ref]


class Git:
    def __init__(self, directory, remote, token=None):
        self.directory, self.remote, self.env = directory, remote, git_environment(token)
        self.run(['init', '--bare', '--template=', directory])

    def run(self, args, allow_failure=False):
        try:
            result = subprocess.run(['git', *args], cwd=self.directory, env=self.env, stdin=subprocess.DEVNULL,
                                    capture_output=True, timeout=60, check=False)
        except (OSError, subprocess.SubprocessError):
            raise CleanupError('Git cleanup command failed to start or timed out') from None
        if len(result.stdout) > 8 * 1024 * 1024 or len(result.stderr) > 1024 * 1024:
            raise CleanupError('Git cleanup output exceeded its limit')
        if result.returncode and not allow_failure:
            raise CleanupError('Git cleanup prerequisite failed; check repository access')
        return result

    def heads(self):
        output = self.run(['ls-remote', '--heads', self.remote]).stdout.decode()
        heads = {}
        for line in output.splitlines():
            fields = line.split('\t')
            if len(fields) != 2 or not re.fullmatch(r'[0-9a-f]{40}', fields[0]) or not fields[1].startswith('refs/heads/'):
                raise CleanupError('Unexpected remote branch advertisement')
            heads[fields[1]] = fields[0]
        return heads

    def delete(self, ref, expected):
        self.run(['check-ref-format', ref])
        result = self.run(deletion_arguments(self.remote, ref, expected), allow_failure=True)
        if result.returncode == 0:
            return 'deleted'
        current = self.heads().get(ref)
        if current is None:
            return 'already-deleted'
        if current != expected:
            return 'advanced-preserved'
        raise CleanupError('Expected branch still exists after rejected deletion; check permissions or repository rules')


class API:
    def __init__(self, repository, token):
        self.base = 'https://api.github.com/repos/' + repository
        self.token = token

    def get(self, endpoint):
        request = urllib.request.Request(self.base + endpoint, headers={
            'Authorization': 'Bearer ' + self.token, 'Accept': 'application/vnd.github+json',
            'X-GitHub-Api-Version': '2022-11-28', 'User-Agent': 'bareplane-merged-branch-cleanup',
        })
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                data = response.read(8 * 1024 * 1024 + 1)
            if len(data) > 8 * 1024 * 1024:
                raise CleanupError('GitHub response exceeded the cleanup limit')
            return json.loads(data)
        except (urllib.error.URLError, OSError, ValueError):
            raise CleanupError('GitHub cleanup inspection failed; no API response or credentials are logged') from None

    def pages(self, endpoint, **parameters):
        for page in range(1, 101):
            result = self.get(endpoint + '?' + urllib.parse.urlencode(dict(parameters, per_page=100, page=page)))
            if not isinstance(result, list):
                raise CleanupError('GitHub returned an unexpected cleanup collection')
            yield from result
            if len(result) < 100:
                return
        raise CleanupError('Cleanup inspection exceeded the pagination limit')


def candidates(pulls, repository, default, heads, excluded):
    result = set()
    blocked = excluded | {default, 'main', 'master'}
    for pull in pulls:
        head, base = pull.get('head') or {}, pull.get('base') or {}
        name, sha = head.get('ref'), head.get('sha')
        if not pull.get('merged_at') or (head.get('repo') or {}).get('full_name') != repository or (base.get('repo') or {}).get('full_name') != repository:
            continue
        if base.get('ref') != default or not isinstance(name, str) or name in blocked:
            continue
        ref = 'refs/heads/' + name
        if heads.get(ref) == sha and isinstance(sha, str) and re.fullmatch(r'[0-9a-f]{40}', sha):
            result.add((ref, sha))
    return sorted(result)


def cleanup(api, git, repository, report=None):
    default = api.get('')['default_branch']
    if not isinstance(default, str) or not default:
        raise CleanupError('Repository default branch is unavailable')
    excluded = set()
    for branch in api.pages('/branches', protected='true'):
        if not isinstance(branch.get('name'), str) or not branch['name']:
            raise CleanupError('Protected branch metadata is unavailable')
        excluded.add(branch['name'])
    for pull in api.pages('/pulls', state='open'):
        head = pull.get('head') or {}
        if (head.get('repo') or {}).get('full_name') == repository:
            if not isinstance(head.get('ref'), str) or not head['ref']:
                raise CleanupError('Active pull request branch metadata is unavailable')
            excluded.add(head['ref'])
    heads = git.heads()
    selected = candidates(api.pages('/pulls', state='closed'), repository, default, heads, excluded)
    results = []
    for ref, expected in selected:
        result = {'ref': ref, 'result': git.delete(ref, expected)}
        results.append(result)
        if report is not None:
            report(result)  # Keep an audit trail even if a later ref fails.
    return results


def main():
    repository, token = os.environ.get('GITHUB_REPOSITORY', ''), os.environ.get('GH_TOKEN', '')
    if os.environ.get('GITHUB_ACTIONS') != 'true' or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}', repository):
        raise CleanupError('This cleanup command requires a GitHub Actions repository context')
    if repository.split('/')[1] in {'.', '..'} or not token or any(c.isspace() for c in token):
        raise CleanupError('Repository cleanup authentication is unavailable')
    # Do not use checked-out Git configuration, hooks, credentials, or remotes.
    with tempfile.TemporaryDirectory(prefix='bareplane-cleanup-') as directory:
        git = Git(directory, 'https://github.com/' + repository + '.git', token)
        results = cleanup(API(repository, token), git, repository, report=lambda result: print(json.dumps(result), flush=True))
        if not results:
            print(json.dumps({'result': 'no-eligible-branches'}))


if __name__ == '__main__':
    try:
        main()
    except (CleanupError, KeyError, TypeError, AttributeError, UnicodeError) as error:
        message = str(error) if isinstance(error, CleanupError) else 'Unexpected cleanup metadata; refusing unsafe deletion'
        print(message, file=sys.stderr)
        raise SystemExit(1)
