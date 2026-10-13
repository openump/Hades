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
        fixture = json.loads((ROOT / 'examples/contracts/telemetry/cases.json').read_text())[0]['payload']
        valid = json.dumps(fixture).encode()
        duplicate = valid.replace(b'{', b'{"schema_version": "1.0",', 1)
        invalid = [valid.replace(b'67.2', token) for token in (b'NaN', b'Infinity', b'1e999')]
        for raw in invalid + [duplicate, b'\xff', b'[]', b'{']:
            with self.subTest(raw=raw[:80]):
                with self.assertRaises(ValueError):
                    validate_payload('telemetry', raw)

    def test_wire_limit_is_utf8_bytes_not_character_count(self):
        fixture = json.loads((ROOT / 'examples/contracts/telemetry/cases.json').read_text())[0]['payload']
        raw = json.dumps(fixture).encode()
        validate_payload('telemetry', raw + b' ' * (65536 - len(raw)))
        with self.assertRaises(ValueError):
            validate_payload('telemetry', raw + b' ' * (65537 - len(raw)))
        fixture['measurements'] = {f'm{i}': {'value': '😀' * 256, 'unit': '1', 'quality': 'good'} for i in range(64)}
        text = json.dumps(fixture, ensure_ascii=False)
        self.assertLess(len(text), 65536)
        self.assertGreater(len(text.encode()), 65536)
        with self.assertRaises(ValueError):
            validate_payload('telemetry', text.encode())

    def test_unsupported_message_type_cannot_select_arbitrary_file(self):
        with self.assertRaises(ValueError):
            validate_payload('../secrets', b'{}')


if __name__ == '__main__':
    unittest.main()
