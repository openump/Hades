import json
from pathlib import Path
import unittest
from tools.contracts import validate_payload

ROOT = Path(__file__).resolve().parents[1]


class TelemetryTests(unittest.TestCase):
    def test_fixture_expectations(self):
        cases = json.loads((ROOT / 'examples/contracts/telemetry/cases.json').read_text())
        for case in cases:
            with self.subTest(case=case['name']):
                raw = json.dumps(case['payload']).encode()
                if case['valid']:
                    validate_payload('telemetry', raw)
                else:
                    with self.assertRaises(ValueError):
                        validate_payload('telemetry', raw)

    def test_invalid_wire_json_is_rejected(self):
        for raw in [b'{"value": NaN}', b'{"value": Infinity}', b'{"value": 1e999}',
                    b'{"value": 1, "value": 2}', b'\xff', b'[]', b'{']:
            with self.subTest(raw=raw):
                with self.assertRaises(ValueError):
                    validate_payload('telemetry', raw)

    def test_wire_limit_is_utf8_bytes_not_character_count(self):
        fixture = json.loads((ROOT / 'examples/contracts/telemetry/cases.json').read_text())[0]['payload']
        raw = json.dumps(fixture).encode()
        validate_payload('telemetry', raw + b' ' * (65536 - len(raw)))
        with self.assertRaises(ValueError):
            validate_payload('telemetry', raw + b' ' * (65537 - len(raw)))

    def test_unsupported_message_type_cannot_select_arbitrary_file(self):
        with self.assertRaises(ValueError):
            validate_payload('../secrets', b'{}')


if __name__ == '__main__':
    unittest.main()
