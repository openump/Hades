import json
from pathlib import Path
import unittest
from tools.contracts import validate_payload
from tools.semantic_contracts import reconcile

ROOT = Path(__file__).resolve().parents[1]
CASES = ROOT / 'examples/contracts/semantic-mapping'


class SemanticMappingTests(unittest.TestCase):
    def test_shared_fixtures(self):
        for case in json.loads((CASES / 'cases.json').read_text()):
            with self.subTest(case=case['name']):
                raw = json.dumps(case['payload']).encode()
                if case['valid']:
                    validate_payload(case['kind'], raw)
                else:
                    with self.assertRaises(ValueError):
                        validate_payload(case['kind'], raw)

    def test_reconciliation_with_paired_metadata(self):
        for case in json.loads((CASES / 'reconcile-cases.json').read_text()):
            with self.subTest(case=case['name']):
                def check():
                    mapping = validate_payload('semantic-mapping', json.dumps(case['payload']).encode())
                    metadata = validate_payload('metadata', json.dumps(case['metadata']).encode())
                    reconcile(mapping, metadata)
                if case['valid']:
                    check()
                else:
                    with self.assertRaises(ValueError):
                        check()

    def test_example_pairs_with_site_metadata(self):
        mapping = validate_payload('semantic-mapping', (CASES / 'semantic-mapping.json').read_bytes())
        metadata = validate_payload('metadata', (ROOT / 'examples/site/metadata.json').read_bytes())
        reconcile(mapping, metadata)


if __name__ == '__main__':
    unittest.main()
