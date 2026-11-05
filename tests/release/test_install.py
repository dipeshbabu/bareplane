"""Explicit install identity, unmanaged preservation and checksum refusals."""

import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'hack'))
import install_release as installer
import homebrew_formula
import test_artifacts
from test_artifacts import FakeBuild, SOURCE, VERSION, release


class InstallTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='bareplane-install-test-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        build = FakeBuild(self.root)
        self.output = self.root / 'release'
        self.manifest = release.build_release(build.root, self.output, VERSION, SOURCE, build.run)
        self.prefix = self.root / 'prefix'

    def test_download_uses_only_exact_version_urls_and_trusted_manifest_digest(self):
        calls = []
        def fetch(url, limit):
            calls.append(url)
            data = (self.output / url.rsplit('/', 1)[1]).read_bytes()
            self.assertLessEqual(len(data), limit)
            return data
        stage = self.root / 'download'
        stage.mkdir()
        digest = release.digest((self.output / ('bareplane_' + VERSION + '_manifest.json')).read_bytes())
        result = installer.download_release(VERSION, digest, stage, fetch)
        self.assertEqual(result, self.manifest)
        self.assertEqual(len(calls), 8)
        self.assertTrue(all(url.startswith('https://github.com/' + release.REPOSITORY + '/releases/download/v' + VERSION + '/') for url in calls))

    def test_changed_manifest_or_unsafe_version_stops_before_archive_download(self):
        stage = self.root / 'download'
        stage.mkdir()
        calls = []
        def fetch(url, _):
            calls.append(url)
            return b'changed'
        for version in [VERSION, 'latest', '../escape', '1.2.3; command']:
            calls.clear()
            with self.subTest(version=version), self.assertRaises(installer.ReleaseError):
                installer.download_release(version, '0' * 64, stage, fetch)
            self.assertLessEqual(len(calls), 1)
            self.assertEqual(list(stage.iterdir()), [])

    def test_formula_has_four_exact_release_urls_and_validated_digests(self):
        rendered = homebrew_formula.formula(installer.verify_release(self.output))
        self.assertEqual(rendered.count('      url '), 4)
        self.assertEqual(rendered.count('      sha256 '), 4)
        for item in self.manifest['artifacts']:
            self.assertIn('/releases/download/v' + VERSION + '/' + item['filename'], rendered)
            self.assertIn(item['sha256'], rendered)
        self.assertIn('on_macos do', rendered)
        self.assertIn('on_linux do', rendered)
        self.assertNotIn('depends_on "go"', rendered)

    @unittest.skipIf(os.name == 'nt', 'Installation is supported inside WSL/Linux/macOS')
    def test_install_is_managed_and_preserves_unrelated_prefix_files(self):
        self.prefix.mkdir()
        sentinel = self.prefix / 'operator-file'
        sentinel.write_bytes(b'preserve')
        result = installer.install(self.output, self.prefix, target=('linux', 'amd64'))
        link = self.prefix / 'bin/bareplane'
        self.assertTrue(link.is_symlink())
        self.assertEqual(link.resolve().parent.name, VERSION + '-' + SOURCE)
        self.assertEqual(link.stat().st_mode & 0o777, 0o755)
        self.assertEqual(link.resolve().parent.stat().st_mode & 0o777, 0o755)
        self.assertTrue(result['installed'])
        self.assertEqual(sentinel.read_bytes(), b'preserve')
        with self.assertRaisesRegex(installer.ReleaseError, 'upgrade'):
            installer.install(self.output, self.prefix, target=('linux', 'amd64'))

    def test_regular_unmanaged_binary_is_never_overwritten(self):
        binary = self.prefix / 'bin/bareplane'
        binary.parent.mkdir(parents=True)
        binary.write_bytes(b'operator-binary')
        with self.assertRaisesRegex(installer.ReleaseError, 'unmanaged'):
            installer.install(self.output, self.prefix, upgrade=True, target=('linux', 'amd64'))
        self.assertEqual(binary.read_bytes(), b'operator-binary')
        self.assertFalse((self.prefix / 'lib').exists())

    @unittest.skipIf(os.name == 'nt', 'POSIX ownership boundary')
    def test_foreign_binary_link_and_redirected_store_are_preserved(self):
        binary = self.prefix / 'bin/bareplane'
        binary.parent.mkdir(parents=True)
        foreign = self.root / 'operator-binary'
        foreign.write_bytes(b'preserve')
        binary.symlink_to(foreign)
        with self.assertRaisesRegex(installer.ReleaseError, 'unmanaged'):
            installer.install(self.output, self.prefix, upgrade=True, target=('linux', 'amd64'))
        self.assertEqual(foreign.read_bytes(), b'preserve')
        binary.unlink()
        (self.prefix / 'lib').symlink_to(self.root, target_is_directory=True)
        with self.assertRaisesRegex(installer.ReleaseError, 'redirected'):
            installer.install(self.output, self.prefix, target=('linux', 'amd64'))

    @unittest.skipIf(os.name == 'nt', 'POSIX atomic install')
    def test_atomic_activation_collision_preserves_foreign_destination(self):
        original = os.link
        def collide(source, destination, **kwargs):
            Path(destination).write_bytes(b'concurrent-operator-file')
            return original(source, destination, **kwargs)
        with patch.object(installer.os, 'link', side_effect=collide), self.assertRaises(FileExistsError):
            installer.install(self.output, self.prefix, target=('linux', 'amd64'))
        self.assertEqual((self.prefix / 'bin/bareplane').read_bytes(), b'concurrent-operator-file')
        self.assertFalse(list((self.prefix / 'bin').glob('.bareplane-*')))

    @unittest.skipIf(os.name == 'nt', 'POSIX managed upgrade')
    def test_modified_managed_binary_blocks_upgrade_without_losing_old_bytes(self):
        installer.install(self.output, self.prefix, target=('linux', 'amd64'))
        binary = self.prefix / 'bin/bareplane'
        binary.write_bytes(b'operator-modified')
        with self.assertRaisesRegex(installer.ReleaseError, 'changed'):
            installer.install(self.output, self.prefix, upgrade=True, target=('linux', 'amd64'))
        self.assertEqual(binary.read_bytes(), b'operator-modified')

    @unittest.skipIf(os.name == 'nt', 'POSIX managed upgrade')
    def test_explicit_upgrade_switches_atomically_and_preserves_previous_version(self):
        installer.install(self.output, self.prefix, target=('linux', 'amd64'))
        link = self.prefix / 'bin/bareplane'
        previous = link.resolve()
        second = self.root / 'second'
        second.mkdir()
        build = FakeBuild(second)
        output = second / 'release'
        with patch.object(test_artifacts, 'VERSION', '0.1.0-rc.2'):
            release.build_release(build.root, output, '0.1.0-rc.2', SOURCE, build.run)
        installer.install(output, self.prefix, upgrade=True, target=('linux', 'amd64'))
        self.assertNotEqual(link.resolve(), previous)
        self.assertEqual(link.resolve().parent.name, '0.1.0-rc.2-' + SOURCE)
        self.assertTrue(previous.is_file())
        self.assertTrue((previous.parent / 'installation.json').is_file())


if __name__ == '__main__':
    unittest.main()
