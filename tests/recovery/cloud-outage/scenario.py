"""Test orchestration only; deployed transfer and fixture code remain Go."""
import base64, json, os, pathlib, socket, subprocess, time

from scripts.wal_store import load_values

class Scenario:
 def __init__(self, scratch, children, logs, broker_port, wait):
  self.scratch=pathlib.Path(scratch);self.children=children;self.logs=logs;self.wait=wait;self.port=broker_port
  self.output=open(self.scratch/'transfer.jsonl','w');self.fault=self.scratch/'cloud-fault';self.fault.write_text('offline')
  self.cert=self.scratch/'pki'
  subprocess.run(['artifacts/hades-pki','-out',str(self.cert)],check=True)
  with socket.socket() as s:s.bind(('127.0.0.1',0));self.cloud_port=s.getsockname()[1]
  self.start_receiver();self.start_transfer()
 def start_receiver(self):
  self.receiver=subprocess.Popen(['artifacts/hades-test-receiver','-listen',f'127.0.0.1:{self.cloud_port}','-store',str(self.scratch/'receiver'),'-fault-file',str(self.fault),'-tls-ca',str(self.cert/'ca.crt'),'-tls-cert',str(self.cert/'cloud-receiver.crt'),'-tls-key',str(self.cert/'cloud-receiver.key')],stdout=self.logs,stderr=self.logs);self.children.append(self.receiver)
 def start_transfer(self):
  self.output.seek(0);self.output.truncate()
  self.adapter=subprocess.Popen(['artifacts/hades-transfer','-broker',f'mqtt://127.0.0.1:{self.port}','-receiver',f'https://127.0.0.1:{self.cloud_port}/v1/ingest/observations','-intake',str(self.scratch/'transfer'),'-quarantine',str(self.scratch/'transfer-quarantine'),'-queue-records','4','-cloud-ca',str(self.cert/'ca.crt'),'-cloud-cert',str(self.cert/'transfer-adapter.crt'),'-cloud-key',str(self.cert/'transfer-adapter.key')],stdout=self.output,stderr=self.logs);self.children.append(self.adapter)
  self.wait(lambda:'"type":"transfer_ready"' in (self.scratch/'transfer.jsonl').read_text(),'transfer subscription')
 def records(self,name):return load_values(str(self.scratch/name))
 def stats(self):
  lines=(self.scratch/'transfer.jsonl').read_text().splitlines()
  return next((e['stats'] for e in reversed([json.loads(line) for line in lines]) if e.get('type')=='transfer_stats'),{})
 def recover(self,local_records,consumer_paths):
  self.wait(lambda:self.stats().get('drops')==1,'explicit reject-newest overflow')
  queued=self.records('transfer');assert len(queued)==4,queued
  telemetry=[r for r in queued if r['topic'].endswith('/telemetry')]
  assert len(telemetry)==2 and len(local_records)==3
  for path in consumer_paths:
   events=[json.loads(line) for line in pathlib.Path(path).read_text().splitlines()]
   assert len([e for e in events if e.get('type')=='telemetry'])==3,events
  local={json.loads(base64.b64decode(r['payload']))['message_id']:r for r in local_records}
  owned={r['message_id']:r for r in telemetry}
  assert set(owned)<set(local)
  # Interrupt immutable replay, recover the same four disk records.
  self.adapter.kill();self.adapter.wait(timeout=10);self.start_transfer()
  assert {r['message_id'] for r in self.records('transfer')}=={r['message_id'] for r in queued}
  # Cloud commits but every response is lost; source must retain ownership.
  self.fault.write_text('receipt-loss')
  self.wait(lambda:len(self.records('receiver'))>=1,'receiver commit before receipt loss')
  assert len(self.records('transfer'))==4
  original_receipts={r['record']['message_id']:r['receipt'] for r in self.records('receiver')}
  self.receiver.kill();self.receiver.wait(timeout=10);self.start_receiver()
  self.fault.write_text('normal')
  self.wait(lambda:len(self.records('transfer'))==0,'verified replay retirement')
  remote={r['record']['message_id']:r for r in self.records('receiver')}
  assert set(remote)=={r['message_id'] for r in queued},remote
  for r in queued:
   assert remote[r['message_id']]['record']['payload']==r['payload']
   assert remote[r['message_id']]['record']['topic']==r['topic']
   assert remote[r['message_id']]['record']['digest']==r['digest']
  for identity,receipt in original_receipts.items():assert remote[identity]['receipt']==receipt
  report={'profile':'isolated-contract-compatible-test-receiver','local_history_ids':sorted(local),'remote_telemetry_ids':sorted(owned),'transfer_lost_ids':sorted(set(local)-set(owned)),'local_observations':3,'remote_observations':2,'transfer_loss':1,'local_loss':0,'consumer_observations_each':3,'queue_records_limit':4,'queue_bytes_limit':256*1024*1024,'interrupted_replay_recovered':4,'receipt_loss_retained_then_recovered':True,'receiver_sigkill_recovered_original_receipts':len(original_receipts),'exact_topic_payload_digest':True,'late_event_age_seconds_minimum':15,'pilot':'not_selected'}
  destination=pathlib.Path('artifacts/cloud-outage');destination.mkdir(parents=True,exist_ok=True)
  (destination/'report.json').write_text(json.dumps(report,indent=2)+'\n')
  print('cloud outage: '+json.dumps(report,sort_keys=True))
