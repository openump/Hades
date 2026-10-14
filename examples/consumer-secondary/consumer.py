#!/usr/bin/env python3
"""Independent development oracle: Mosquitto C transport + Python schemas/context."""
import argparse, collections, datetime, hashlib, json, pathlib, sys
sys.path.insert(0,str(pathlib.Path(__file__).resolve().parents[2]))
from tools.contracts import validate_payload,validate_measurement_context,validate_topic,observation_freshness
class Consumer:
 def __init__(self,metadata):
  self.metadata=validate_payload('metadata',metadata)
  m=self.metadata;identity=m['asset_id'].split(':');h=m['hierarchy']
  self.registry=[dict(asset_id=m['asset_id'],enterprise=identity[2],identity_site=identity[3],asset_key=identity[4],location={k:h[k] for k in ('site','area','line','asset')})]
  self.seen=collections.OrderedDict()
 def inspect(self,event,now):
  topic=event['topic'];raw=event['payload'].encode('utf-8')
  kind=topic.rsplit('/',1)[-1];p=validate_payload(kind,raw)
  validate_topic(topic,p['asset_id'],self.registry)
  if p['metadata_revision']!=self.metadata['metadata_revision']:raise ValueError('unknown metadata revision')
  if kind=='metadata' and p!=self.metadata:raise ValueError('metadata conflict')
  if kind=='telemetry':
   if event['retain']:raise ValueError('retained telemetry')
   validate_measurement_context(p,self.metadata)
  digest=hashlib.sha256(topic.encode()+b'\0'+raw).digest();key=p['message_id']
  duplicate=key in self.seen
  if duplicate and self.seen[key]!=digest:raise ValueError('conflicting identity')
  if not duplicate:
   self.seen[key]=digest
   if len(self.seen)>4096:self.seen.popitem(last=False)
  result=dict(type=kind,asset_id=p['asset_id'],message_id=key,duplicate=duplicate,topic=topic,payload=p)
  if kind=='telemetry':
   r=p['measurements']['motor_temperature'];d=self.metadata['measurements']['motor_temperature']
   result.update(value=r['value'],unit=r['unit'],quality=r['quality'],observed_at=p['observed_at'],freshness=observation_freshness(p['observed_at'],now,d['expected_interval_ms'],p['source']['clock_uncertainty_ms']))
  return result

def main():
 parser=argparse.ArgumentParser();parser.add_argument('--config',default='examples/site/metadata.json');parser.add_argument('--count',type=int,default=3)
 args=parser.parse_args()
 if not 1<=args.count<=10000:parser.error('count must be 1..10000')
 consumer=Consumer(pathlib.Path(args.config).read_bytes());total=0
 while total<args.count:
  # JSON escaping of a 64 KiB payload is bounded by six bytes per wire byte.
  line=sys.stdin.buffer.readline(512*1024+1)
  if not line:raise ValueError('transport ended before expected unique observations')
  if len(line)>512*1024:raise ValueError('transport line exceeds bound')
  if line.startswith(b'Subscribed (mid:'):continue
  if line.startswith(b'Client '):
   if b'received SUBACK' in line:print('{"type":"ready"}',flush=True)
   continue
  result=consumer.inspect(json.loads(line),datetime.datetime.now(datetime.timezone.utc).isoformat())
  print(json.dumps(result,separators=(',',':')),flush=True)
  if result['type']=='telemetry' and not result['duplicate']:total+=1
if __name__=='__main__':
 try:main()
 except (ValueError,KeyError,TypeError) as error:print(str(error),file=sys.stderr);sys.exit(1)
