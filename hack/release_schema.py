#!/usr/bin/env python3
"""Check a verified release SBOM against a checksum-pinned official schema."""

import argparse
import hashlib
import json
from pathlib import Path
import urllib.request

import jsonschema

from release_artifacts import ReleaseError, require, verify_release


SCHEMA_URL = 'https://raw.githubusercontent.com/spdx/spdx-spec/v2.3.1/schemas/spdx-schema.json'
SCHEMA_SHA256 = '4126dc29f15e92feec92e9622f4939131ad58b125d6f578aa93e980a6e6f3212'


def validate(directory):
    directory = Path(directory)
    manifest = verify_release(directory)
    with urllib.request.urlopen(SCHEMA_URL, timeout=30) as response:
        data = response.read(1024 * 1024 + 1)
    require(len(data) <= 1024 * 1024 and hashlib.sha256(data).hexdigest() == SCHEMA_SHA256,
            'Official SPDX schema differs from its reviewed checksum')
    schema = json.loads(data)
    validator = jsonschema.validators.validator_for(schema)
    validator.check_schema(schema)
    validator(schema).validate(json.loads((directory / manifest['sbom']['filename']).read_bytes()))
    return dict(sbom_schema='SPDX 2.3.1', schema_sha256=SCHEMA_SHA256, verified=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    args = parser.parse_args()
    try:
        print(json.dumps(validate(args.directory), sort_keys=True))
    except (ReleaseError, OSError, ValueError, jsonschema.ValidationError, jsonschema.SchemaError) as error:
        raise SystemExit(str(error))
