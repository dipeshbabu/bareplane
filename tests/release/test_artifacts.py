"""Artifact identity, archive safety, source binding and reproducibility."""

import copy
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('release_artifacts', ROOT / 'hack/release_artifacts.py')
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)
SOURCE = 'a' * 40
VERSION = '0.1.0-rc.1'
EPOCH = 1700000000
CREATED = '2023-11-14T22:13:20Z'


def executable(target_os, arch):
    data = bytearray(128)
    if target_os == 'linux':
        data[:6] = b'\x7fELF\x02\x01'
        data[18:20] = {'amd64': 62, 'arm64': 183}[arch].to_bytes(2, 'little')
    else:
        data[:4] = b'\xcf\xfa\xed\xfe'
        data[4:8] = {'amd64': 0x1000007, 'arm64': 0x100000c}[arch].to_bytes(4, 'little')
    return bytes(data)


class FakeBuild:
    def __init__(self, directory):
        self.root = directory / 'source'
        self.root.mkdir()
        (self.root / 'LICENSE').write_text('Bareplane MIT license\n')
        self.goroot = directory / 'go'
        self.goroot.mkdir()
        (self.goroot / 'LICENSE').write_text('Go BSD license\n')
        self.dependency = directory / 'yaml'
        self.dependency.mkdir()
        (self.dependency / 'LICENSE').write_text('YAML complete dual license notice\n')
        self.binaries = {}
        self.builds = []
        self.dirty = False
        self.drift_after_build = None

    def run(self, args, root, environment=None):
        if args[:3] == ['git', 'rev-parse', 'HEAD']:
            return SOURCE + '\n'
        if args[:2] == ['git', 'status']:
            return ' M go.mod\n' if self.dirty else ''
        if args[:2] == ['git', 'show']:
            return str(EPOCH) + '\n'
        if args == ['go', 'env', 'GOVERSION']:
            return 'go1.27.3\n'
        if args == ['go', 'env', 'GOROOT']:
            return str(self.goroot) + '\n'
        if args[:3] == ['go', 'mod', 'download'] or args[:3] == ['go', 'mod', 'verify']:
            return ''
        if args[:3] == ['go', 'list', '-m']:
            return json.dumps(dict(Path=release.MODULE, Main=True, Dir=str(self.root))) + '\n' + json.dumps(
                dict(Path='gopkg.in/yaml.v3', Version='v3.0.1', Dir=str(self.dependency)))
        if args[:2] == ['go', 'build']:
            if environment.get('CGO_ENABLED') != '0' or environment.get('GOWORK') != 'off':
                raise AssertionError('Ambient compiler settings reached the build')
            flags = args[args.index('-ldflags') + 1]
            for value in [VERSION, SOURCE, CREATED, '-buildid=']:
                if value not in flags:
                    raise AssertionError('Missing fixed release metadata: ' + value)
            target = environment['GOOS'], environment['GOARCH']
            path = Path(args[args.index('-o') + 1])
            path.write_bytes(executable(*target))
            self.binaries[str(path)] = target
            self.builds.append(target)
            if len(self.builds) == self.drift_after_build:
                self.dirty = True
            return ''
        if args[:3] == ['go', 'version', '-m']:
            target_os, arch = self.binaries[args[3]]
            return (args[3] + ': go1.27.3\n\tpath\t' + release.MODULE + '/cmd/bareplane\n'
                    '\tdep\tgopkg.in/yaml.v3\tv3.0.1\th1:fixture\n'
                    '\tbuild\tCGO_ENABLED=0\n\tbuild\t-trimpath=true\n'
                    '\tbuild\tGOOS=' + target_os + '\n\tbuild\tGOARCH=' + arch + '\n')
        raise AssertionError('Unexpected command: ' + repr(args))


class ReleaseArtifactTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='bareplane-release-test-')
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.build = FakeBuild(self.directory)
        self.output = self.directory / 'release'

    def create(self):
        return release.build_release(self.build.root, self.output, VERSION, SOURCE, self.build.run)

    def rewrite_metadata(self, manifest):
        for item in manifest['artifacts']:
            data = (self.output / item['filename']).read_bytes()
            item.update(size=len(data), sha256=release.digest(data))
        inventory = release.canonical(release.sbom(VERSION, SOURCE, CREATED, manifest['artifacts'],
            [dict(path='gopkg.in/yaml.v3', version='v3.0.1')], manifest['goVersion']))
        (self.output / manifest['sbom']['filename']).write_bytes(inventory)
        manifest['sbom'].update(size=len(inventory), sha256=release.digest(inventory))
        (self.output / ('bareplane_' + VERSION + '_manifest.json')).write_bytes(release.canonical(manifest))
        text = ''.join(release.digest(path.read_bytes()) + '  ' + path.name + '\n'
                       for path in sorted(self.output.iterdir()) if path.name != 'SHA256SUMS')
        (self.output / 'SHA256SUMS').write_text(text)

    def test_complete_builds_are_reproducible_and_include_notices(self):
        manifest = self.create()
        first = {path.name: path.read_bytes() for path in self.output.iterdir()}
        second = self.directory / 'again'
        release.build_release(self.build.root, second, VERSION, SOURCE, self.build.run)
        self.assertEqual(first, {path.name: path.read_bytes() for path in second.iterdir()})
        self.assertEqual(release.verify_release(self.output), manifest)
        self.assertEqual(set(self.build.builds), set(release.TARGETS))
        inventory = json.loads((self.output / manifest['sbom']['filename']).read_bytes())
        self.assertIn('gopkg.in/yaml.v3', {item['name'] for item in inventory['packages']})
        self.assertNotIn('gopkg.in/check.v1', {item['name'] for item in inventory['packages']})
        for item in manifest['artifacts']:
            with tarfile.open(self.output / item['filename']) as archive:
                self.assertEqual([member.name for member in archive], ['bareplane', 'LICENSE', 'THIRD_PARTY_NOTICES.txt'])
                self.assertIn(b'YAML complete dual license notice', archive.extractfile('THIRD_PARTY_NOTICES.txt').read())

    def test_unsafe_versions_and_mismatched_or_dirty_source_stop_before_build(self):
        for version in ['latest', 'v1.2.3', '01.2.3', '1.2', '1.2.3-rc.01', '../1.2.3', '1.2.3;touch marker']:
            with self.subTest(version=version), self.assertRaises(release.ReleaseError):
                release.build_release(self.build.root, self.output, version, SOURCE, self.build.run)
        for source in ['b' * 40, 'a' * 39, 'A' * 40]:
            with self.subTest(source=source), self.assertRaises(release.ReleaseError):
                release.build_release(self.build.root, self.output, VERSION, source, self.build.run)
        self.build.dirty = True
        with self.assertRaises(release.ReleaseError):
            self.create()
        self.assertEqual(self.build.builds, [])
        self.assertFalse(self.output.exists())

    def test_source_drift_prevents_manifest_and_existing_output_is_preserved(self):
        self.build.drift_after_build = 1
        with self.assertRaisesRegex(release.ReleaseError, 'clean'):
            self.create()
        self.assertFalse(list(self.output.glob('*manifest.json')))
        with self.assertRaises(release.ReleaseError):
            release.verify_release(self.output)
        self.build.dirty = False
        self.build.drift_after_build = None
        sentinel = self.output / 'operator-file'
        sentinel.write_bytes(b'preserve')
        with self.assertRaisesRegex(release.ReleaseError, 'new directory'):
            self.create()
        self.assertEqual(sentinel.read_bytes(), b'preserve')

    def test_modified_or_extra_artifacts_cannot_verify(self):
        manifest = self.create()
        archive = self.output / manifest['artifacts'][0]['filename']
        original = archive.read_bytes()
        archive.write_bytes(original + b'changed')
        with self.assertRaises(release.ReleaseError):
            release.verify_release(self.output)
        archive.write_bytes(original)
        extra = self.output / 'extra.txt'
        extra.write_text('unreviewed')
        with self.assertRaisesRegex(release.ReleaseError, 'unexpected'):
            release.verify_release(self.output)

    def test_manifest_cannot_redirect_to_parent_or_duplicate_a_platform(self):
        original = self.create()
        path = self.output / ('bareplane_' + VERSION + '_manifest.json')
        for change in ['parent', 'duplicate', 'wrong-source']:
            manifest = copy.deepcopy(original)
            if change == 'parent':
                manifest['artifacts'][0]['filename'] = '../secret'
            elif change == 'duplicate':
                manifest['artifacts'][0] = copy.deepcopy(manifest['artifacts'][1])
            else:
                manifest['sourceCommit'] = 'not-a-source'
            path.write_bytes(release.canonical(manifest))
            with self.subTest(change=change), self.assertRaises(release.ReleaseError):
                release.verify_release(self.output)

    def test_archive_links_wrong_architecture_and_traversal_are_rejected(self):
        manifest = self.create()
        first = manifest['artifacts'][0]
        path = self.output / first['filename']
        original = path.read_bytes()
        for kind in ['link', 'traversal', 'architecture']:
            if kind == 'architecture':
                data = release.archive_bytes(executable('darwin', 'arm64'), b'license', b'notice', EPOCH)
            else:
                buffer = io.BytesIO()
                with tarfile.open(fileobj=buffer, mode='w:gz') as archive:
                    member = tarfile.TarInfo('bareplane' if kind == 'link' else '../bareplane')
                    member.mode, member.mtime = 0o755, EPOCH
                    member.type, member.linkname = tarfile.SYMTYPE, '/unrelated'
                    archive.addfile(member)
                data = buffer.getvalue()
            path.write_bytes(data)
            self.rewrite_metadata(manifest)
            with self.subTest(kind=kind), self.assertRaises(release.ReleaseError):
                release.verify_release(self.output)
        path.write_bytes(original)

    def test_ambient_execution_overrides_and_credentials_are_not_forwarded(self):
        environment = release.build_environment(dict(PATH='/bin', HOME='/operator', GOCACHE='/cache', LOCALAPPDATA='/local-data',
            GOFLAGS='-overlay=unreviewed.json', GOTOOLCHAIN='auto', GOOS='hostile', GOWORK='/unreviewed',
            BAREPLANE_PROXMOX_TOKEN_SECRET='SECRET', GITHUB_TOKEN='SECRET', TF_CLI_ARGS='-destroy', PYTHONPATH='/unreviewed'))
        self.assertEqual(environment['GOFLAGS'], '')
        self.assertEqual(environment['GOWORK'], 'off')
        self.assertEqual(environment['GOTOOLCHAIN'], 'local')
        self.assertEqual(environment['LOCALAPPDATA'], '/local-data')
        self.assertNotIn('GOOS', environment)
        self.assertNotIn('SECRET', str(environment))
        self.assertNotIn('PYTHONPATH', environment)

    def test_linked_module_replacements_are_refused(self):
        data = ('/binary: go1.27.3\n\tpath\t' + release.MODULE + '/cmd/bareplane\n'
                '\tdep\texample.com/module\tv1.0.0\n\t=>\t../local\n')
        with self.assertRaisesRegex(release.ReleaseError, 'replaced'):
            release.parse_build_info(data)


if __name__ == '__main__':
    unittest.main()
