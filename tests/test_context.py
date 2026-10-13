import copy
import json
from pathlib import Path
import unittest
from tools.contracts import validate_payload, validate_measurement_context, observation_freshness

ROOT = Path(__file__).resolve().parents[1]


class ContextTests(unittest.TestCase):
    def test_context_fixture_expectations(self):
        cases = json.loads((ROOT / 'examples/contracts/context/cases.json').read_text())
        for case in cases:
            with self.subTest(case=case['name']):
                raw = json.dumps(case['payload']).encode()
                if case['valid']:
                    validate_payload(case['kind'], raw)
                else:
                    with self.assertRaises(ValueError):
                        validate_payload(case['kind'], raw)

    def test_telemetry_requires_matching_context(self):
        telemetry = json.loads((ROOT / 'examples/contracts/telemetry/cases.json').read_text())[0]['payload']
        metadata = json.loads((ROOT / 'examples/contracts/context/cases.json').read_text())[0]['payload']
        validate_measurement_context(telemetry, metadata)
        for change in ('revision', 'identity', 'unit', 'type', 'range', 'missing'):
            other = copy.deepcopy(metadata)
            if change == 'revision': other['metadata_revision'] = 2
            if change == 'identity': other['asset_id'] = 'urn:openump:acme:berlin:pump-02'
            if change == 'unit': other['measurements']['motor_temperature']['unit'] = 'K'
            if change == 'type': other['measurements']['motor_temperature']['type'] = 'boolean'
            if change == 'range': other['measurements']['motor_temperature']['valid_range']['maximum'] = 10
            if change == 'missing': other['measurements'].clear()
            with self.subTest(change=change):
                with self.assertRaises(ValueError):
                    validate_measurement_context(telemetry, other)

    def test_boolean_is_not_a_numeric_observation(self):
        telemetry = json.loads((ROOT / 'examples/contracts/telemetry/cases.json').read_text())[0]['payload']
        metadata = json.loads((ROOT / 'examples/contracts/context/cases.json').read_text())[0]['payload']
        telemetry['measurements']['motor_temperature']['value'] = True
        with self.assertRaises(ValueError):
            validate_measurement_context(telemetry, metadata)

    def test_freshness_requires_time_and_known_clock_uncertainty(self):
        now = '2026-10-13T14:00:30.000Z'
        self.assertEqual(observation_freshness('2026-10-13T14:00:29.000Z', now, 1000, 5), 'fresh')
        self.assertEqual(observation_freshness('2026-10-13T14:00:00.000Z', now, 1000, 5), 'stale')
        self.assertEqual(observation_freshness('2026-10-13T14:00:31.000Z', now, 1000, 5), 'unknown')
        self.assertEqual(observation_freshness('2026-10-13T14:00:29.000Z', now, 1000, None), 'unknown')


if __name__ == '__main__':
    unittest.main()
