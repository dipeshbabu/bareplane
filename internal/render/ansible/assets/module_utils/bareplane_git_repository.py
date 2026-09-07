"""Bounded, credential-free HTTPS Git inspection; no checkout or hooks."""

import hashlib
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import tempfile
import time
import urllib.parse

import yaml


class GitOpsError(ValueError):
    pass


class UniqueLoader(yaml.SafeLoader):
    def construct_mapping(self, node, deep=False):
        keys = [self.construct_object(key, deep=deep) for key, _ in node.value]
        if len(set(keys)) != len(keys):
            raise GitOpsError('Git root kustomization has duplicate YAML keys')
        return super().construct_mapping(node, deep=deep)


def require(condition, message):
    if not condition:
        raise GitOpsError(message)


class Commands:
    """Keep untrusted output off pipes and bound process time/output/filesize.

    Each child owns a process group, so timeout/cancellation also stops Git's
    helpers. Errors deliberately exclude command output and environment values.
    """

    def __init__(self, directory, timeout=900):
        self.directory = str(directory)
        self.deadline = time.monotonic() + timeout

    def run(self, argv, data=b'', timeout=60, limit=8 * 1024 * 1024, git=False):
        import resource

        remaining = min(timeout, self.deadline - time.monotonic())
        require(remaining > 0, 'GitOps operation deadline was exceeded')
        env = {key: value for key, value in os.environ.items() if key in {'PATH', 'LANG', 'LC_ALL', 'LC_CTYPE'}}
        env.update(HOME=self.directory, TMPDIR=self.directory, GIT_CONFIG_NOSYSTEM='1',
                   GIT_CONFIG_GLOBAL='/dev/null', GIT_CONFIG_SYSTEM='/dev/null', GIT_TERMINAL_PROMPT='0',
                   GIT_ASKPASS='/bin/false', SSH_ASKPASS='/bin/false', GIT_LFS_SKIP_SMUDGE='1', KUBECTL_KUBERC='false')

        def limits():
            # A shallow pack is also bounded; untrusted servers cannot write an
            # unlimited pack before we get a chance to inspect the repository.
            maximum = 64 * 1024 * 1024 if git else limit
            resource.setrlimit(resource.RLIMIT_FSIZE, (maximum, maximum))

        with tempfile.TemporaryFile(dir=self.directory) as output:
            process = None
            try:
                process = subprocess.Popen(argv, stdin=subprocess.PIPE, stdout=output, stderr=subprocess.DEVNULL,
                                           cwd=self.directory, env=env, start_new_session=True, preexec_fn=limits)
                process.communicate(data, timeout=remaining)
                require(process.returncode == 0, 'A bounded GitOps command failed; inspect the named prerequisite privately')
                output.seek(0)
                result = output.read(limit + 1)
                require(len(result) <= limit, 'GitOps command output exceeded the allowed size')
                return result
            except (OSError, subprocess.SubprocessError):
                raise GitOpsError('A GitOps command could not start or exceeded its deadline') from None
            finally:
                if process is not None:
                    # Also reap a helper left behind after its group leader exits.
                    try:
                        os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    process.communicate(timeout=5)


def validate_contract(repository, revision, root):
    try:
        parsed = urllib.parse.urlsplit(repository)
        port = parsed.port
    except ValueError:
        raise GitOpsError('Invalid public Git repository contract') from None
    require(len(repository) <= 2048 and parsed.scheme == 'https' and parsed.hostname and '@' not in parsed.netloc
            and not parsed.query and not parsed.fragment and not any(c in repository for c in '\\?#\r\n\t ')
            and parsed.path.startswith('/') and parsed.path != '/' and not parsed.path.endswith('/')
            and (port is None or 1 <= port <= 65535), 'Only credential-free canonical HTTPS Git repositories are supported')
    require(all(re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}', p) for p in parsed.path[1:].split('/')),
            'Git repository path is not portable')
    require(re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_./-]{0,199}', revision) and '..' not in revision
            and '//' not in revision and not revision.endswith(('/', '.'))
            and all(not p.startswith('.') and not p.lower().endswith('.lock') for p in revision.split('/')),
            'Git revision must be an explicit safe branch, tag, or commit')
    require(len(root) <= 512 and all(re.fullmatch(r'[a-z0-9][a-z0-9_.-]{0,127}', p)
            and p not in {'.', '..', 'terraform', 'state', 'node_modules'} and not p.endswith('.') for p in root.split('/')),
            'Git root path must be a safe repository-relative directory')


