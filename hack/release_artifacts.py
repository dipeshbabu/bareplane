#!/usr/bin/env python3
"""Build and verify deterministic release artifacts; never publish anything."""

import argparse
import datetime as dt
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
from pathlib import PurePosixPath
import re
import shutil
import subprocess
import tarfile
from urllib.parse import quote
import zipfile


MODULE = 'github.com/dipeshbabu/bareplane'
REPOSITORY = 'dipeshbabu/bareplane'
TARGETS = (('linux', 'amd64'), ('linux', 'arm64'), ('darwin', 'amd64'), ('darwin', 'arm64'))
VERSION = re.compile(r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(?:alpha|beta|rc)\.(?:0|[1-9][0-9]*))?\Z')
SHA = re.compile(r'[0-9a-f]{40}\Z')
DIGEST = re.compile(r'[0-9a-f]{64}\Z')
MAX_TEXT = 2 * 1024 * 1024
MAX_BINARY = 128 * 1024 * 1024
MAX_ARCHIVE = 256 * 1024 * 1024
MAX_SOURCE = 128 * 1024 * 1024


class ReleaseError(Exception):
    pass


def require(condition, message):
    if not condition:
        raise ReleaseError(message)


def canonical(value):
    return (json.dumps(value, sort_keys=True, indent=2) + '\n').encode()


def digest(data):
    return hashlib.sha256(data).hexdigest()


def released_go(version):
    match = re.fullmatch(r'go1\.([0-9]+)\.([0-9]+)', version) if isinstance(version, str) else None
    return bool(match and int(match[1]) >= 23)


def command(args, root, environment=None, binary=False):
    result = subprocess.run(args, cwd=root, env=environment, capture_output=True, timeout=600)
    require(len(result.stdout) <= (MAX_SOURCE if binary else MAX_TEXT) and len(result.stderr) <= MAX_TEXT,
            'Build command output exceeded its bound')
    if result.returncode:
        raise ReleaseError(Path(args[0]).name + ' failed: ' + result.stderr.decode(errors='replace')[-4000:])
    return result.stdout if binary else result.stdout.decode('utf-8')


def build_environment(base=None):
    base = os.environ if base is None else base
    allowed = {'PATH', 'SystemRoot', 'SYSTEMROOT', 'WINDIR', 'TEMP', 'TMP', 'TMPDIR', 'HOME',
               'USER', 'USERPROFILE', 'HOMEDRIVE', 'HOMEPATH', 'LOCALAPPDATA', 'APPDATA',
               'GOCACHE', 'GOMODCACHE', 'GOPATH'}
    environment = {key: value for key, value in base.items() if key in allowed}
    environment.update(CGO_ENABLED='0', GOWORK='off', GOENV='off', GOFLAGS='', GOTOOLCHAIN='local',
                       GOAMD64='v1', GOARM64='v8.0',
                       GOPROXY='https://proxy.golang.org', GOSUMDB='sum.golang.org', GOVCS='*:off')
    return environment


def check_source(root, source, run=command):
    require(SHA.fullmatch(source) is not None, 'Source must be an exact lowercase 40-character commit')
    require(run(['git', 'rev-parse', 'HEAD'], root).strip() == source, 'Source commit does not match this checkout')
    require(not run(['git', 'status', '--porcelain', '--untracked-files=all'], root).strip(), 'Release source must be clean')
    epoch = run(['git', 'show', '-s', '--format=%ct', source], root).strip()
    require(epoch.isdigit() and 0 < int(epoch) < 2**32, 'Source timestamp is not a bounded Git commit timestamp')
    return int(epoch)


def snapshot_source(root, source, destination, run=command):
    data = run(['git', 'archive', '--format=tar', source], root, None, True)
    require(isinstance(data, bytes) and len(data) <= MAX_SOURCE, 'Source snapshot is missing or oversized')
    destination.mkdir(mode=0o700)
    names = set()
    with tarfile.open(fileobj=io.BytesIO(data), mode='r:') as archive:
        for index, member in enumerate(archive):
            path = PurePosixPath(member.name)
            require(index < 10000 and not path.is_absolute() and '..' not in path.parts
                    and not any(character in member.name for character in ('\\', ':', '\x00'))
                    and len(member.name) <= 1024 and (member.isfile() or member.isdir()),
                    'Source snapshot contains unsafe paths, links or an excessive inventory')
            identity = str(path).casefold()
            require(identity not in names, 'Source snapshot has colliding portable paths')
            names.add(identity)
            target = destination.joinpath(*path.parts)
            require(target.resolve().is_relative_to(destination.resolve()), 'Source snapshot path escaped its owned directory')
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                require(0 <= member.size <= 16 * 1024 * 1024, 'Source file exceeded its size bound')
                require(not target.exists(), 'Source snapshot would overwrite another path')
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(archive.extractfile(member).read(member.size + 1))
                target.chmod(member.mode & 0o755)
    require((destination / 'go.mod').is_file(), 'Source snapshot has no module contract')
    return {path.relative_to(destination).as_posix(): digest(path.read_bytes())
            for path in destination.rglob('*') if path.is_file()}


