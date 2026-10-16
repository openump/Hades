import copy
import json
from pathlib import Path
import unittest
from tools.contracts import validate_payload
from tools.control_contracts import validate_proposal

ROOT = Path(__file__).resolve().parents[1]
CONTROL = ROOT / 'examples/contracts/control'

class ControlTests(unittest.TestCase):
    def test_shared_fixtures(self):
        for case in json.loads((CONTROL / 'cases.json').read_text()):
            with self.subTest(case=case['name']):
                raw = json.dumps(case['payload']).encode()
                if case['valid']:
                    validate_payload(case['kind'], raw)
                else:
                    with self.assertRaises(ValueError):
                        validate_payload(case['kind'], raw)

    def test_offline_policy_and_lease(self):
        command = json.loads((CONTROL / 'command.json').read_text())
        policy = (CONTROL / 'actuation-policy.json').read_bytes()
        registry = json.loads((ROOT / 'examples/contracts/assets.json').read_text())
        topic = 'openump/v1/acme/berlin/assembly/line-01/press-04/command/pressure_setpoint'
        validate_proposal(json.dumps(command).encode(), policy, topic, registry, 1792152000001)
        for change in ('expired', 'future', 'envelope', 'preconditions', 'autonomy', 'actor', 'topic'):
            c = copy.deepcopy(command)
            now = 1792152000001
            target = topic
            if change == 'expired': now += 5000
            if change == 'future': now -= 1000
            if change == 'envelope': c['target_value'] = 11
            if change == 'preconditions': c['preconditions'][0]['value'] = 0
            if change == 'autonomy': c['origin']['autonomy_level'] = 'L3'
            if change == 'actor': c['origin']['actor_id'] = 'stranger'
            if change == 'topic': target = topic.replace('/berlin/', '/elsewhere/')
            with self.subTest(change=change), self.assertRaises(ValueError):
                validate_proposal(json.dumps(c).encode(), policy, target, registry, now)

    def test_receipt_requires_five_distinct_passed_gates(self):
        receipt = json.loads((CONTROL / 'command-receipt.json').read_text())
        receipt.update(state='EXECUTED', terminal=True, pre_write_value=4, post_write_value=5)
        for gate in receipt['gates']: gate['result'] = 'PASS'
        validate_payload('command-receipt', json.dumps(receipt).encode())
        receipt['gates'][4]['result'] = 'NOT_EVALUATED'
        with self.assertRaises(ValueError):
            validate_payload('command-receipt', json.dumps(receipt).encode())
        receipt['gates'][4].update(result='PASS', gate=4)
        with self.assertRaises(ValueError):
            validate_payload('command-receipt', json.dumps(receipt).encode())
        command = json.loads((CONTROL / 'command.json').read_text())
        command['command_id'] += '\n'
        with self.assertRaises(ValueError):
            validate_payload('command', json.dumps(command).encode())
