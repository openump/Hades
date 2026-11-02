import json
from pathlib import Path
import unittest
from tools.contracts import validate_payload
from tools.inference_contracts import validate_derived_declaration, validate_derived_topic

ROOT = Path(__file__).resolve().parents[1]
DERIVED = ROOT / 'examples/contracts/derived'


class DerivedValueTests(unittest.TestCase):
    def test_shared_fixtures(self):
        for case in json.loads((DERIVED / 'cases.json').read_text()):
            with self.subTest(case=case['name']):
                raw = json.dumps(case['payload']).encode()
                if case['valid']:
                    validate_payload(case['kind'], raw)
                else:
                    with self.assertRaises(ValueError):
                        validate_payload(case['kind'], raw)

    def test_declaration_and_topic(self):
        value = validate_payload('derived-value', (DERIVED / 'derived-value.json').read_bytes())
        manifest = json.loads((ROOT / 'examples/contracts/inference/model-manifest.json').read_text())
        devices = {'motor_temperature', 'hydraulic_pressure'}
        validate_derived_declaration(value, [manifest], devices)
        for change in ({'unit': 'K'}, {'model_version': '9.9.9'}, {'measurement': 'other_estimate'},
                       {'measurement': 'motor_temperature'}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                validate_derived_declaration({**value, **change}, [manifest], devices)
        registry = json.loads((ROOT / 'examples/contracts/assets.json').read_text())
        base = 'openump/v1/acme/berlin/assembly/line-01/press-04/'
        validate_derived_topic(base + 'derived/estimated_bearing_wear', value, registry)
        for topic in (base + 'telemetry', base + 'derived/other', base + 'event/estimated_bearing_wear',
                      'openump/v1/acme/berlin/assembly/line-01/press-05/derived/estimated_bearing_wear'):
            with self.subTest(topic=topic), self.assertRaises(ValueError):
                validate_derived_topic(topic, value, registry)


if __name__ == '__main__':
    unittest.main()
