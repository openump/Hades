import json
from pathlib import Path
import unittest
from tools.contracts import validate_payload
from tools.inference_contracts import validate_event_topic

ROOT = Path(__file__).resolve().parents[1]
INFERENCE = ROOT / 'examples/contracts/inference'


class InferenceTests(unittest.TestCase):
    def test_shared_fixtures(self):
        for case in json.loads((INFERENCE / 'cases.json').read_text()):
            with self.subTest(case=case['name']):
                raw = json.dumps(case['payload']).encode()
                if case['valid']:
                    validate_payload(case['kind'], raw)
                else:
                    with self.assertRaises(ValueError):
                        validate_payload(case['kind'], raw)

    def test_event_topic_binding(self):
        event = validate_payload('inference-event', (INFERENCE / 'inference-event.json').read_bytes())
        registry = json.loads((ROOT / 'examples/contracts/assets.json').read_text())
        base = 'openump/v1/acme/berlin/assembly/line-01/press-04/'
        validate_event_topic(base + 'event/vibration_anomaly', event, registry)
        for topic in (base + 'event/other_anomaly', base + 'command/vibration_anomaly',
                      base + 'event/vibration_anomaly/extra',
                      'openump/v1/acme/berlin/assembly/line-01/press-05/event/vibration_anomaly'):
            with self.subTest(topic=topic), self.assertRaises(ValueError):
                validate_event_topic(topic, event, registry)


if __name__ == '__main__':
    unittest.main()
