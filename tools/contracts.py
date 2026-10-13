"""Foundation contract checks; full CLI/CI conformance is Hades-006."""
import re

SEGMENT = re.compile(r'[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?')


def validate_topic(topic, asset_id, registry):
    """Validate a publication topic against a current, trusted asset registry."""
    if not isinstance(topic, str) or len(topic.encode('utf-8')) > 512:
        raise ValueError('topic must be a string of at most 512 UTF-8 bytes')
    parts = topic.split('/')
    if len(parts) != 8 or parts[:2] != ['openump', 'v1']:
        raise ValueError('expected openump/v1 and five hierarchy segments plus message type')
    if any(not SEGMENT.fullmatch(value) for value in parts[2:7]):
        raise ValueError('hierarchy segments must be 1–64 lowercase ASCII letters/digits/hyphens')
    if parts[7] not in ('telemetry', 'metadata', 'status'):
        raise ValueError('unsupported message type')
    identities = [entry['asset_id'] for entry in registry]
    if len(identities) != len(set(identities)):
        raise ValueError('duplicate asset identity in registry')
    paths = [(entry['enterprise'], *[entry['location'][key] for key in ('site', 'area', 'line', 'asset')])
             for entry in registry]
    if len(paths) != len(set(paths)):
        raise ValueError('multiple assets assigned to the same topic path')
    matching = [entry for entry in registry if entry['asset_id'] == asset_id]
    if len(matching) != 1:
        raise ValueError('asset is not registered')
    asset = matching[0]
    expected_id = f"urn:openump:{asset['enterprise']}:{asset['identity_site']}:{asset['asset_key']}"
    if asset_id != expected_id:
        raise ValueError('registry identity does not match immutable allocation fields')
    expected = [asset['enterprise']] + [asset['location'][key] for key in ('site', 'area', 'line', 'asset')]
    if parts[2:7] != expected:
        raise ValueError('topic does not match current asset registry location')
    return parts[7]


def validate_payload(kind, raw):
    """Check bounded strict JSON and local Draft 2020-12 schemas without network IO."""
    import json
    import math
    from pathlib import Path
    from jsonschema import Draft202012Validator, FormatChecker
    from referencing import Registry, Resource

    if kind not in ('telemetry', 'metadata', 'status'):
        raise ValueError('unsupported message type')
    if not isinstance(raw, bytes) or len(raw) > 65536:
        raise ValueError('payload must be bytes of at most 65536 bytes')

    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError('duplicate JSON key')
            result[key] = value
        return result

    def reject_constant(value):
        raise ValueError('non-finite JSON constant')

    def finite_float(value):
        number = float(value)
        if not math.isfinite(number):
            raise ValueError('non-finite JSON number')
        return number

    try:
        payload = json.loads(raw.decode('utf-8'), object_pairs_hook=pairs,
                             parse_constant=reject_constant, parse_float=finite_float)
    except (UnicodeError, RecursionError) as error:
        raise ValueError('invalid UTF-8 or excessive JSON nesting') from error
    root = Path(__file__).resolve().parents[1] / 'schemas/v1'
    schemas = [json.loads(path.read_text()) for path in sorted(root.glob('*.schema.json'))]
    registry = Registry().with_resources((schema['$id'], Resource.from_contents(schema)) for schema in schemas)
    schema = next((value for value in schemas if value['$id'] == f'urn:openump:schema:1.0:{kind}'), None)
    if schema is None:
        raise ValueError('schema is not implemented')
    validator = Draft202012Validator(schema, registry=registry, format_checker=FormatChecker())
    error = next(validator.iter_errors(payload), None)
    if error:
        raise ValueError(f'{list(error.absolute_path)}: {error.message}')
    return payload
