"""Independent development oracle for the semantic-mapping contract (Hades-045).

Written separately from the Go validator: the dictionary identifier forms and
the (asset_id, metadata_revision) reconciliation are re-implemented here.
"""
import re

# [0-9], never \d: Python's \d also matches non-ASCII digits.
_ECLASS = re.compile(r'(irdi:)?[0-9]{4}-1#[0-9]{2}-[A-Z0-9]{6}#[0-9]{3}')
_IEC = re.compile(r'0112/2///[0-9]{5}(_[0-9]+)?#[A-Z]{3}[0-9]{3}#[0-9]{3}')
_URI = re.compile(r'(urn:[a-z0-9][a-z0-9-]{0,31}:|https?://)[!-~]+')


def _matches(dictionary, identifier):
    if dictionary == 'eclass':
        return bool(_ECLASS.fullmatch(identifier))
    if dictionary == 'iec_cdd':
        return bool(_IEC.fullmatch(identifier) or _URI.fullmatch(identifier))
    if dictionary in ('opcua_companion', 'custom_uri'):
        return bool(_URI.fullmatch(identifier))
    return False


def validate_semantic_mapping(mapping):
    """Every semantic_id must have the form of its declared dictionary."""
    if 'semantic_id' in mapping and not _matches(mapping['dictionary'], mapping['semantic_id']):
        raise ValueError('asset semantic_id does not match its dictionary')
    for name, measurement in sorted(mapping['measurements'].items()):
        if 'semantic_id' in measurement and not _matches(measurement['dictionary'], measurement['semantic_id']):
            raise ValueError(f'{name}: semantic_id does not match its dictionary')


def reconcile(mapping, metadata):
    """A mapping is valid only with its exact asset and metadata revision."""
    if mapping['asset_id'] != metadata['asset_id']:
        raise ValueError('semantic mapping is for another asset')
    if mapping['metadata_revision'] != metadata['metadata_revision']:
        raise ValueError('semantic mapping is pinned to another metadata_revision')
    undeclared = sorted(set(mapping['measurements']) - set(metadata['measurements']))
    if undeclared:
        raise ValueError(f'{undeclared[0]} is not declared in the paired metadata revision')
