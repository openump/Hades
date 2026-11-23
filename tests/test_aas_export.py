"""Hades-045: AAS exports are valid against the official AAS V3.0.6 JSON schema.

The schema is vendored unmodified (tests/fixtures/aas/README.md). This test is
independent of the Go exporter: it reads the committed golden export and
checks it against the schema and against the OpenUMP catalog it came from.
"""
import copy
import hashlib
import json
from pathlib import Path
import unittest

from jsonschema import Draft201909Validator

ROOT = Path(__file__).resolve().parents[1]
SCHEMA = ROOT / 'tests/fixtures/aas/aas-v3.0.6.json'
SCHEMA_SHA256 = '4baf8c108ee3aa736408e2a5aa6bfd31097ced447fa59716354f1213ccca115a'
GOLDEN = ROOT / 'examples/aas/press-04.aas.json'


def load(path):
    return json.loads(path.read_text(encoding='utf-8'))


class AASExportTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        raw = SCHEMA.read_bytes()
        assert hashlib.sha256(raw).hexdigest() == SCHEMA_SHA256, 'vendored AAS schema changed'
        cls.validator = Draft201909Validator(json.loads(raw))

    def errors(self, document):
        return list(self.validator.iter_errors(document))

    def test_golden_and_examples_are_metamodel_valid(self):
        for path in (GOLDEN, ROOT / 'examples/aas/vendor-environment.json'):
            with self.subTest(path=path.name):
                self.assertEqual([e.message for e in self.errors(load(path))], [])

    def test_schema_rejects_broken_exports(self):
        # Negative controls: the validator is live, not vacuously passing.
        golden = load(GOLDEN)
        mutations = {
            'idShort starts with a digit': lambda e: e['assetAdministrationShells'][0].update(idShort='4press'),
            'missing modelType': lambda e: e['submodels'][0].pop('modelType'),
            'unknown valueType': lambda e: e['submodels'][1]['submodelElements'][1]['value'][0].update(valueType='xs:number'),
            'empty reference keys': lambda e: e['submodels'][0]['semanticId'].update(keys=[]),
            'control character in text': lambda e: e['assetAdministrationShells'][0]['displayName'][0].update(text='a\x01b'),
            'missing assetKind': lambda e: e['assetAdministrationShells'][0]['assetInformation'].pop('assetKind'),
        }
        for name, mutate in mutations.items():
            with self.subTest(mutation=name):
                broken = copy.deepcopy(golden)
                mutate(broken)
                self.assertNotEqual(self.errors(broken), [])

    def test_golden_matches_its_catalog(self):
        """Independent of Go: the export says what the OpenUMP pair says."""
        env = load(GOLDEN)
        metadata = load(ROOT / 'examples/site/metadata.json')
        mapping = load(ROOT / 'examples/contracts/semantic-mapping/semantic-mapping.json')
        shell, = env['assetAdministrationShells']
        self.assertEqual(shell['id'], mapping['aas']['aas_id'])
        self.assertEqual(shell['assetInformation']['globalAssetId'], mapping['aas']['global_asset_id'])
        extensions = {x['name']: x['value'] for x in shell['extensions']}
        self.assertEqual(extensions['openump:asset_id'], metadata['asset_id'])
        self.assertEqual(extensions['openump:metadata_revision'], str(metadata['metadata_revision']))
        submodels = {s['idShort']: s for s in env['submodels']}
        self.assertEqual(submodels['Nameplate']['semanticId']['keys'][0]['value'],
                         'https://admin-shell.io/zvei/nameplate/2/0/Nameplate')
        self.assertEqual(submodels['TechnicalData']['semanticId']['keys'][0]['value'],
                         'https://admin-shell.io/ZVEI/TechnicalData/Submodel/1/2')
        # OperationalData is a Hades submodel, never presented as IDTA 02008.
        operational = submodels['OperationalData']['semanticId']['keys'][0]['value']
        self.assertTrue(operational.startswith('urn:openump:'))
        self.assertEqual({s['id'] for s in env['submodels']}, set(mapping['aas']['submodel_ids'].values()))

        concepts = {c['id']: c for c in env['conceptDescriptions']}
        technical = next(e for e in submodels['TechnicalData']['submodelElements'] if e['idShort'] == 'TechnicalProperties')
        elements = {e['idShort']: e for e in technical['value']}
        self.assertEqual(set(elements), set(metadata['measurements']))
        for name, declared in metadata['measurements'].items():
            with self.subTest(measurement=name):
                element = elements[name]
                mapped = mapping['measurements'].get(name, {})
                if 'semantic_id' in mapped:
                    self.assertEqual(element['semanticId']['keys'][0]['value'], mapped['semantic_id'])
                concept_id = next(r['keys'][0]['value'] for r in [element['semanticId'], *element.get('supplementalSemanticIds', [])]
                                  if r['type'] == 'ModelReference')
                iec = concepts[concept_id]['embeddedDataSpecifications'][0]['dataSpecificationContent']
                self.assertEqual(iec['unit'], declared['unit'])
                qualifiers = {q['type']: q['value'] for q in element['qualifiers']}
                self.assertEqual(int(qualifiers['openump:expected_interval_ms']), declared['expected_interval_ms'])
                if 'source_tag' in mapped:
                    self.assertEqual(qualifiers['openump:source_tag'], mapped['source_tag'])
                if 'valid_range' in declared:
                    self.assertEqual(element['modelType'], 'Range')
                    self.assertEqual(float(element['min']), declared['valid_range']['minimum'])
                    self.assertEqual(float(element['max']), declared['valid_range']['maximum'])


if __name__ == '__main__':
    unittest.main()