def check_snapshot(root, expected):
    current = {}
    for path in root.rglob('*'):
        require(not path.is_symlink(), 'Normalized build source acquired a redirected path')
        if path.is_file():
            current[path.relative_to(root).as_posix()] = digest(path.read_bytes())
    require(current == expected, 'Normalized build source changed during compilation')


def parse_json_stream(data):
    values, decoder = [], json.JSONDecoder()
    remaining = data.lstrip()
    while remaining:
        value, end = decoder.raw_decode(remaining)
        values.append(value)
        remaining = remaining[end:].lstrip()
    return values


def parse_build_info(data):
    lines = data.splitlines()
    require(bool(lines) and ': go' in lines[0], 'Go build information is missing')
    toolchain = lines[0].split(': ', 1)[1].split()[0]
    modules, settings, package = [], {}, ''
    for line in lines[1:]:
        parts = line.strip().split('\t')
        if parts[0] == '=>':
            raise ReleaseError('Release binaries cannot contain local or replaced dependencies')
        if parts[0] == 'path' and len(parts) == 2:
            package = parts[1]
        elif parts[0] == 'dep' and len(parts) >= 3:
            require(parts[2].startswith('v') and parts[2] != '(devel)', 'Dependency version is not pinned')
            modules.append(dict(path=parts[1], version=parts[2]))
        elif parts[0] == 'build' and len(parts) == 2:
            key, separator, value = parts[1].partition('=')
            if separator:
                settings[key] = value
    require(package == MODULE + '/cmd/bareplane', 'Binary has an unexpected main package')
    return dict(toolchain=toolchain, modules=sorted(modules, key=lambda value: value['path']), settings=settings)


def text_file(path):
    require(path.is_file() and not path.is_symlink() and path.stat().st_size <= MAX_TEXT, 'License input is missing, redirected or oversized')
    return path.read_text(encoding='utf-8').replace('\r\n', '\n').rstrip() + '\n'


def third_party_notices(root, goroot, modules, module_locations):
    sections = [('Go standard library', text_file(goroot / 'LICENSE'))]
    for module in modules:
        location = module_locations.get((module['path'], module['version']))
        require(location is not None, 'A linked module has no verified license location')
        candidates = [Path(location) / name for name in ('LICENSE', 'LICENSE.txt', 'COPYING')]
        license_path = next((path for path in candidates if path.is_file()), None)
        require(license_path is not None, 'A linked module is missing its license notice')
        sections.append((module['path'] + '@' + module['version'], text_file(license_path)))
    assets = root / 'internal/render/gitops/assets'
    for path in sorted(assets.rglob('*')):
        if path.is_file() and ('license' in path.name.lower() or 'notice' in path.name.lower()):
            sections.append((path.relative_to(root).as_posix(), text_file(path)))
    return ('Third-party notices distributed with Bareplane\n\n' + '\n'.join(
        '===== ' + name + ' =====\n\n' + text for name, text in sections)).encode()


def archive_bytes(binary, license_text, notices, epoch):
    contents = {'bareplane': (binary, 0o755), 'LICENSE': (license_text, 0o644),
                'THIRD_PARTY_NOTICES.txt': (notices, 0o644)}
    output = io.BytesIO()
    with gzip.GzipFile(fileobj=output, mode='wb', filename='', mtime=epoch, compresslevel=9) as compressed:
        with tarfile.open(fileobj=compressed, mode='w', format=tarfile.USTAR_FORMAT) as archive:
            for name, (data, mode) in contents.items():
                member = tarfile.TarInfo(name)
                member.size, member.mode, member.mtime = len(data), mode, epoch
                member.uid = member.gid = 0
                member.uname = member.gname = ''
                archive.addfile(member, io.BytesIO(data))
    return output.getvalue()


