#!/usr/bin/env python3
"""Bounded disposable multi-context reference workload. Never a pilot approval."""
import argparse
import base64
import copy
import datetime
import hashlib
import json
import math
import os
import pathlib
import platform
import shutil
import signal
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[2]
BIN = ROOT / 'artifacts'
TOOLS = ('hades-service', 'hades-normalizer', 'hades-history', 'hades-transfer', 'hades-pki', 'hades-consumer')


def command(args, **kw):
    return subprocess.run([str(a) for a in args], check=True, **kw)


def sha(path):
    digest = hashlib.sha256()
    with open(path,'rb') as stream:
        for chunk in iter(lambda:stream.read(1024*1024),b''):
            digest.update(chunk)
    return digest.hexdigest()


def private_json(path, value):
    path.write_text(json.dumps(value, separators=(',', ':')))
    path.chmod(0o600)


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def wait(check, label, timeout=45):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            value = check()
            if value:
                return value
        except (OSError, ValueError) as error:
            last = error
        time.sleep(.1)
    raise RuntimeError(f'{label} timed out: {last}')


def entries(path):
    result = []
    for p in path.glob('*.json'):
        try:
            result.append(json.loads(p.read_bytes())['value'])
        except FileNotFoundError:
            pass  # A committed queue record may retire between enumeration and read.
    if result:
        return result
    records = {}
    for p in sorted(path.glob('segment-*.wal')):
        try:
            data = p.read_bytes()
        except FileNotFoundError:
            continue
        offset = 0
        while offset + 28 <= len(data):
            if data[offset:offset+4] != b'HWAL':
                break
            kind = data[offset+4]
            key_len = int.from_bytes(data[offset+16:offset+20], 'little')
            val_len = int.from_bytes(data[offset+20:offset+24], 'little')
            end = offset + 28 + key_len + val_len
            if end > len(data):
                break
            key = data[offset+28:offset+28+key_len].decode('utf-8', errors='replace')
            if kind == 1:
                try:
                    records[key] = json.loads(data[offset+28+key_len:end])
                except ValueError:
                    break
            elif kind == 2:
                records.pop(key, None)
            offset = end
    return list(records.values())


def events(path):
    result = []
    if path.exists():
        for line in path.read_text().splitlines():
            try:
                result.append(json.loads(line))
            except ValueError:
                pass  # A concurrently written last line is retried on the next sample.
    return result


def logical_bytes(path):
    total = 0
    for p in path.rglob('*'):
        try:
            if p.is_file():
                total += p.stat().st_size
        except FileNotFoundError:
            pass
    return total


def distribution(values):
    ordered = sorted(values)
    return {'samples': len(ordered), **{name: ordered[min(len(ordered)-1, math.ceil(q*len(ordered))-1)] if ordered else None
                                     for name, q in [('p50', .5), ('p95', .95), ('p99', .99)]}}


def process_sample(pid):
    """CPU cumulative seconds and current RSS bytes; missing is never zero."""
    try:
        if sys.platform == 'linux':
            raw = pathlib.Path(f'/proc/{pid}/stat').read_text().rsplit(')', 1)[1].split()
            return {'cpu_seconds': (int(raw[11])+int(raw[12])) / os.sysconf('SC_CLK_TCK'),
                    'rss_bytes': int(raw[21])*os.sysconf('SC_PAGE_SIZE')}
        text = subprocess.check_output(['ps', '-p', str(pid), '-o', 'time=', '-o', 'rss='], text=True).split()
        segments = text[0].split(':')
        seconds = sum(float(n)*60**i for i, n in enumerate(reversed(segments)))
        return {'cpu_seconds': seconds, 'rss_bytes': int(text[1])*1024}
    except (OSError, ValueError, IndexError, subprocess.CalledProcessError):
        return {'cpu_seconds': None, 'rss_bytes': None}


