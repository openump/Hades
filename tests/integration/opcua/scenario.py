"""Development-only end-to-end OPC UA source/security/fault evidence."""
import base64,datetime,hashlib,json,os,pathlib,platform,socket,subprocess,sys,time,urllib.parse

class Scenario:
 def __init__(self,scratch,logs,children,flags,http,wait,sub_command):
  self.dir=pathlib.Path(scratch);self.logs=logs;self.children=children;self.flags=flags;self.http=http;self.wait=wait;self.server=None
  self.sub_command=sub_command;self.events=[];self.cases=[];self.units=[]
 def run(self):
  subprocess.run(['./scripts/opcua-mock-tool','build'],check=True)
  subprocess.run(['go','build','-o','artifacts/hades-opcua-gateway-native','./cmd/hades-opcua-gateway'],check=True)
  self.pki=self.dir/'opcua-pki';subprocess.run(['artifacts/hades-opcua-mock-native','-action','init','-pki',str(self.pki)],check=True)
  self.fault=self.dir/'opcua-fault';self.fault.write_text('good')
  with socket.socket() as free:free.bind(('127.0.0.1',0));self.port=free.getsockname()[1]
  self.endpoint='opc.tcp://127.0.0.1:'+str(self.port)
  self.secondary=self.dir/'opcua-secondary.jsonl';self.secondfile=open(self.secondary,'w')
  transport=subprocess.Popen(self.sub_command+['-t','openump/v1/acme/berlin/assembly/line-01/press-04/+','-F','%j','-d'],stdout=subprocess.PIPE,stderr=self.logs);self.children.append(transport)
  second=subprocess.Popen([os.environ.get('HADES_PYTHON',sys.executable),'examples/consumer-secondary/consumer.py','--config','examples/site/opcua-mock.metadata.json','--count','30'],stdin=transport.stdout,stdout=self.secondfile,stderr=self.logs);self.children.append(second);transport.stdout.close();self.second=second
  self.wait(lambda:'"type":"ready"' in self.secondary.read_text(),'OPC UA independent consumer')
  self.start_server(False)
  modes=['good','uncertain','bad','missing','delay','server-down','restarted-good','no-source-time','no-times','wrong-type']
  try:
   for index,mode in enumerate(modes):
    if mode=='server-down':self.stop_server()
    elif mode=='restarted-good':self.fault.write_text('good');self.start_server(True)
    else:self.fault.write_text(mode)
    binary=pathlib.Path('artifacts/hades-opcua-gateway-native').resolve()
    argv=['-endpoint',self.endpoint,'-opcua-pki',str(self.pki),'-ingress','https://127.0.0.1:18083/observations','-count','1']+self.flags('gateway-press-04')
    state=self.dir/('ua-'+str(index));state.mkdir(mode=0o700)
    files=[pathlib.Path('examples/site/opcua-mock.metadata.json').resolve(),*pathlib.Path('schemas/v1').resolve().glob('*.json'),self.pki/'server.crt',self.pki/'client.crt',self.pki/'client.key']
    # Include the ingress trust/identity files referenced by the reviewed command.
    files += [pathlib.Path(argv[i+1]) for i,v in enumerate(argv[:-1]) if v in ('-tls-ca','-tls-cert','-tls-key')]
    cfg={'revision':'opcua-pilot-v1','binary':str(binary),'binary_sha256':hashlib.sha256(binary.read_bytes()).hexdigest(),'args':[str(v) for v in argv],'directory':str(pathlib.Path.cwd()),'state':str(state),'ready_pattern':'opcua_gateway_ready','files':{str(p):hashlib.sha256(p.read_bytes()).hexdigest() for p in files},'writable_paths':[str(self.dir)],'log_bytes':65536,'finite':True}
    definition=self.dir/('ua-'+str(index)+'.json');definition.write_text(json.dumps(cfg));definition.chmod(0o600)
    digest=subprocess.check_output(['artifacts/hades-service-native','-action','digest','-config',str(definition)],text=True).strip()
    if os.environ.get('HADES_TEST_SYSTEMD')=='1':
     unit='hades-opcua-'+os.urandom(5).hex();unitfile=self.dir/(unit+'.service')
     unitfile.write_bytes(subprocess.check_output(['artifacts/hades-service-native','-action','unit','-config',str(definition),'-name',unit,'-user',os.environ['USER']]))
     subprocess.run(['sudo','install','-m','644',str(unitfile),'/run/systemd/system/'+unit+'.service'],check=True);self.units.append(unit)
     subprocess.run(['sudo','systemctl','daemon-reload'],check=True);subprocess.run(['sudo','systemctl','start',unit],check=True)
     self.wait(lambda:subprocess.check_output(['systemctl','show',unit,'-p','ActiveState','--value'],text=True).strip()=='inactive','finite installed OPC UA gateway')
     assert subprocess.check_output(['systemctl','show',unit,'-p','ExecMainStatus','--value'],text=True).strip()=='0'
    else:subprocess.run(['artifacts/hades-service-native','-action','run','-config',str(definition),'-digest',digest],check=True,timeout=20,stderr=self.logs)
    payloads=[json.loads(line)['payload'] for line in (state/'events.log').read_text().splitlines() if line.startswith('{"boundary":')]
    assert len(payloads)==3,(mode,payloads)
    expected='uncertain' if mode=='uncertain' else 'bad' if mode in ('bad','missing','delay','server-down','wrong-type') else 'good'
    for payload in payloads:
     reading=next(iter(payload['signals'].values()))
     assert reading['quality']==expected,(mode,reading)
     assert (reading['value'] is None)==(expected=='bad'),(mode,reading)
     assert payload['source']['clock_uncertainty_ms'] is None
     if mode=='no-times':assert payload['source']['clock_origin']=='gateway'
     elif mode not in ('delay','server-down'):assert payload['source']['clock_origin']=='device'
    self.events.extend(payloads);self.cases.append({'mode':mode,'ids':[p['message_id'] for p in payloads]})
   assert self.second.wait(timeout=10)==0
   return self.events
  finally:
   self.stop_server()
   for unit in self.units:
    subprocess.run(['sudo','systemctl','stop',unit],check=False);subprocess.run(['sudo','rm','-f','/run/systemd/system/'+unit+'.service'],check=True)
   if self.units:subprocess.run(['sudo','systemctl','daemon-reload'],check=True)
   self.secondfile.close()
 def start_server(self,pad):
  ledger=self.dir/('opcua-source-b.jsonl' if pad else 'opcua-source-a.jsonl')
  argv=['artifacts/hades-opcua-mock-native','-endpoint',self.endpoint,'-pki',str(self.pki),'-fault-file',str(self.fault),'-ledger',str(ledger)]
  if pad:argv+=['-namespace-padding']
  self.server=subprocess.Popen(argv,stdout=self.logs,stderr=self.logs);self.children.append(self.server)
  self.wait(lambda:socket.create_connection(('127.0.0.1',self.port),timeout=1).close() or True,'OPC UA server readiness')
 def stop_server(self):
  if self.server is not None and self.server.poll() is None:self.server.terminate();assert self.server.wait(timeout=8)==0
  self.server=None
 def verify(self,local,remote,primary):
  ids={p['message_id'] for p in self.events};assert len(ids)==30
  original={p['message_id']:p for p in self.events}
  normalized={}
  for owned in local:
   payload=json.loads(base64.b64decode(owned['payload']));normalized[payload['message_id']]=payload
  assert set(normalized)==ids
  for key,payload in normalized.items():
   raw=original[key]
   for field in ('message_id','source','observed_at','ingested_at','extensions'):assert payload[field]==raw[field],(key,field)
   assert payload['measurements']==raw['signals']
  for path in (pathlib.Path(primary),self.secondary):
   records=[json.loads(line) for line in path.read_text().splitlines()]
   records=[r for r in records if r.get('type')=='telemetry']
   assert {p['message_id'] for p in records}==ids
   for record in records:assert record['payload']==normalized[record['message_id']]
  server_records=[]
  for path in self.dir.glob('opcua-source-*.jsonl'):server_records += [json.loads(line) for line in path.read_text().splitlines()]
  indices={p["extensions"]["opcua.namespace_index"] for p in self.events if p["extensions"].get("opcua.namespace_index") is not None}
  assert len(indices)==2,indices
  matched=0
  for payload in self.events:
   ext=payload['extensions'];mode=next(c['mode'] for c in self.cases if payload['message_id'] in c['ids'])
   if mode in ('delay','server-down','no-times'):continue
   matches=[r for r in server_records if r['node']==ext['opcua.node_id'] and r['source_timestamp']==ext['opcua.source_timestamp'] and r['server_timestamp']==ext['opcua.server_timestamp']]
   if mode=='no-source-time':matches=[r for r in server_records if r['node']==ext['opcua.node_id'] and r['mode']==mode and r['server_timestamp']==ext['opcua.server_timestamp']]
   assert matches,(mode,ext,server_records)
   assert any(r['status_code']==ext['opcua.status_code'] for r in matches)
   reading=next(iter(payload['signals'].values()))
   if reading['value'] is not None:assert any(r['value']==reading['value'] for r in matches)
   matched+=1
  query=urllib.parse.urlencode({'match[]':'hades_observation_value{asset_id="urn:openump:acme:berlin:press-04",metadata_revision="3"}'})
  def exported():return [json.loads(line) for line in self.http('/api/v1/export?'+query,'consumer-primary',200).read().splitlines()]
  expected={name:{} for name in ('motor_temperature','cycle_counter','machine_state')}
  for payload in normalized.values():
   for name,value in payload['measurements'].items():
    if value['quality']=='good':expected[name][int(round(datetime.datetime.fromisoformat(payload['observed_at'].replace('Z','+00:00')).timestamp()*1000))]=value['value']
  def history_matches():
   rows=exported();actual={row['metric']['measurement']:dict(zip(row['timestamps'],row['values'])) for row in rows}
   return actual==expected
  self.wait(history_matches,'OPC UA numeric historian comparison')
  out=pathlib.Path('artifacts/opcua-pilot');out.mkdir(parents=True,exist_ok=True)
  inventory={'memory':subprocess.check_output(['sysctl','-n','hw.memsize'],text=True).strip() if sys.platform=='darwin' else pathlib.Path('/proc/meminfo').read_text(),'filesystem':subprocess.check_output(['df','-h',str(self.dir)],text=True),'physical_storage_model':None}
  report={'host_inventory':inventory,'format':'openump-opcua-pilot/1','source_revision':subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(),'sdk':'github.com/awcullen/opcua@v1.4.0','policy':'Aes256_Sha256_RsaPss/SignAndEncrypt','endpoint_scope':'loopback only','platform':platform.platform(),'machine':platform.machine(),'logical_cpus':os.cpu_count(),'cases':self.cases,'admitted':len(ids),'namespace_indexes_observed':sorted(indices),'source_ledger_matched':matched,'local_ids':sorted(normalized),'remote_ids':sorted(r['message_id'] for r in [json.loads(base64.b64decode(v['payload'])) for v in remote]),'historian_good_series':expected,'clock_uncertainty_ms':None,'physical_device_qualified':False,'deployed_zeus':False,'limits':'software fixture only; 3 volatile observations per poll; normalizer acceptance is volatile; short run does not qualify capacity'}
  (out/'report.json').write_text(json.dumps(report,indent=2)+'\n')
  (out/'observations.jsonl').write_text(''.join(json.dumps(p)+'\n' for p in self.events))
  (out/'source.jsonl').write_text(''.join(json.dumps(p)+'\n' for p in server_records))
  print('OPC UA pilot: 30 observations, ten source/fault/restart/time cases, exact source/status/time/ID preservation, Go+C/Python consumers, history and durable receipts passed')
