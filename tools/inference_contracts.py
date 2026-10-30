"""Independent development oracle for offline Edge AI contract semantics.

These checks do not verify signatures, artifact digests or run any model.
"""


def _enterprise(asset):
    return asset.split(':')[2]


def _version(text):
    return tuple(int(part) for part in text.split('.'))


def validate_manifest(manifest):
    rollback = manifest['budgets']['rollback_to_version']
    if rollback is not None and _version(rollback) >= _version(manifest['model_version']):
        raise ValueError('rollback target is not an earlier version')
    bindings = manifest['inputs']['bindings']
    if len({_enterprise(b['asset']) for b in bindings}) != 1:
        raise ValueError('input bindings span enterprises')
    enterprise = _enterprise(bindings[0]['asset'])
    inputs = [(b['asset'], b['measurement']) for b in bindings]
    if len(inputs) != len(set(inputs)):
        raise ValueError('duplicate input measurement binding')
    events, sensors = [], []
    for output in manifest['outputs']:
        if _enterprise(output['asset']) != enterprise:
            raise ValueError('output outside input enterprise')
        if output['kind'] == 'telemetry':
            series = (output['asset'], output['measurement'])
            if series in inputs:
                raise ValueError('soft-sensor output shadows an input')
            sensors.append(series)
        if output['kind'] == 'inference-event':
            events.append((output['asset'], output['inference_type']))
            if output['severity']['warning_score'] > output['severity']['critical_score']:
                raise ValueError('warning score exceeds critical score')
    if len(events) != len(set(events)) or len(sensors) != len(set(sensors)):
        raise ValueError('duplicate output')


def validate_event(event):
    from datetime import datetime, timezone
    if event['window_end_ms'] <= event['window_start_ms']:
        raise ValueError('empty or inverted window')
    emitted = datetime.strptime(event['timestamp'], '%Y-%m-%dT%H:%M:%S.%fZ').replace(tzinfo=timezone.utc)
    if emitted.timestamp() * 1000 < event['window_end_ms']:
        raise ValueError('event precedes the end of its window')
    if (event['severity'] != 'info') != (event['anomaly_score'] >= event['threshold']):
        raise ValueError('severity disagrees with score and threshold')
    measurements = [c['measurement'] for c in event['top_contributors']]
    if len(measurements) != len(set(measurements)):
        raise ValueError('duplicate contributing measurement')


def validate_event_topic(topic, event, registry):
    from tools.contracts import validate_topic
    parts = topic.split('/')
    if len(parts) != 9 or parts[7:] != ['event', event['inference_type']]:
        raise ValueError('invalid inference-event topic')
    validate_topic('/'.join(parts[:7] + ['telemetry']), event['asset'], registry)