def inspect_repository(commands, repository, revision, root, expected_files=None):
    validate_contract(repository, revision, root)
    temporary = Path(tempfile.mkdtemp(prefix='repository-', dir=commands.directory))
    try:
        base = ['git', '-c', 'credential.helper=', '-c', 'core.hooksPath=/dev/null',
                '-c', 'core.fsmonitor=false', '-c', 'protocol.allow=never', '-c', 'protocol.https.allow=always',
                '-c', 'http.sslVerify=true', '-c', 'http.followRedirects=false', '-c', 'http.proxy=',
                '-c', 'fetch.fsckObjects=true', '-c', 'transfer.fsckObjects=true',
                '-c', 'submodule.recurse=false', '-c', 'gc.auto=0', '-c', 'maintenance.auto=false']
        commands.run(base + ['init', '--bare', '--template=', str(temporary)], git=True)
        base += ['--git-dir', str(temporary)]
        commands.run(base + ['fetch', '--no-tags', '--depth=1', '--no-recurse-submodules',
                             repository, revision], timeout=120, git=True)
        size = sum(p.stat().st_size for p in temporary.rglob('*') if p.is_file())
        require(size <= 64 * 1024 * 1024, 'Public Git repository exceeds the bootstrap inspection size limit')
        commit = commands.run(base + ['rev-parse', '--verify', 'FETCH_HEAD^{commit}'], git=True).decode().strip()
        require(re.fullmatch(r'[0-9a-f]{40}', commit), 'Git did not resolve a supported immutable commit')
        directory = commands.run(base + ['ls-tree', '-z', commit, '--', root], git=True).decode()
        require(directory.startswith('040000 tree ') and directory.endswith('\t' + root + '\0') and directory.count('\0') == 1,
                'Configured Git root is missing, redirected, or not a directory')
        filename = root + '/kustomization.yaml'
        entry = commands.run(base + ['ls-tree', '-z', commit, '--', filename], git=True).decode()
        require(entry.startswith('100644 blob ') and entry.endswith('\t' + filename + '\0') and entry.count('\0') == 1,
                'Configured Git root must contain a regular kustomization.yaml')
        length = commands.run(base + ['cat-file', '-s', commit + ':' + filename], git=True).decode().strip()
        require(length.isdigit() and 0 < int(length) <= 65536, 'Git root kustomization is empty or oversized')
        content = commands.run(base + ['cat-file', 'blob', commit + ':' + filename], limit=65536, git=True)
        require(not any(isinstance(t, (yaml.tokens.AliasToken, yaml.tokens.AnchorToken, yaml.tokens.TagToken)) for t in yaml.scan(content)),
                'Git root kustomization must not use YAML aliases, anchors, or custom tags')
        document = yaml.load(content, Loader=UniqueLoader)
        require(isinstance(document, dict) and document.get('apiVersion') == 'kustomize.config.k8s.io/v1beta1'
                and document.get('kind') == 'Kustomization' and isinstance(document.get('resources'), list)
                and document['resources'], 'Git root is not a nonempty declarative Kustomization')
        require(all(isinstance(item, str) and item and all(re.fullmatch(r'[a-z0-9][a-z0-9_.-]*', part)
                    and part not in {'.', '..'} for part in item.split('/')) for item in document['resources']),
                'Git root resources must be portable local repository paths')
        if expected_files is not None:
            require(isinstance(expected_files, dict) and 0 < len(expected_files) <= 4096,
                    'Expected GitOps payload inventory is invalid')
            directories = {root}
            for name, expected in sorted(expected_files.items()):
                require(isinstance(name, str) and all(re.fullmatch(r'[a-z0-9][a-z0-9_.-]*', p) and p not in {'.', '..'} for p in name.split('/')),
                        'Expected GitOps payload path is invalid')
                require(isinstance(expected, bytes) and len(expected) <= 4 * 1024 * 1024, 'Expected GitOps payload is oversized')
                if name.startswith('components/'):
                    require(len(name.split('/')) >= 3, 'Component payload needs an explicit component directory')
                    directories.add('/'.join(name.split('/')[:2]))
                entry = commands.run(base + ['ls-tree', '-z', commit, '--', name], git=True).decode()
                require(entry.startswith('100644 blob ') and entry.endswith('\t' + name + '\0') and entry.count('\0') == 1,
                        'Published GitOps payload is missing, executable, or redirected: ' + name)
                length = commands.run(base + ['cat-file', '-s', commit + ':' + name], git=True).decode().strip()
                require(length.isdigit() and int(length) == len(expected), 'Published GitOps payload differs from the approved render: ' + name)
                actual = commands.run(base + ['cat-file', 'blob', commit + ':' + name], limit=max(1, len(expected)), git=True)
                require(hashlib.sha256(actual).digest() == hashlib.sha256(expected).digest(),
                        'Published GitOps payload differs from the approved render: ' + name)
            for directory in sorted(directories):
                listed = commands.run(base + ['ls-tree', '-r', '--name-only', '-z', commit, '--', directory], limit=512 * 1024, git=True).decode()
                names = listed.split('\0')[:-1] if listed.endswith('\0') else []
                expected = {name for name in expected_files if name.startswith(directory + '/')}
                require(set(names) == expected and len(names) == len(expected),
                        'Published GitOps source directory contains unexpected or missing files: ' + directory)
        return commit
    except (UnicodeError, KeyError, TypeError, OSError, RecursionError, yaml.YAMLError):
        raise GitOpsError('Cannot safely inspect the configured public Git repository') from None
    finally:
        # This path is created by mkdtemp under the already-validated private
        # controller directory. Git never checks out repository-controlled files.
        shutil.rmtree(temporary)