class Run:
    def __init__(self, args, scratch):
        self.args, self.scratch = args, scratch
        self.children, self.handles, self.services, self.units = [], [], {}, []
        self.samples, self.source, self.lifecycle = [], [], []
        self.stop_monitor = threading.Event()
        self.native = bool(os.environ.get('HADES_MOSQUITTO_BINARY'))
        self.component_versions = {}
        if self.native:
            for name, argv, version in [('mosquitto',[os.environ['HADES_MOSQUITTO_BINARY'],'-h'],'2.1.2'), ('victoriametrics',[os.environ['HADES_VM_BINARY'],'-version'],'1.154.0')]:
                result = subprocess.run(argv, capture_output=True, text=True)
                text = result.stdout+result.stderr
                if version not in text:
                    raise RuntimeError('native component version mismatch: '+name)
                self.component_versions[name] = text[:2000]
        self.project = 'hades-capacity-' + uuid.uuid4().hex[:10]
        self.compose = ['docker', 'compose', '-p', self.project, '-f', str(scratch/'deploy/compose.yaml'), '--profile', 'history']
        self.log = self.open('infrastructure.log')
        self.pids = {}
        self.container_ids = {}
        self.deadline = time.monotonic() + args.max_seconds
        self.monitor_error = None
        self.first_seen = {k: {} for k in ('local', 'consumer_0', 'consumer_1', 'remote')}

    def open(self, name):
        f = open(self.scratch/name, 'w'); os.chmod(f.name, 0o600); self.handles.append(f)
        return f

    def spawn(self, name, args, stdout=None, **kwargs):
        p = subprocess.Popen([str(a) for a in args], cwd=ROOT, stdout=stdout or self.log, stderr=self.log, **kwargs)
        self.children.append(p); self.pids[name] = p.pid
        return p

    def flags(self, role):
        return ['-tls-ca', self.pki/'ca.crt', '-tls-cert', self.pki/(role+'.crt'), '-tls-key', self.pki/(role+'.key')]

    def context(self):
        c = ssl.create_default_context(cafile=self.pki/'ca.crt')
        c.load_cert_chain(self.pki/'transfer-adapter.crt', self.pki/'transfer-adapter.key')
        return c

    def gateway_context(self):
        c = ssl.create_default_context(cafile=self.pki/'ca.crt')
        c.load_cert_chain(self.pki/'gateway-press-04.crt', self.pki/'gateway-press-04.key')
        return c

    def sync_opcua_source(self):
        if not self.args.opcua_pilot or 'press-04-gateway' not in self.services:
            return
        log_path = pathlib.Path(self.services['press-04-gateway'][0]['state'])/'events.log'
        seen = {s['message_id'] for s in self.source}
        for event in events(log_path):
            if event.get('type') != 'gateway_admitted':
                continue
            payload = event['payload']
            mid = payload['message_id']
            if mid in seen:
                continue
            seq = int(payload['source']['sequence'])
            phase = 'steady' if seq < self.args.steady_count*3 else 'burst_outage'
            ingested_wall = datetime.datetime.fromisoformat(payload['ingested_at'].replace('Z', '+00:00')).timestamp()
            started = self.gateway_start_mono + (ingested_wall - self.gateway_start_wall)
            if getattr(self, 'first_poll_mono', None) is None and seq == 0:
                self.first_poll_mono = started
            http_ms = float(event.get('http_ms', 0.0))
            finished = started + http_ms/1000.0
            scheduled = getattr(self, 'first_poll_mono', started) + (seq//3)*1.0
            raw = json.dumps(payload, separators=(',', ':')).encode()
            self.source.append({'asset': 'press-04', 'message_id': mid, 'observed_at': payload['observed_at'],
                                'phase': phase, 'scheduled': scheduled, 'started': started, 'finished': finished,
                                'status': 202, 'payload_bytes': len(raw), 'raw_sha256': hashlib.sha256(raw).hexdigest(),
                                'payload': payload})
            seen.add(mid)

    def start(self, name, cfg):
        pathlib.Path(cfg['state']).mkdir(mode=0o700, exist_ok=True)
        path = self.scratch/(name+'.json')
        private_json(path, cfg)
        digest = subprocess.check_output([BIN/'hades-service-native', '-action', 'digest', '-config', path], text=True).strip()
        if self.args.systemd:
            unit = self.project+'-'+name
            text = subprocess.check_output([BIN/'hades-service-native', '-action', 'unit', '-config', path, '-name', unit, '-user', os.environ['USER']], text=True)
            local = self.scratch/(unit+'.service'); local.write_text(text)
            command(['sudo', 'install', '-m', '644', local, '/run/systemd/system/'+unit+'.service'])
            if unit not in self.units:
                self.units.append(unit)
            command(['sudo', 'systemctl', 'daemon-reload'])
            command(['sudo', 'systemctl', 'start', unit])
            proc = None
        else:
            proc = self.spawn(name+'-supervisor', [BIN/'hades-service-native', '-action', 'run', '-config', path, '-digest', digest])
        self.services[name] = (cfg, path, proc)
        state = wait(lambda: self.status(name), name+' live readiness')
        self.pids[name] = state['child_pid']
        self.pids[name+'-supervisor'] = state['supervisor_pid']
        self.lifecycle.append({'action': 'start', 'name': name, 'status': state})
        return state

    def status(self, name):
        result = subprocess.run([BIN/'hades-service-native', '-action', 'status', '-config', self.services[name][1]], capture_output=True, text=True)
        return json.loads(result.stdout) if result.returncode == 0 else None

    def stop(self, name):
        cfg, path, proc = self.services[name]
        if self.args.systemd:
            command(['sudo', 'systemctl', 'stop', self.project+'-'+name])
        else:
            if proc.poll() is None:
                proc.terminate()
            assert proc.wait(timeout=15) == 0
        assert self.status(name) is None, 'stopped service reported live'
        self.pids.pop(name, None)
        self.pids.pop(name+'-supervisor', None)
        self.lifecycle.append({'action': 'stop', 'name': name})

    def definition(self, name, tool, argv, ready, files, finite=False):
        binary = BIN/(tool+'-native')
        cfg = {'revision': 'reference-a', 'binary': str(binary), 'binary_sha256': sha(binary),
               'args': [str(v) for v in argv], 'directory': str(ROOT), 'state': str(self.scratch/(name+'-svc')),
               'ready_pattern': ready, 'files': {str(f): sha(f) for f in list(files)+list((ROOT/'schemas/v1').glob('*.json'))},
               'writable_paths': [str(self.scratch)], 'log_bytes': 65536}
        if finite:
            cfg['finite'] = True
        return cfg

    def setup(self):
        self.mp, self.vp = port(), port()
        self.base = f'http://127.0.0.1:{self.vp}'
        self.broker = f'mqtt://127.0.0.1:{self.mp}'
        # Fresh private ACL enumerates only this workload's asset contexts.
        if self.args.opcua_pilot:
            topics = ['openump/v1/acme/berlin/assembly/line-01/press-04/#']
        else:
            topics = [f'openump/v1/acme/berlin/assembly/line-01/bench-{i:02d}/#' for i in range(self.args.assets)]
        acl = 'topic readwrite hades/dev/health\n'+''.join('topic readwrite '+t+'\n' for t in topics)
        if self.native:
            (self.scratch/'dev.acl').write_text(acl)
            conf = f'listener {self.mp} 127.0.0.1\nallow_anonymous true\nacl_file {self.scratch}/dev.acl\npersistence false\nmax_packet_size 70000\n'
            (self.scratch/'mosquitto.conf').write_text(conf)
            self.spawn('broker', [os.environ['HADES_MOSQUITTO_BINARY'], '-c', self.scratch/'mosquitto.conf'])
            self.spawn('historian', [os.environ['HADES_VM_BINARY'], '-httpListenAddr=127.0.0.1:'+str(self.vp),
                                    '-storageDataPath='+str(self.scratch/'vm'), '-retentionPeriod=1d',
                                    '-dedup.minScrapeInterval=1ms', '-inmemoryDataFlushInterval=1s', '-memory.allowedBytes=128MiB'])
        else:
            for dirname in ('deploy', 'config'):
                shutil.copytree(ROOT/dirname, self.scratch/dirname)
            (self.scratch/'config/broker/dev.acl').write_text(acl)
            env = dict(os.environ, HADES_MQTT_PORT=str(self.mp), HADES_VM_PORT=str(self.vp))
            command(self.compose+['up', '--build', '-d', '--wait'], env=env, stdout=self.log, stderr=self.log)
            # Linux Docker host PIDs support cumulative CPU/RSS sampling.
            for name in ('broker', 'historian'):
                cid = subprocess.check_output(self.compose+['ps', '-q', name], env=env, text=True).strip()
                self.container_ids[name] = cid
                state = json.loads(subprocess.check_output(['docker', 'inspect', cid]))[0]
                self.pids[name] = state['State']['Pid'] if sys.platform == 'linux' else -1
            self.compose_env = env
        wait(lambda: urllib.request.urlopen(self.base+'/health', timeout=1).status == 200, 'historian')
        wait(lambda: socket.create_connection(('127.0.0.1', self.mp), timeout=1).close() or True, 'broker')
        self.historian_start_bytes = self.historian_bytes()
        self.pki = self.scratch/'pki'
        command([BIN/'hades-pki-native', '-out', self.pki], stdout=self.log)
        self.assets = []
        if self.args.opcua_pilot:
            asset = 'press-04'
            metadata = json.loads((ROOT/'examples/site/opcua-mock.metadata.json').read_text())
            path = self.scratch/(asset+'.metadata.json'); private_json(path, metadata)
            common = ['-broker', self.broker, '-config', path]
            if not self.args.no_transfer:
                fault = self.scratch/(asset+'.fault'); fault.write_text('normal')
                cp = port()
                self.spawn(asset+'-receiver', [BIN/'hades-test-receiver-native', '-listen', '127.0.0.1:'+str(cp), '-store', self.scratch/(asset+'-receiver'), '-config', path, '-fault-file', fault]+self.flags('cloud-receiver'))
                wait(lambda: socket.create_connection(('127.0.0.1', cp), timeout=1).close() or True, 'receiver')
                argv = common+['-receiver', f'https://127.0.0.1:{cp}/v1/ingest/observations', '-intake', self.scratch/(asset+'-transfer'), '-quarantine', self.scratch/(asset+'-tq'), '-cloud-ca', self.pki/'ca.crt', '-cloud-cert', self.pki/'transfer-adapter.crt', '-cloud-key', self.pki/'transfer-adapter.key']
                cfg = self.definition(asset+'-transfer', 'hades-transfer', argv, '"type":"transfer_ready"', [path, self.pki/'ca.crt', self.pki/'transfer-adapter.crt', self.pki/'transfer-adapter.key'])
                self.start(asset+'-transfer', cfg)
            argv = common+['-historian', self.base, '-intake', self.scratch/(asset+'-history'), '-quarantine', self.scratch/(asset+'-hq')]
            self.start(asset+'-history', self.definition(asset+'-history', 'hades-history', argv, '"type":"history_ready"', [path]))
            expected_obs = self.args.count * 3
            output = self.open(asset+'-primary.jsonl')
            primary = self.spawn(asset+'-primary', [BIN/'hades-consumer-native']+common+['-count', str(expected_obs), '-timeout', str(self.args.max_seconds)+'s'], stdout=output)
            wait(lambda: any(e.get('type') == 'ready' for e in events(pathlib.Path(output.name))), 'Go consumer')
            prefix = f'openump/v1/acme/berlin/assembly/line-01/{asset}/+'
            transport = [os.environ['HADES_MQTT_SUB'], '-h', '127.0.0.1', '-p', str(self.mp)] if self.native else self.compose+['exec', '-T', 'broker', 'mosquitto_sub', '-h', '127.0.0.1', '-p', '1883']
            sub = self.spawn(asset+'-c-transport', transport+['-V', 'mqttv5', '-q', '1', '-t', prefix, '-F', '%j', '-d'], stdout=subprocess.PIPE, env=getattr(self, 'compose_env', os.environ))
            second = self.open(asset+'-secondary.jsonl')
            secondary = self.spawn(asset+'-secondary', [sys.executable, ROOT/'examples/consumer-secondary/consumer.py', '--config', path, '--count', str(expected_obs)], stdin=sub.stdout, stdout=second)
            sub.stdout.close()
            np = port()
            argv = common+['-signal-profile', 'opcua-mock', '-listen', '127.0.0.1:'+str(np), '-quarantine', self.scratch/(asset+'-nq'),
                           '-ingress-ca', self.pki/'ca.crt', '-ingress-cert', self.pki/'ingress.crt', '-ingress-key', self.pki/'ingress.key']
            self.start(asset+'-normalizer', self.definition(asset+'-normalizer', 'hades-normalizer', argv, 'normalizer ready',
                                                            [path, self.pki/'ca.crt', self.pki/'ingress.crt', self.pki/'ingress.key']))
            wait(lambda: any(e.get('type') == 'ready' for e in events(pathlib.Path(second.name))), 'independent consumer')
            self.opcua_pki = self.scratch/'opcua-pki'
            command([BIN/'hades-opcua-mock-native', '-action', 'init', '-pki', self.opcua_pki], stdout=self.log)
            self.opcua_fault = self.scratch/'opcua-fault'; self.opcua_fault.write_text('good')
            self.opcua_ledger = self.scratch/'opcua-source.jsonl'
            self.op = port()
            self.opcua_endpoint = f'opc.tcp://127.0.0.1:{self.op}'
            self.spawn('opcua-mock', [BIN/'hades-opcua-mock-native', '-endpoint', self.opcua_endpoint, '-pki', self.opcua_pki, '-fault-file', self.opcua_fault, '-ledger', self.opcua_ledger])
            wait(lambda: socket.create_connection(('127.0.0.1', self.op), timeout=1).close() or True, 'OPC UA mock server')
            self.assets.append({'name': asset, 'metadata': metadata, 'config_path': path, 'url': f'https://127.0.0.1:{np}/observations', 'outputs': [pathlib.Path(output.name), pathlib.Path(second.name)], 'consumers': [primary, secondary]})
        else:
            for i in range(self.args.assets):
                asset = f'bench-{i:02d}'
                metadata = json.loads((ROOT/'examples/site/metadata.json').read_text())
                metadata['asset_id'] = 'urn:openump:acme:berlin:'+asset
                metadata['message_id'] = str(uuid.uuid4()); metadata['hierarchy']['asset'] = asset
                template = metadata['measurements']['motor_temperature']
                for measurement in range(1, self.args.measurements):
                    metadata['measurements'][f'temperature_{measurement:02d}'] = copy.deepcopy(template)
                path = self.scratch/(asset+'.metadata.json'); private_json(path, metadata)
                common = ['-broker', self.broker, '-config', path]
                if not self.args.no_transfer:
                    fault = self.scratch/(asset+'.fault'); fault.write_text('normal')
                    cp = port()
                    self.spawn(asset+'-receiver', [BIN/'hades-test-receiver-native', '-listen', '127.0.0.1:'+str(cp), '-store', self.scratch/(asset+'-receiver'), '-config', path, '-fault-file', fault]+self.flags('cloud-receiver'))
                    wait(lambda: socket.create_connection(('127.0.0.1', cp), timeout=1).close() or True, 'receiver')
                    argv = common+['-receiver', f'https://127.0.0.1:{cp}/v1/ingest/observations', '-intake', self.scratch/(asset+'-transfer'), '-quarantine', self.scratch/(asset+'-tq'), '-cloud-ca', self.pki/'ca.crt', '-cloud-cert', self.pki/'transfer-adapter.crt', '-cloud-key', self.pki/'transfer-adapter.key']
                    cfg = self.definition(asset+'-transfer', 'hades-transfer', argv, '"type":"transfer_ready"', [path, self.pki/'ca.crt', self.pki/'transfer-adapter.crt', self.pki/'transfer-adapter.key'])
                    self.start(asset+'-transfer', cfg)
                argv = common+['-historian', self.base, '-intake', self.scratch/(asset+'-history'), '-quarantine', self.scratch/(asset+'-hq')]
                self.start(asset+'-history', self.definition(asset+'-history', 'hades-history', argv, '"type":"history_ready"', [path]))
                output = self.open(asset+'-primary.jsonl')
                primary = self.spawn(asset+'-primary', [BIN/'hades-consumer-native']+common+['-count', str(self.args.count), '-timeout', str(self.args.max_seconds)+'s'], stdout=output)
                wait(lambda: any(e.get('type') == 'ready' for e in events(pathlib.Path(output.name))), 'Go consumer')
                # Independent C transport + Python schema/context implementation.
                prefix = f'openump/v1/acme/berlin/assembly/line-01/{asset}/+'
                transport = [os.environ['HADES_MQTT_SUB'], '-h', '127.0.0.1', '-p', str(self.mp)] if self.native else self.compose+['exec', '-T', 'broker', 'mosquitto_sub', '-h', '127.0.0.1', '-p', '1883']
                sub = self.spawn(asset+'-c-transport', transport+['-V', 'mqttv5', '-q', '1', '-t', prefix, '-F', '%j', '-d'], stdout=subprocess.PIPE, env=getattr(self, 'compose_env', os.environ))
                second = self.open(asset+'-secondary.jsonl')
                secondary = self.spawn(asset+'-secondary', [sys.executable, ROOT/'examples/consumer-secondary/consumer.py', '--config', path, '--count', str(self.args.count)], stdin=sub.stdout, stdout=second)
                sub.stdout.close()
                np = port()
                argv = common+['-signal-profile', 'approved-numeric', '-listen', '127.0.0.1:'+str(np), '-quarantine', self.scratch/(asset+'-nq')]
                self.start(asset+'-normalizer', self.definition(asset+'-normalizer', 'hades-normalizer', argv, 'normalizer ready', [path, ROOT/'config/normalization/temperature.blobl']))
                wait(lambda: any(e.get('type') == 'ready' for e in events(pathlib.Path(second.name))), 'independent consumer')
                self.assets.append({'name': asset, 'metadata': metadata, 'url': f'http://127.0.0.1:{np}/observations', 'outputs': [pathlib.Path(output.name), pathlib.Path(second.name)], 'consumers': [primary, secondary]})
        # Negative context tests remain on the actual ingress.
        invalid = json.loads((ROOT/'examples/raw-observations/valid.json').read_text())
        self.rejections = []
        ssl_ctx = self.gateway_context() if self.args.opcua_pilot else None
        for payload in (b'{', json.dumps(invalid).encode()):
            try:
                urllib.request.urlopen(urllib.request.Request(self.assets[0]['url'], data=payload), timeout=3, context=ssl_ctx)
                raise AssertionError('malformed/unapproved context accepted')
            except urllib.error.HTTPError as error:
                assert 400 <= error.code < 500
                self.rejections.append(error.code)

    def historian_bytes(self):
        if self.native:
            return logical_bytes(self.scratch/'vm')
        raw = subprocess.check_output(['docker', 'exec', self.container_ids['historian'], 'du', '-sb', '/storage'], text=True)
        return int(raw.split()[0])

    def worker_stats(self, name):
        path = pathlib.Path(self.services[name][0]['state'])/'events.log'
        return next((e['stats'] for e in reversed(events(path)) if 'stats' in e), {})

    def monitor(self):
        try:
            while not self.stop_monitor.is_set():
                if time.monotonic() > self.deadline:
                    raise RuntimeError('workload wall-clock bound exceeded')
                start = time.monotonic()
                self.sync_opcua_source()
                procs = {name: process_sample(pid) for name, pid in list(self.pids.items())}
                disk = {name: logical_bytes(self.scratch/name) for name in ['vm']+[a['name']+'-'+kind for a in self.assets for kind in ('history', 'transfer', 'receiver')]}
                if not self.native:
                    disk['vm'] = self.historian_bytes()
                for asset in self.assets:
                    for raw, value in self.local(asset).items():
                        if value['state'] == 'api_accepted':
                            self.first_seen['local'].setdefault(json.loads(raw)['message_id'], time.monotonic())
                    for kind in (0, 1):
                        for event in self.telemetry(asset, kind):
                            self.first_seen['consumer_'+str(kind)].setdefault(event['message_id'], time.monotonic())
                    if not self.args.no_transfer:
                        for value in entries(self.scratch/(asset['name']+'-receiver')):
                            record = value['record']
                            if record['topic'].endswith('/telemetry'):
                                self.first_seen['remote'].setdefault(record['message_id'], time.monotonic())
                self.samples.append({'at': start, 'processes': procs, 'logical_bytes': disk, 'sampling_seconds': time.monotonic()-start})
                if logical_bytes(self.scratch)+(disk.get('vm',0) if not self.native else 0) > self.args.max_disk_mib*1024**2:
                    raise RuntimeError('private run disk bound exceeded')
                self.stop_monitor.wait(self.args.sample_interval)
        except Exception as error:
            self.monitor_error = error

    def submit(self, asset, index, phase, scheduled):
        if self.monitor_error:
            raise self.monitor_error
        payload = json.loads((ROOT/'examples/raw-observations/valid.json').read_text())
        payload['signals'] = {name: {'value': 67.2, 'unit': definition['unit'], 'quality': 'good'} for name, definition in asset['metadata']['measurements'].items()}
        payload['asset_id'] = asset['metadata']['asset_id']; payload['message_id'] = str(uuid.uuid4())
        now = datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='milliseconds').replace('+00:00', 'Z')
        payload.update(observed_at=now, ingested_at=now); payload['source']['sequence'] = index+1
        raw = json.dumps(payload, separators=(',', ':')).encode()
        raw += b' '*max(0, self.args.envelope_bytes-len(raw))
        before = time.monotonic()
        request = urllib.request.Request(asset['url'], data=raw)
        try:
            with urllib.request.urlopen(request, timeout=5) as response:
                status = response.status
        except urllib.error.HTTPError as error:
            status = error.code
        self.source.append({'asset': asset['name'], 'message_id': payload['message_id'], 'observed_at': now,
                            'phase': phase, 'scheduled': scheduled, 'started': before, 'finished': time.monotonic(),
                            'status': status, 'payload_bytes': len(raw), 'raw_sha256': hashlib.sha256(raw).hexdigest()})
        assert status == 202, f'workload refused {status}'

    def local(self, asset):
        return {base64.b64decode(v['payload']): v for v in entries(self.scratch/(asset['name']+'-history'))}

    def telemetry(self, asset, kind):
        return [e for e in events(asset['outputs'][kind]) if e.get('type') == 'telemetry']

    def exercise_limits(self):
        tool = BIN/'hades-service-probe-native'
        for mode in ('cpu', 'memory'):
            name = 'limit-'+mode
            cfg = {'revision':'limit-probe', 'binary':str(tool), 'binary_sha256':sha(tool), 'args':['-mode',mode],
                   'directory':str(ROOT), 'state':str(self.scratch/(name+'-svc')), 'ready_pattern':'probe_ready',
                   'files':{}, 'writable_paths':[str(self.scratch)], 'log_bytes':65536}
            self.start(name,cfg)
            unit = self.project+'-'+name
            def property_value(key):
                return subprocess.check_output(['systemctl','show',unit,'-p',key,'--value'],text=True).strip()
            if mode == 'cpu':
                before = int(property_value('CPUUsageNSec')); start = time.monotonic()
                time.sleep(2)
                used = (int(property_value('CPUUsageNSec'))-before)/1e9
                elapsed = time.monotonic()-start
                assert 0 < used <= elapsed*.85, ('CPU quota not enforced',used,elapsed)
                evidence = {'cpu_seconds':used, 'elapsed_seconds':elapsed, 'quota_core_equivalents':.5}
            else:
                wait(lambda:int(property_value('NRestarts'))>=1,'cgroup memory limit restart',timeout=20)
                evidence = {'restart_count':int(property_value('NRestarts')), 'memory_peak_bytes':property_value('MemoryPeak'),
                            'journal_tail':subprocess.check_output(['sudo','journalctl','-u',unit,'-n','12','--no-pager'],text=True)}
                assert 'oom' in evidence['journal_tail'].lower(), evidence
            self.stop(name)
            files = [pathlib.Path(cfg['state'])/p for p in ('events.log','events.log.1')]
            assert all(p.exists() and p.stat().st_size<=65536 and p.stat().st_mode&0o777==0o600 for p in files)
            self.lifecycle.append({'action':'resource_limit_exercised','mode':mode,'evidence':evidence,'bounded_logs_bytes':[p.stat().st_size for p in files]})

    def run_outage_upgrade_and_replay(self, min_pending):
        self.outage = {'pending': sum(len(entries(self.scratch/(a['name']+'-transfer'))) for a in self.assets),
                       'logical_file_bytes': sum(logical_bytes(self.scratch/(a['name']+'-transfer')) for a in self.assets),
                       'accounted_bytes_from_records': sum(sum(p.stat().st_size+4096 for p in (self.scratch/(a['name']+'-transfer')).glob('*.json')) for a in self.assets),
                       'local_accepted': sum(len(self.local(a)) for a in self.assets),
                       'worker_stats': {a['name']: self.worker_stats(a['name']+'-transfer') for a in self.assets}}
        assert self.outage['pending'] >= min_pending, (self.outage['pending'], min_pending)
        # A compatible upgrade and rollback while cloud work is owned.
        name = self.assets[0]['name']+'-transfer'
        before = {v['message_id'] for v in entries(self.scratch/name)}
        cfg = copy.deepcopy(self.services[name][0])
        # A partially applied definition must not report the old child as current.
        changed = copy.deepcopy(cfg); changed['revision'] = 'unapplied'
        private_json(self.services[name][1], changed)
        assert self.status(name) is None
        private_json(self.services[name][1], cfg)
        assert self.status(name)
        self.stop(name)
        invalid = copy.deepcopy(cfg); invalid['binary_sha256'] = '0'*64
        bad = self.scratch/'failed.json'; private_json(bad, invalid)
        failed = subprocess.run([BIN/'hades-service-native', '-action', 'run', '-config', bad, '-digest', '0'*64], capture_output=True)
        assert failed.returncode != 0 and before <= {v['message_id'] for v in entries(self.scratch/name)}
        self.lifecycle.append({'action': 'failed_start_preserved_ids', 'count': len(before)})
        upgraded = copy.deepcopy(cfg); upgraded['revision'] = 'reference-b'
        self.start(name, upgraded); assert before <= {v['message_id'] for v in entries(self.scratch/name)}
        self.stop(name); self.start(name, cfg)
        assert before <= {v['message_id'] for v in entries(self.scratch/name)}
        self.replay_start = time.monotonic()
        for asset in self.assets:
            (self.scratch/(asset['name']+'.fault')).write_text('normal')

    def run(self):
        self.setup()
        if self.args.systemd:
            self.exercise_limits()
            name = self.assets[0]['name']+'-normalizer'
            old = self.status(name)['child_pid']
            unit = self.project+'-'+name
            command(['sudo', 'systemctl', 'kill', '--kill-whom=all', '--signal=SIGKILL', unit])
            state = wait(lambda: (v if (v := self.status(name)) and v['child_pid'] != old else None), 'systemd automatic restart')
            self.pids[name] = state['child_pid']
            limits = subprocess.check_output(['systemctl', 'show', unit, '-p', 'CPUQuotaPerSecUSec', '-p', 'MemoryMax', '-p', 'MemorySwapMax', '-p', 'TasksMax', '-p', 'NRestarts'], text=True)
            assert 'MemoryMax=536870912' in limits and 'MemorySwapMax=0' in limits and 'TasksMax=64' in limits
            self.lifecycle.append({'action': 'systemd_kill_restart', 'old_pid': old, 'status': state, 'limits': limits})
        thread = threading.Thread(target=self.monitor); thread.start()
        self.measurement_start = time.monotonic()
        try:
            split = self.args.steady_count
            if self.args.opcua_pilot:
                asset = self.assets[0]
                gw_flags = self.flags('gateway-press-04')
                gw_argv = ['-endpoint', self.opcua_endpoint, '-opcua-pki', self.opcua_pki, '-config', asset['config_path'],
                           '-ingress', asset['url'], '-count', str(self.args.count)] + gw_flags
                gw_files = [asset['config_path'], self.opcua_pki/'server.crt', self.opcua_pki/'client.crt', self.opcua_pki/'client.key',
                            self.pki/'ca.crt', self.pki/'gateway-press-04.crt', self.pki/'gateway-press-04.key']
                gw_cfg = self.definition('press-04-gateway', 'hades-opcua-gateway', gw_argv, 'opcua_gateway_ready', gw_files, finite=True)
                self.gateway_start_mono = time.monotonic()
                self.gateway_start_wall = time.time()
                self.start('press-04-gateway', gw_cfg)
                steady_obs = split * 3
                total_obs = self.args.count * 3
                wait(lambda: (self.sync_opcua_source() or True) and len(self.source) >= steady_obs and len(self.local(asset)) >= steady_obs and all(v['state'] == 'api_accepted' for v in self.local(asset).values()) and (self.args.no_transfer or len(entries(self.scratch/'press-04-receiver')) >= steady_obs), 'OPC UA steady phase completion')
                if not self.args.no_transfer:
                    (self.scratch/'press-04.fault').write_text('offline')
                wait(lambda: (self.sync_opcua_source() or True) and len(self.source) == total_obs and len(self.local(asset)) == total_obs and all(v['state'] == 'api_accepted' for v in self.local(asset).values()) and len(self.telemetry(asset, 0)) == total_obs and len(self.telemetry(asset, 1)) == total_obs, 'OPC UA local path independent of cloud')
                if not self.args.no_transfer:
                    burst_obs = (self.args.count - split) * 3
                    wait(lambda: len(entries(self.scratch/'press-04-transfer')) >= burst_obs, 'OPC UA transfer outage queue accumulation')
                    self.run_outage_upgrade_and_replay(burst_obs)
                self.stop('press-04-gateway')
            else:
                for phase, count, rate in [('steady', split, self.args.rate), ('burst_outage', self.args.count-split, self.args.burst_rate)]:
                    if phase == 'burst_outage' and not self.args.no_transfer:
                        for asset in self.assets:
                            (self.scratch/(asset['name']+'.fault')).write_text('offline')
                    phase_start = time.monotonic()
                    for sequence in range(count):
                        for asset in self.assets:
                            schedule = phase_start + (sequence*len(self.assets)+self.assets.index(asset))/rate
                            time.sleep(max(0, schedule-time.monotonic()))
                            self.submit(asset, sequence+(0 if phase == 'steady' else split), phase, schedule)
                    expected = split if phase == 'steady' else self.args.count
                    wait(lambda: all(len(self.local(a)) == expected and all(v['state'] == 'api_accepted' for v in self.local(a).values()) and len(self.telemetry(a, 0)) == expected and len(self.telemetry(a, 1)) == expected for a in self.assets), 'local path independent of cloud')
                    if phase == 'burst_outage' and not self.args.no_transfer:
                        self.run_outage_upgrade_and_replay(count*len(self.assets))
            if not self.args.no_transfer:
                wait(lambda: all(not entries(self.scratch/(a['name']+'-transfer')) for a in self.assets), 'durable receipt replay', timeout=60)
                self.replay_seconds = time.monotonic()-self.replay_start
            for a in self.assets:
                for consumer in a['consumers']:
                    assert consumer.wait(timeout=5) == 0
            time.sleep(self.args.sample_interval*2)
            self.historian_end_bytes = self.historian_bytes()
            self.measurement_end = time.monotonic()
            self.verify()
        finally:
            self.stop_monitor.set(); thread.join(timeout=5)
        if self.monitor_error:
            raise self.monitor_error
        return self.report()

    def verify(self):
        self.comparison = []
        for a in self.assets:
            source = {s['message_id'] for s in self.source if s['asset'] == a['name']}
            local = {json.loads(raw)['message_id']: (raw, v) for raw, v in self.local(a).items()}
            rows = {'source': sorted(source), 'local': sorted(local)}
            for kind in (0, 1):
                data = self.telemetry(a, kind)
                ids = [e['message_id'] for e in data]
                assert set(ids) == source
                rows['consumer_'+str(kind)] = {'unique': len(set(ids)), 'duplicate_events': len(ids)-len(set(ids)), 'missing_ids': sorted(source-set(ids))}
                assert {e['observed_at'] for e in data} == {s['observed_at'] for s in self.source if s['asset'] == a['name']}
            assert source == set(local)
            if not self.args.no_transfer:
                remote = {v['record']['message_id']: base64.b64decode(v['record']['payload']) for v in entries(self.scratch/(a['name']+'-receiver')) if v['record']['topic'].endswith('/telemetry')}
                assert set(remote) == source
                assert all(remote[k] == local[k][0] for k in source)
                rows['remote'] = sorted(remote)
            query = urllib.parse.urlencode({'match[]': f'hades_observation_value{{asset_id="{a["metadata"]["asset_id"]}"}}'})
            def exported():
                raw = urllib.request.urlopen(self.base+'/api/v1/export?'+query, timeout=3).read()
                return [json.loads(line) for line in raw.splitlines()]
            if self.args.opcua_pilot:
                expected_series = {name: {} for name in ('motor_temperature', 'cycle_counter', 'machine_state')}
                for raw_bytes, _ in local.values():
                    payload = json.loads(raw_bytes)
                    for name, reading in payload['measurements'].items():
                        if reading['quality'] == 'good':
                            ts = int(round(datetime.datetime.fromisoformat(payload['observed_at'].replace('Z', '+00:00')).timestamp()*1000))
                            expected_series[name][ts] = reading['value']
                def complete_opcua_series():
                    values = exported()
                    actual = {row['metric']['measurement']: dict(zip(row['timestamps'], row['values'])) for row in values}
                    return values if actual == expected_series else None
                wait(complete_opcua_series, 'all OPC UA historian series/timestamps')
                server_records = [json.loads(line) for line in self.opcua_ledger.read_text().splitlines()]
                matched = 0
                for s in self.source:
                    payload = s['payload']
                    ext = payload['extensions']
                    matches = [r for r in server_records if r['node'] == ext['opcua.node_id'] and r['source_timestamp'] == ext['opcua.source_timestamp'] and r['server_timestamp'] == ext['opcua.server_timestamp']]
                    assert matches, (ext, len(server_records))
                    assert any(r['status_code'] == ext['opcua.status_code'] for r in matches)
                    reading = next(iter(payload['signals'].values()))
                    assert any(r['value'] == reading['value'] for r in matches)
                    matched += 1
                rows['opcua_source_ledger_matched'] = matched
            else:
                expected = {int(round(datetime.datetime.fromisoformat(s['observed_at'].replace('Z', '+00:00')).timestamp()*1000)) for s in self.source if s['asset'] == a['name']}
                def complete_series():
                    values = exported()
                    return values if len(values) == self.args.measurements and all(set(row['timestamps']) == expected for row in values) else None
                values = wait(complete_series, 'all numeric historian series/timestamps')
                assert all(v == 67.2 for row in values for v in row['values'])
            rows['asset'] = a['name']; self.comparison.append(rows)

    def report(self):
        elapsed = self.measurement_end-self.measurement_start
        components = {}
        for name in {n for s in self.samples for n in s['processes']}:
            values = [(s['at'], s['processes'][name]) for s in self.samples if name in s['processes']]
            cpu = [v['cpu_seconds'] for _, v in values if v['cpu_seconds'] is not None]
            rss = [v['rss_bytes'] for _, v in values if v['rss_bytes'] is not None]
            # Restarts reset CPU: accumulate only nonnegative adjacent deltas.
            seconds = sum(max(0, b-a) for a, b in zip(cpu, cpu[1:])) if cpu else None
            components[name] = {'sampled_cpu_seconds_delta': seconds, 'mean_core_equivalents': seconds/elapsed if seconds is not None else None,
                                'sampled_peak_rss_bytes': max(rss) if rss else None, 'samples': len(values), 'missing_cpu_samples': len(values)-len(cpu)}
        disk = {name: {'first_bytes': self.samples[0]['logical_bytes'].get(name), 'last_bytes': self.samples[-1]['logical_bytes'].get(name)} for name in self.samples[-1]['logical_bytes']}
        disk['vm'] = {'first_bytes': self.historian_start_bytes, 'last_bytes': self.historian_end_bytes, 'method': 'sum logical file lengths' if self.native else 'container du apparent bytes including directories'}
        history_growth = sum(v['last_bytes']-v['first_bytes'] for k, v in disk.items() if k.endswith('-history'))
        free = shutil.disk_usage(self.scratch).free
        pending = getattr(self, 'outage', {}).get('pending', 0)
        logical_pending_bytes = getattr(self, 'outage', {}).get('logical_file_bytes', 0)
        accounted = getattr(self, 'outage', {}).get('accounted_bytes_from_records', 0)
        bytes_per_pending = accounted/pending if pending else None
        logical_bytes_per_pending = logical_pending_bytes/pending if pending else None
        # Each asset owns its own byte/record cap. Shared disk reserve is counted
        # once; all of these are illustrative short-run extrapolations.
        remaining_records = max(0, 100000*self.args.assets-pending)
        remaining_bytes = max(0, 256*1024**2*self.args.assets-accounted)
        outage_seconds = min(remaining_records, remaining_bytes/bytes_per_pending, max(0,free-134217728)/bytes_per_pending)/self.args.rate if bytes_per_pending else None
        vm_bytes_per_obs = max(0, self.historian_end_bytes-self.historian_start_bytes)/len(self.source)
        vm_bytes_per_day = vm_bytes_per_obs*self.args.rate*86400
        effective_records_per_asset = min(100000, int((256*1024**2)//bytes_per_pending)) if bytes_per_pending else None
        v1_cap_hours = (effective_records_per_asset*self.args.assets)/self.args.rate/3600 if effective_records_per_asset else None
        tier2_model = {
            'target_profile': 'Tier 2 Standard Line IPC (4-core linux/amd64, 4-8 GB RAM, 256 GB Industrial M.2 NVMe / SATA SSD with PLP)',
            'victoria_metrics_retention': {
                'default_configured_retention_period': '7d (deploy/compose.yaml)',
                'historian_backfill_window_cap': '24h (internal/history/history.go WindowCheck)',
                'projected_bytes_per_day_at_steady_rate': vm_bytes_per_day,
                'projected_30d_bytes_at_steady_rate': vm_bytes_per_day*30,
                'projected_90d_bytes_at_steady_rate': vm_bytes_per_day*90,
            },
            'transfer_outage_capacity': {
                'v1_per_asset_record_cap': 100000,
                'v1_per_asset_accounted_byte_cap': 268435456,
                'v1_effective_records_per_asset_at_measured_record_size': effective_records_per_asset,
                'v1_max_outage_hours_at_steady_rate': v1_cap_hours,
                'target_7d_outage_required_records_total': self.args.rate*86400*7,
                'target_7d_outage_required_logical_bytes': logical_bytes_per_pending*self.args.rate*86400*7 if logical_bytes_per_pending else None,
                'target_7d_outage_required_accounted_bytes': bytes_per_pending*self.args.rate*86400*7 if bytes_per_pending else None,
            },
            'history_intake_dedup_window': {
                'retained_api_accepted_ttl_days': 7,
                'v1_per_asset_record_cap': 100000,
                'v1_per_asset_accounted_byte_cap': 268435456,
                'v1_hours_until_dedup_store_full_at_steady_rate': v1_cap_hours,
                'constraint': 'internal/history/history.go retains api_accepted records for 7 days in the same 100,000-record / 256 MiB flat-file store',
            },
            'ssd_endurance_model': {
                'v1_synced_file_mutations_per_observation': 4,
                'v1_fsync_calls_per_observation': 7,
                'assumed_physical_bytes_per_synced_mutation_range': [16384, 65536],
                'projected_daily_physical_write_bytes_range': [self.args.rate*86400*4*16384, self.args.rate*86400*4*65536],
                'projected_annual_tbw_range': [self.args.rate*86400*365*4*16384/1e12, self.args.rate*86400*365*4*65536/1e12],
                'tier2_reference_ssd_tbw_rating': 150.0,
            },
        }
        inventory = {'platform': platform.platform(), 'machine': platform.machine(), 'logical_cpus': os.cpu_count(), 'host_label': self.args.host_label,
                     'filesystem': subprocess.check_output(['df', '-T' if sys.platform == 'linux' else '-h', str(self.scratch)], text=True),
                     'storage_model': None, 'physical_write_bytes': None, 'disk_free_bytes': free}
        try:
            inventory['memory'] = pathlib.Path('/proc/meminfo').read_text() if sys.platform == 'linux' else subprocess.check_output(['sysctl', '-n', 'hw.memsize'], text=True)
            inventory['cpu_model'] = pathlib.Path('/proc/cpuinfo').read_text().split('\n\n')[0] if sys.platform == 'linux' else subprocess.check_output(['sysctl', '-n', 'machdep.cpu.brand_string'], text=True).strip()
        except (OSError, subprocess.CalledProcessError):
            pass
        binaries = {t: sha(BIN/(t+'-native')) for t in TOOLS}
        if self.args.opcua_pilot:
            for extra in ('hades-opcua-gateway', 'hades-opcua-mock'):
                binaries[extra] = sha(BIN/(extra+'-native'))
        omitted = (['transfer and receipt receiver'] if self.args.no_transfer else []) + (
            ['physical PLC', 'deployed Zeus', 'production network', 'history TLS proxy (separately covered by smoke-security)']
            if self.args.opcua_pilot else
            ['OPC UA acquisition', 'physical PLC', 'deployed Zeus', 'production network', 'history TLS proxy (separately covered by smoke-security)']
        )
        return {'format': 'openump-stack-capacity/1', 'scope': 'opcua_software_pilot_stack' if self.args.opcua_pilot else 'disposable_synthetic_multi_context_stack', 'pilot_qualified': False,
                'source_revision': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
                'working_tree_dirty': bool(subprocess.check_output(['git', 'status', '--porcelain'], cwd=ROOT)),
                'binaries_sha256': binaries, 'go_version': subprocess.check_output(['go', 'version'], text=True).strip(), 'normalizer_build_info': subprocess.check_output(['go','version','-m',BIN/'hades-normalizer-native'],text=True),
                'component_version_output_native': self.component_versions, 'components_pinned': {'mosquitto': '2.1.2', 'victoriametrics': '1.154.0'}, 'inventory': inventory,
                'workload': {**vars(self.args), 'output': str(self.args.output)}, 'measurement_seconds': elapsed, 'warmup': 'all services live and both consumers subscribed; no warmup observations',
                'numeric_label_sets': self.args.assets*self.args.measurements, 'measurements_per_asset': self.args.measurements,
                'achieved_phase_admission_per_second': {phase: len(items)/(max(v['finished'] for v in items)-min(v['started'] for v in items)) for phase in ('steady','burst_outage') if (items := [v for v in self.source if v['phase']==phase])},
                'scheduled_phases': {'steady_per_second_total': self.args.rate, 'burst_per_second_total': self.args.burst_rate},
                'admitted': len(self.source), 'refused': sum(s['status'] != 202 for s in self.source),
                'achieved_admission_per_second': len(self.source)/(max(s['finished'] for s in self.source)-min(s['started'] for s in self.source)),
                'http_admission_ms': distribution([(s['finished']-s['started'])*1000 for s in self.source]),
                'sampled_first_visibility_ms': {kind: {**distribution([(seen[s['message_id']]-s['started'])*1000 for s in self.source if s['message_id'] in seen]), 'missing_observations': sum(s['message_id'] not in seen for s in self.source), 'method': 'upper bound from periodic file/consumer scan; includes sampling lag'} for kind, seen in self.first_seen.items()},
                'schedule_lag_ms': distribution([(s['started']-s['scheduled'])*1000 for s in self.source]),
                'components': components, 'worker_stats_process_lifetime': {name: self.worker_stats(name) for name in self.services}, 'disk': disk, 'outage': getattr(self, 'outage', None), 'replay_seconds': getattr(self, 'replay_seconds', None),
                'replay_records_per_second': self.outage['pending']/self.replay_seconds if not self.args.no_transfer else None,
                'sizing_worksheet': {'history_adapter_growth_bytes_per_observation': history_growth/len(self.source), 'historian_growth_bytes': self.historian_end_bytes-self.historian_start_bytes,
                                     'queue_pending_file_bytes_at_outage': getattr(self, 'outage', {}).get('logical_file_bytes'), 'suspended_records': sum(self.worker_stats(a['name']+'-transfer').get('suspended',0) for a in self.assets) if not self.args.no_transfer else None,
                                     'illustrative_historian_bytes_per_observation': vm_bytes_per_obs,
                                     'illustrative_history_bytes_per_day_at_requested_steady_rate': vm_bytes_per_day,
                                     'illustrative_transfer_seconds_at_steady_rate': outage_seconds,
                                     'outage_accounted_bytes_from_records': accounted, 'accounted_bytes_per_pending_record': bytes_per_pending,
                                     'logical_bytes_per_pending_record': logical_bytes_per_pending,
                                     'pending_accounted_bytes_at_outage_sample': sum(v.get('store',{}).get('bytes',0) for v in getattr(self,'outage',{}).get('worker_stats',{}).values()),
                                     'queue_accounting_caveat': 'Synced record accounting uses file length plus runtime 4096-byte overhead per record. Outage worker stats are periodic snapshots and may lag peak occupancy; logical files are separately scanned. Accounted storage includes per-record overhead.',
                                     'reserve_bytes_per_store': 134217728, 'free_after_reserve_bytes': max(0, free-134217728),
                                     'tier2_reference_ipc_model': tier2_model,
                                     'uncertainty': 'Short run includes startup/metadata/segment overhead. Logical file length is not physical allocation or media wear. No sustainable retention/outage target inferred.'},
                'sampling': {'interval_seconds': self.args.sample_interval, 'total_overhead_seconds': sum(s['sampling_seconds'] for s in self.samples), 'rss_aggregate_caveat': 'Per-process RSS includes shared pages; summing overcounts memory.'},
                'lifecycle': self.lifecycle, 'negative_ingress_status': self.rejections, 'transfer_state': 'disabled' if self.args.no_transfer else 'independently_verified_fixture_receipts',
                'omitted': omitted,
                'not_measured': ['physical writes/endurance', 'sub-sample latency'],
                'id_comparison': self.comparison}

    def close(self):
        self.stop_monitor.set()
        for name in list(self.services)[::-1]:
            try:
                self.stop(name)
            except (OSError, AssertionError, subprocess.CalledProcessError, subprocess.TimeoutExpired):
                pass
        for p in self.children[::-1]:
            if p.poll() is None:
                p.terminate()
            try:
                p.wait(timeout=10)
            except subprocess.TimeoutExpired:
                p.kill(); p.wait()
        if self.units:
            for unit in self.units:
                command(['sudo', 'systemctl', 'stop', unit])
                command(['sudo', 'rm', '-f', '/run/systemd/system/'+unit+'.service'])
            command(['sudo', 'systemctl', 'daemon-reload'])
        if not self.native:
            command(self.compose+['down', '-v'], env=getattr(self, 'compose_env', os.environ), stdout=self.log, stderr=self.log)
        for handle in self.handles:
            handle.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--opcua-pilot', action='store_true', help='measure 1-asset press-04 OPC UA read-only gateway + mock PLC software pilot stack')
    parser.add_argument('--assets', type=int, default=2)
    parser.add_argument('--measurements', type=int, default=1, help='approved numeric measurements per asset (1..32)')
    parser.add_argument('--burst-seconds', type=float, help='requested burst duration; rounds up to whole per-asset cycles')
    parser.add_argument('--count', type=int, default=8, help='observations per asset (or OPC UA poll cycles with --opcua-pilot), including burst')
    parser.add_argument('--rate', type=float, default=4, help='total steady observations/sec')
    parser.add_argument('--burst-rate', type=float, default=12)
    parser.add_argument('--envelope-bytes', type=int, default=1024, help='minimum valid JSON length, whitespace padded')
    parser.add_argument('--sample-interval', type=float, default=.5)
    parser.add_argument('--max-seconds', type=int, default=240)
    parser.add_argument('--max-disk-mib', type=int, default=512)
    parser.add_argument('--host-label', required=True)
    parser.add_argument('--output', type=pathlib.Path, required=True)
    parser.add_argument('--no-transfer', action='store_true')
    parser.add_argument('--systemd', action='store_true', help='disposable Linux only: install temporary units through sudo')
    args = parser.parse_args()
    if args.opcua_pilot:
        args.assets = 1
        args.measurements = 3
        args.rate = 3.0
        args.burst_rate = 3.0
    if not 1 <= args.measurements <= 32 or (args.burst_seconds is not None and not .1 <= args.burst_seconds <= 120):
        parser.error('measurement count or burst duration exceeds bounds')
    if not 4 <= args.count <= (200 if args.opcua_pilot else 1000) or not 1 <= len(args.host_label.strip()) <= 128:
        parser.error('count or named host label exceeds bounds')
    args.steady_count = args.count//2
    if args.burst_seconds is not None and args.assets > 0 and not args.opcua_pilot:
        args.count = args.steady_count + math.ceil(args.burst_seconds*args.burst_rate/args.assets)
    asset_bounds_ok = (args.assets == 1) if args.opcua_pilot else (2 <= args.assets <= 8)
    if not (asset_bounds_ok and 4 <= args.count <= 1000 and 0 < args.rate <= 100 and 0 < args.burst_rate <= 200 and 512 <= args.envelope_bytes <= 60000 and .2 <= args.sample_interval <= 5 and 60 <= args.max_seconds <= 900 and 64 <= args.max_disk_mib <= 2048):
        parser.error('workload exceeds documented bounds')
    total_scheduled_obs = args.count*3 if args.opcua_pilot else args.count*args.assets
    if total_scheduled_obs/min(args.rate, args.burst_rate) > args.max_seconds/2:
        parser.error('schedule exceeds half the wall-clock budget')
    if args.systemd and (sys.platform != 'linux' or os.getuid() == 0):
        parser.error('systemd reference requires a non-root Linux account with sudo')
    args.output = args.output.absolute()
    if args.output.exists():
        parser.error('output must be a new directory')
    os.chdir(ROOT); os.umask(0o077)
    for tool in TOOLS:
        command(['go', 'build', '-o', BIN/(tool+'-native'), './cmd/'+tool])
    command(['go', 'build', '-o', BIN/'hades-test-receiver-native', './tests/fixtures/cloud-receiver/cmd'])
    if args.opcua_pilot:
        command(['./scripts/opcua-mock-tool', 'build'])
        command(['go', 'build', '-o', BIN/'hades-opcua-gateway-native', './cmd/hades-opcua-gateway'])
    if args.systemd:
        command(['go','build','-o',BIN/'hades-service-probe-native','./tests/fixtures/service-probe/cmd'])
    with tempfile.TemporaryDirectory(prefix='hcap-', dir='/tmp') as scratch:
        run = Run(args, pathlib.Path(scratch))
        try:
            report = run.run()
            args.output.mkdir(parents=True)
            report['workload']['output'] = str(args.output)
            private_json(args.output/'report.json', report)
            source_rows = [{k: v for k, v in s.items() if k != 'payload'} for s in run.source]
            for name, data in [('source.jsonl', source_rows), ('samples.jsonl', run.samples)]:
                (args.output/name).write_text(''.join(json.dumps(v)+'\n' for v in data))
            print(json.dumps({'report': str(args.output/'report.json'), 'admitted': report['admitted'], 'assets': args.assets, 'pilot_qualified': False}))
        except Exception as error:
            if not args.output.exists():
                args.output.mkdir(parents=True)
                private_json(args.output/'report.json', {'format':'openump-stack-capacity/1','status':'failed', 'pilot_qualified':False, 'error':str(error), 'attempted':len(run.source), 'admitted':sum(v['status']==202 for v in run.source), 'refused':sum(v['status']!=202 for v in run.source), 'id_comparison':'incomplete; no loss claim'})
                source_rows = [{k: v for k, v in s.items() if k != 'payload'} for s in run.source]
                for name,data in [('source.jsonl',source_rows),('samples.jsonl',run.samples)]:
                    (args.output/name).write_text(''.join(json.dumps(v)+'\n' for v in data))
            print(run.scratch, file=sys.stderr)
            print((run.scratch/'infrastructure.log').read_text()[-6000:], file=sys.stderr)
            for cfg, _, _ in run.services.values():
                p = pathlib.Path(cfg['state'])/'events.log'
                if p.exists():
                    print(str(p)+'\n'+p.read_text()[-2000:], file=sys.stderr)
            raise
        finally:
            run.close()


if __name__ == '__main__':
    main()