def binary_target(data):
    require(len(data) >= 32, 'Release executable header is truncated')
    if data[:6] == b'\x7fELF\x02\x01':
        machine = int.from_bytes(data[18:20], 'little')
        require(machine in {62, 183}, 'ELF executable has an unsupported architecture')
        return 'linux', {62: 'amd64', 183: 'arm64'}[machine]
    if data[:4] == b'\xcf\xfa\xed\xfe':
        machine = int.from_bytes(data[4:8], 'little')
        require(machine in {0x1000007, 0x100000c}, 'Mach-O executable has an unsupported architecture')
        return 'darwin', {0x1000007: 'amd64', 0x100000c: 'arm64'}[machine]
    raise ReleaseError('Release executable is not a supported ELF or Mach-O binary')


def installer_zip(root, version, source):
    buffer = io.BytesIO()
    files = {'__main__.py': ('from install_release import cli\ncli(' + repr(version) + ', ' + repr(source) + ')\n').encode(),
             'install_release.py': (root / 'hack/install_release.py').read_bytes(),
             'release_artifacts.py': (root / 'hack/release_artifacts.py').read_bytes(),
             'LICENSE': (root / 'LICENSE').read_bytes()}
    with zipfile.ZipFile(buffer, 'w', compression=zipfile.ZIP_STORED) as archive:
        for name, data in sorted(files.items()):
            entry = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            entry.create_system = 3
            entry.external_attr = 0o100644 << 16
            archive.writestr(entry, data)
    return buffer.getvalue()


def sbom(version, source, created, records, modules, toolchain, installer=None):
    packages, relationships = [], []
    dependencies = [('SPDXRef-GoStandardLibrary', 'Go standard library', toolchain.removeprefix('go'), None)]
    for module in modules:
        identifier = 'SPDXRef-GoModule-' + digest(module['path'].encode())[:16]
        purl = 'pkg:golang/' + quote(module['path'], safe='/') + '@' + quote(module['version'], safe='')
        dependencies.append((identifier, module['path'], module['version'], purl))
    for identifier, name, dependency_version, purl in dependencies:
        item = dict(SPDXID=identifier, name=name, versionInfo=dependency_version, downloadLocation='NOASSERTION',
                    filesAnalyzed=False, licenseDeclared='NOASSERTION', licenseConcluded='NOASSERTION', copyrightText='NOASSERTION')
        if purl:
            item['externalRefs'] = [dict(referenceCategory='PACKAGE-MANAGER', referenceType='purl', referenceLocator=purl)]
        packages.append(item)
    for record in records:
        identifier = 'SPDXRef-Bareplane-' + record['os'] + '-' + record['arch']
        packages.append(dict(SPDXID=identifier, name='bareplane-' + record['os'] + '-' + record['arch'],
            versionInfo=version, packageFileName=record['filename'], filesAnalyzed=False,
            downloadLocation='https://github.com/' + REPOSITORY + '/releases/download/v' + version + '/' + record['filename'],
            licenseDeclared='MIT', licenseConcluded='NOASSERTION', copyrightText='NOASSERTION',
            checksums=[dict(algorithm='SHA256', checksumValue=record['sha256'])]))
        relationships.append(dict(spdxElementId='SPDXRef-DOCUMENT', relationshipType='DESCRIBES', relatedSpdxElement=identifier))
        for dependency, _, _, _ in dependencies:
            relationships.append(dict(spdxElementId=identifier, relationshipType='DEPENDS_ON', relatedSpdxElement=dependency))
    if installer:
        identifier = 'SPDXRef-Bareplane-Installer'
        packages.append(dict(SPDXID=identifier, name='bareplane-installer', versionInfo=version,
            packageFileName=installer['filename'], filesAnalyzed=False,
            downloadLocation='https://github.com/' + REPOSITORY + '/releases/download/v' + version + '/' + installer['filename'],
            licenseDeclared='MIT', licenseConcluded='NOASSERTION', copyrightText='NOASSERTION',
            checksums=[dict(algorithm='SHA256', checksumValue=installer['sha256'])]))
        relationships.append(dict(spdxElementId='SPDXRef-DOCUMENT', relationshipType='DESCRIBES', relatedSpdxElement=identifier))
    return dict(spdxVersion='SPDX-2.3', dataLicense='CC0-1.0', SPDXID='SPDXRef-DOCUMENT', name='bareplane-' + version,
        documentNamespace='https://github.com/' + REPOSITORY + '/sbom/' + source + '/' + version,
        creationInfo=dict(creators=['Tool: bareplane-release-builder'], created=created), packages=packages, relationships=relationships)


