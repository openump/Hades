"""Independent development oracle for offline control-contract semantics.

These checks do not verify signatures or execute proposals.
"""

def validate_policy(policy):
    seen = set()
    for asset in policy['assets']:
        identity = asset['asset']
        if identity in seen or identity.split(':')[2] != policy['enterprise']:
            raise ValueError('duplicate asset or enterprise mismatch')
        seen.add(identity)
        if asset['autonomy_level'] > policy['site_autonomy_level']:
            raise ValueError('asset autonomy exceeds site ceiling')
        for parameter in asset['parameters'].values():
            if parameter['min_value'] > parameter['max_value']:
                raise ValueError('inverted safety envelope')
            addresses = list(parameter['mailbox'].values())
            if len(set(addresses)) != len(addresses):
                raise ValueError('mailbox roles overlap')


def validate_receipt(receipt):
    if [g['gate'] for g in receipt['gates']] != list(range(1, 6)):
        raise ValueError('gates must list 1..5 in order')
    if receipt['state'] == 'EXECUTED' and any(g['result'] != 'PASS' for g in receipt['gates']):
        raise ValueError('execution requires passed gates')


def validate_proposal(raw, policy_raw, topic, registry, now_ms):
    from tools.contracts import validate_payload, validate_topic
    command = validate_payload('command', raw)
    policy = validate_payload('actuation-policy', policy_raw)
    parts = topic.split('/')
    if len(parts) != 9 or parts[7:] != ['command', command['target_parameter']]:
        raise ValueError('invalid command topic')
    validate_topic('/'.join(parts[:7] + ['telemetry']), command['asset'], registry)
    if parts[2:4] != [policy['enterprise'], policy['site']] or command['policy_revision'] != policy['policy_revision']:
        raise ValueError('policy scope/revision mismatch')
    age = now_ms - command['issued_at_ms']
    if age < 0 or age >= command['ttl_ms']:
        raise ValueError('expired or future-issued command')
    asset = next((a for a in policy['assets'] if a['asset'] == command['asset']), None)
    if asset is None or command['origin']['autonomy_level'] > asset['autonomy_level']:
        raise ValueError('asset/autonomy not allowed')
    origin = command['origin']
    if {'actor_id': origin['actor_id'], 'key_id': origin['signature']['key_id']} not in asset['allowed_origins']:
        raise ValueError('origin/key not allowed')
    parameter = asset['parameters'].get(command['target_parameter'])
    if parameter is None or command['unit'] != parameter['unit'] or not parameter['min_value'] <= command['target_value'] <= parameter['max_value']:
        raise ValueError('parameter/unit/envelope not allowed')
    if any(p not in command['preconditions'] for p in parameter['required_preconditions']):
        raise ValueError('required precondition omitted or changed')
    return command
