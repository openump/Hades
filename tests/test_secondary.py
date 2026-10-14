import importlib.util,json,pathlib,unittest
path=pathlib.Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('secondary',path/'examples/consumer-secondary/consumer.py');module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
class SecondaryTests(unittest.TestCase):
 def test_duplicate_conflict_stale_and_bad(self):
  consumer=module.Consumer((path/'examples/site/metadata.json').read_bytes())
  raw=json.loads((path/'examples/raw-observations/valid.json').read_text());reading=raw.pop('signals')['temperature'];reading.update(value=67.2,unit='Cel',quality='bad',reason='unreliable');raw['measurements']={'motor_temperature':reading}
  event=dict(topic='openump/v1/acme/berlin/assembly/line-01/press-04/telemetry',payload=json.dumps(raw),retain=0)
  result=consumer.inspect(event,'2026-10-14T12:00:10.000Z')
  self.assertEqual((result['freshness'],result['quality'],result['value']),('stale','bad',67.2))
  self.assertTrue(consumer.inspect(event,'2026-10-14T12:00:10.000Z')['duplicate'])
  raw['ingested_at']='2026-10-14T12:00:11.000Z';event['payload']=json.dumps(raw)
  with self.assertRaisesRegex(ValueError,'conflicting'):consumer.inspect(event,'2026-10-14T12:00:12.000Z')