def build_release(root, output, version, source, run=command):
    root, output = Path(root).resolve(), Path(output).resolve()
    require(VERSION.fullmatch(version) is not None, 'Version must be MAJOR.MINOR.PATCH or an alpha/beta/rc numbered prerelease')
    epoch = check_source(root, source, run)
    environment = build_environment()
    toolchain = run(['go', 'env', 'GOVERSION'], root, environment).strip()
    require(released_go(toolchain), 'A released Go 1.23 or newer toolchain is required')
    goroot = Path(run(['go', 'env', 'GOROOT'], root, environment).strip())
    require(not output.exists(), 'Release output must be a new directory')
    output.mkdir(mode=0o700, parents=True)
    normalized = output / '.source'
    source_files = snapshot_source(root, source, normalized, run)
    modules = parse_json_stream(run(['go', 'list', '-m', '-json', 'all'], normalized, environment))
    require(all(not item.get('Replace') for item in modules), 'Release source cannot use module replacements')
    run(['go', 'mod', 'download'], normalized, environment)
    run(['go', 'mod', 'verify'], normalized, environment)
    module_locations = {(item['Path'], item.get('Version')): item.get('Dir')
                        for item in parse_json_stream(run(['go', 'list', '-m', '-json', 'all'], normalized, environment))}
    check_snapshot(normalized, source_files)
    staging = output / '.build'
    staging.mkdir(mode=0o700)
    created = dt.datetime.fromtimestamp(epoch, dt.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
    license_text = text_file(normalized / 'LICENSE').encode()
    records, linked_modules = [], None
    ldflags = '-s -w -buildid= ' + ' '.join('-X ' + MODULE + '/internal/version.' + name + '=' + value
                                         for name, value in [('Version', version), ('Commit', source), ('Date', created)])
    for target_os, target_arch in TARGETS:
        check_source(root, source, run)
        check_snapshot(normalized, source_files)
        target_env = dict(environment, GOOS=target_os, GOARCH=target_arch)
        binary = staging / (target_os + '-' + target_arch)
        run(['go', 'build', '-mod=readonly', '-trimpath', '-buildvcs=false', '-ldflags', ldflags,
             '-o', str(binary), './cmd/bareplane'], normalized, target_env)
        info = parse_build_info(run(['go', 'version', '-m', str(binary)], normalized, environment))
        require(info['toolchain'] == toolchain and info['settings'].get('CGO_ENABLED') == '0'
                and info['settings'].get('GOOS') == target_os and info['settings'].get('GOARCH') == target_arch
                and info['settings'].get('-trimpath') == 'true',
                'Binary toolchain or target differs from the requested build')
        if linked_modules is None:
            linked_modules = info['modules']
        require(info['modules'] == linked_modules, 'Platform builds have different linked module inventories')
        notices = third_party_notices(normalized, goroot, linked_modules, module_locations)
        binary_data = binary.read_bytes()
        require(binary_target(binary_data) == (target_os, target_arch), 'Executable header differs from its requested target')
        data = archive_bytes(binary_data, license_text, notices, epoch)
        name = 'bareplane_' + version + '_' + target_os + '_' + target_arch + '.tar.gz'
        (output / name).write_bytes(data)
        records.append(dict(os=target_os, arch=target_arch, filename=name, size=len(data), sha256=digest(data)))
        binary.unlink()
    staging.rmdir()
    check_source(root, source, run)
    check_snapshot(normalized, source_files)
    installer_name = 'bareplane_' + version + '_installer.pyz'
    installer_data = installer_zip(normalized, version, source)
    installer = dict(filename=installer_name, size=len(installer_data), sha256=digest(installer_data))
    (output / installer_name).write_bytes(installer_data)
    require(normalized.resolve().parent == output and normalized.name == '.source' and not normalized.is_symlink(),
            'Normalized source cleanup escaped its owned output directory')
    shutil.rmtree(normalized)
    sbom_name = 'bareplane_' + version + '.spdx.json'
    sbom_data = canonical(sbom(version, source, created, records, linked_modules, toolchain, installer))
    (output / sbom_name).write_bytes(sbom_data)
    manifest = dict(schemaVersion=2, name='bareplane', version=version, repository=REPOSITORY, sourceCommit=source,
                    sourceDate=created, goVersion=toolchain, artifacts=records,
                    installer=installer,
                    sbom=dict(filename=sbom_name, size=len(sbom_data), sha256=digest(sbom_data)))
    manifest_name = 'bareplane_' + version + '_manifest.json'
    (output / manifest_name).write_bytes(canonical(manifest))
    checksums = ''.join(digest(path.read_bytes()) + '  ' + path.name + '\n' for path in sorted(output.iterdir()))
    (output / 'SHA256SUMS').write_text(checksums, encoding='utf-8', newline='\n')
    verify_release(output)
    return manifest


def verify_release(directory):
    try:
        return _verify_release(directory)
    except (KeyError, TypeError, AttributeError, IndexError, ValueError, tarfile.TarError, zipfile.BadZipFile) as error:
        raise ReleaseError('Release manifest or archive has a malformed shape') from error


def _verify_release(directory):
    directory = Path(directory)
    require(directory.is_dir() and not directory.is_symlink(), 'Release input must be a regular directory')
    manifests = list(directory.glob('bareplane_*_manifest.json'))
    require(len(manifests) == 1, 'Exactly one release manifest is required')
    require(not manifests[0].is_symlink() and manifests[0].stat().st_size <= MAX_TEXT, 'Release manifest is redirected or oversized')
    manifest = json.loads(manifests[0].read_bytes())
    require(set(manifest) == {'schemaVersion', 'name', 'version', 'repository', 'sourceCommit', 'sourceDate', 'goVersion', 'artifacts', 'sbom', 'installer'}
            and type(manifest['schemaVersion']) is int and manifest['schemaVersion'] == 2
            and manifest['name'] == 'bareplane' and manifest['repository'] == REPOSITORY
            and VERSION.fullmatch(manifest['version']) and SHA.fullmatch(manifest['sourceCommit'])
            and released_go(manifest['goVersion']), 'Release identity is invalid')
    created = dt.datetime.strptime(manifest['sourceDate'], '%Y-%m-%dT%H:%M:%SZ').replace(tzinfo=dt.timezone.utc)
    epoch = int(created.timestamp())
    require(0 < epoch < 2**32, 'Release timestamp is invalid')
    version = manifest['version']
    require(manifests[0].name == 'bareplane_' + version + '_manifest.json', 'Manifest filename differs from its version')
    artifacts = manifest['artifacts']
    require(len(artifacts) == len(TARGETS) and {(item['os'], item['arch']) for item in artifacts} == set(TARGETS),
            'Release must contain every supported platform exactly once')
    expected = {manifests[0].name, 'SHA256SUMS'}
    for item in [*artifacts, manifest['sbom'], manifest['installer']]:
        require(set(item) == ({'os', 'arch', 'filename', 'size', 'sha256'} if 'os' in item else {'filename', 'size', 'sha256'}),
                'Release artifact fields are invalid')
        name = ('bareplane_' + version + '_' + item['os'] + '_' + item['arch'] + '.tar.gz' if 'os' in item
                else 'bareplane_' + version + ('_installer.pyz' if item is manifest['installer'] else '.spdx.json'))
        require(item['filename'] == name and DIGEST.fullmatch(item['sha256']) and type(item['size']) is int and item['size'] > 0,
                'Release artifact identity is invalid')
        path = directory / name
        require(path.is_file() and not path.is_symlink() and path.stat().st_size == item['size']
                and item['size'] <= (MAX_ARCHIVE if 'os' in item else MAX_TEXT), 'Release artifact is missing, redirected or oversized')
        require(digest(path.read_bytes()) == item['sha256'], 'Release artifact checksum differs from its manifest')
        expected.add(name)
        if 'os' in item:
            with tarfile.open(path, 'r|gz') as archive:
                names = ['bareplane', 'LICENSE', 'THIRD_PARTY_NOTICES.txt']
                count = 0
                for index, member in enumerate(archive):
                    require(index < 3 and member.name == names[index] and member.isfile()
                            and member.uid == 0 and member.gid == 0 and not member.uname and not member.gname
                            and member.mode == (0o755 if index == 0 else 0o644) and member.mtime == epoch
                            and 0 < member.size <= (MAX_BINARY if index == 0 else MAX_TEXT),
                            'Release archive has unexpected files, links, size, timestamp or permissions')
                    data = archive.extractfile(member).read(member.size + 1)
                    require(len(data) == member.size, 'Release archive member is truncated')
                    if index == 0:
                        require(binary_target(data) == (item['os'], item['arch']), 'Archive executable does not match its platform')
                    count += 1
                require(count == 3, 'Release archive is incomplete')
        if item is manifest['installer']:
            with zipfile.ZipFile(path) as archive:
                entries = archive.infolist()
                require([entry.filename for entry in entries] == ['LICENSE', '__main__.py', 'install_release.py', 'release_artifacts.py']
                        and all(entry.compress_type == zipfile.ZIP_STORED and 0 < entry.file_size <= MAX_TEXT
                                and entry.external_attr >> 16 == 0o100644 and entry.date_time == (1980, 1, 1, 0, 0, 0)
                                for entry in entries), 'Installer archive contains unexpected files, links or sizes')
                expected_entry = ('from install_release import cli\ncli(' + repr(version) + ', ' + repr(manifest['sourceCommit']) + ')\n').encode()
                require(archive.read('__main__.py') == expected_entry and archive.testzip() is None,
                        'Installer entry point differs from its reviewed source/version')
    require({path.name for path in directory.iterdir()} == expected, 'Release directory contains unexpected or partial output')
    checksums = ''.join(digest(path.read_bytes()) + '  ' + path.name + '\n'
                        for path in sorted(directory.iterdir()) if path.name != 'SHA256SUMS')
    checksum_file = directory / 'SHA256SUMS'
    require(checksum_file.is_file() and not checksum_file.is_symlink() and checksum_file.stat().st_size <= MAX_TEXT,
            'Release checksum list is missing, redirected or oversized')
    require(checksum_file.read_text(encoding='utf-8') == checksums, 'Release checksum list differs from the complete artifact set')
    inventory = json.loads((directory / manifest['sbom']['filename']).read_bytes())
    require(inventory.get('spdxVersion') == 'SPDX-2.3' and inventory.get('SPDXID') == 'SPDXRef-DOCUMENT'
            and inventory.get('creationInfo', {}).get('created') == manifest['sourceDate']
            and inventory.get('documentNamespace') == 'https://github.com/' + REPOSITORY + '/sbom/' + manifest['sourceCommit'] + '/' + version,
            'Release SBOM identity differs from its source')
    packages = {package['SPDXID']: package for package in inventory['packages']}
    require(len(packages) == len(inventory['packages']), 'Release SBOM contains duplicate package identities')
    for item in artifacts:
        package = packages.get('SPDXRef-Bareplane-' + item['os'] + '-' + item['arch'], {})
        require(package.get('packageFileName') == item['filename'] and package.get('versionInfo') == version
                and package.get('checksums') == [dict(algorithm='SHA256', checksumValue=item['sha256'])],
                'Release SBOM does not describe the actual artifact set')
    require(packages.get('SPDXRef-GoStandardLibrary', {}).get('versionInfo') == manifest['goVersion'].removeprefix('go'),
            'Release SBOM toolchain version differs from the artifacts')
    package = packages.get('SPDXRef-Bareplane-Installer', {})
    require(package.get('packageFileName') == manifest['installer']['filename'] and package.get('versionInfo') == version
            and package.get('checksums') == [dict(algorithm='SHA256', checksumValue=manifest['installer']['sha256'])],
            'Release SBOM does not describe the actual installer')
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    build = commands.add_parser('build')
    build.add_argument('--version', required=True)
    build.add_argument('--source-sha', required=True)
    build.add_argument('--output', type=Path, required=True)
    build.add_argument('--source', type=Path, default=Path(__file__).resolve().parents[1])
    verify = commands.add_parser('verify')
    verify.add_argument('directory', type=Path)
    args = parser.parse_args()
    manifest = (build_release(args.source, args.output, args.version, args.source_sha) if args.command == 'build'
                else verify_release(args.directory))
    print(json.dumps(dict(version=manifest['version'], source_commit=manifest['sourceCommit'],
                          platforms=len(manifest['artifacts']), verified=True, publishing_performed=False), sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except (ReleaseError, OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        raise SystemExit(str(error))
