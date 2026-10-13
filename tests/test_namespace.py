import json
from pathlib import Path
import unittest
from tools.contracts import validate_topic

ROOT = Path(__file__).resolve().parents[1]


class NamespaceTests(unittest.TestCase):
    def test_topic_and_asset_fixtures(self):
        registry = json.loads((ROOT / 'examples/contracts/assets.json').read_text())
        cases = json.loads((ROOT / 'examples/contracts/topics.json').read_text())
        for case in cases:
            with self.subTest(case=case['name']):
                if case['valid']:
                    validate_topic(case['topic'], case['asset_id'], registry)
                else:
                    with self.assertRaises(ValueError):
                        validate_topic(case['topic'], case['asset_id'], registry)

    def test_registry_cannot_silently_assign_same_identity_twice(self):
        registry = json.loads((ROOT / 'examples/contracts/assets.json').read_text())
        case = json.loads((ROOT / 'examples/contracts/topics.json').read_text())[0]
        with self.assertRaises(ValueError):
            validate_topic(case['topic'], case['asset_id'], registry + [registry[0]])


if __name__ == '__main__':
    unittest.main()
