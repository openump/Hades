#!/usr/bin/env python3
"""Loopback-only capability checks; not a production runtime or OT driver."""
import argparse
import contextlib
import json
import os
from pathlib import Path
import socket
import socketserver
import struct
import subprocess
import tempfile
import threading
import time
import urllib.parse
import urllib.request

HERE = Path(__file__).resolve().parent


def command(args, **kwargs):
    return subprocess.run(args, check=True, text=True, capture_output=True,
                          timeout=30, **kwargs).stdout


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


@contextlib.contextmanager
def service(args, port, log):
    with log.open('w') as output:
        process = subprocess.Popen(args, stdout=output, stderr=output)
        try:
            deadline = time.monotonic() + 15
            while True:
                if process.poll() is not None:
                    raise RuntimeError(log.read_text())
                try:
                    with socket.create_connection(('127.0.0.1', port), timeout=0.2):
                        break
                except OSError:
                    if time.monotonic() > deadline:
                        raise TimeoutError(log.read_text())
                    time.sleep(0.05)
            yield process
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


class ModbusReadOnly(socketserver.StreamRequestHandler):
    def handle(self):
        self.connection.settimeout(5)
        while True:
            header = self.rfile.read(7)
            if not header:
                return
            if len(header) != 7:
                raise ValueError('truncated Modbus header')
            transaction, protocol, length, unit = struct.unpack('>HHHB', header)
            pdu = self.rfile.read(length - 1)
            if protocol != 0 or unit != 1 or len(pdu) != 5:
                raise ValueError('unexpected Modbus request')
            function, address, count = struct.unpack('>BHH', pdu)
            self.server.requests.append((function, address, count))
            if (function, address, count) != (3, 0, 1):
                raise ValueError('probe permits only holding-register read at address 0')
            response = struct.pack('>BBH', 3, 2, 672)
            self.wfile.write(struct.pack('>HHHB', transaction, 0, len(response) + 1, unit) + response)
            self.wfile.flush()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('mosquitto', 'mosquitto_pub', 'mosquitto_sub', 'bento', 'telegraf', 'victoriametrics'):
        parser.add_argument('--' + name.replace('_', '-'), required=True)
    opts = parser.parse_args()
    versions = {
        'mosquitto': command([opts.mosquitto, '-h']).splitlines()[0],
        'bento': command([opts.bento, '--version']).strip(),
        'telegraf': command([opts.telegraf, '--version']).strip(),
        'victoriametrics': command([opts.victoriametrics, '-version']).strip(),
    }
    for component, expected in [('mosquitto', '2.1.2'), ('bento', '1.22.0'),
                                 ('telegraf', '1.40.2'), ('victoriametrics', '1.154.0')]:
        if expected not in versions[component]:
            raise ValueError(f'{component}: expected {expected}, got {versions[component]}')
    with tempfile.TemporaryDirectory(prefix='hades-probes-') as directory:
        work = Path(directory)
        port = free_port()
        config = work / 'mosquitto.conf'
        config.write_text(f'listener {port} 127.0.0.1\nallow_anonymous true\npersistence false\n')
        with service([opts.mosquitto, '-c', str(config)], port, work / 'mqtt.log'):
            subscriber = subprocess.Popen([opts.mosquitto_sub, '-h', '127.0.0.1', '-p', str(port),
                                           '-t', 'hades/probe', '-q', '1', '-C', '1', '-W', '5'],
                                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                # Retention makes this subscription race-safe for the disposable probe broker only.
                command([opts.mosquitto_pub, '-h', '127.0.0.1', '-p', str(port), '-t', 'hades/probe',
                         '-q', '1', '-r', '-m', '{"probe":"ok"}'])
                output, error = subscriber.communicate(timeout=10)
                if subscriber.returncode != 0 or json.loads(output) != {'probe': 'ok'}:
                    raise RuntimeError(error or output)
            finally:
                if subscriber.poll() is None:
                    subscriber.kill()
                    subscriber.wait()
        raw = {'asset_id': 'urn:openump:acme:berlin:press-04',
               'observed_at': '2026-10-13T14:00:00.000Z', 'raw_temperature': 672}
        mapped = json.loads(command([opts.bento, 'blobl', '-f', str(HERE / 'mapping.blobl')],
                                    input=json.dumps(raw) + '\n'))
        if mapped != {'asset_id': raw['asset_id'], 'observed_at': raw['observed_at'],
                      'temperature': 67.2, 'unit': 'Cel'}:
            raise ValueError(mapped)
        with socketserver.ThreadingTCPServer(('127.0.0.1', 0), ModbusReadOnly) as server:
            server.daemon_threads = True
            server.requests = []
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                env = os.environ.copy()
                env['HADES_PROBE_MODBUS_PORT'] = str(server.server_address[1])
                output = command([opts.telegraf, '--config', str(HERE / 'telegraf.conf'),
                                  '--test', '--input-filter', 'modbus'], env=env)
                if 'temperature=67.2' not in output or not server.requests:
                    raise ValueError(output)
            finally:
                server.shutdown()
                thread.join(timeout=5)
        port = free_port()
        with service([opts.victoriametrics, '-httpListenAddr', f'127.0.0.1:{port}',
                      '-storageDataPath', str(work / 'history'), '-retentionPeriod', '1d', '-inmemoryDataFlushInterval', '1s'],
                     port, work / 'history.log'):
            base = f'http://127.0.0.1:{port}'
            sample = b'hades_probe_temperature{asset="press-04"} 67.2\n'
            request = urllib.request.Request(base + '/api/v1/import/prometheus', data=sample)
            with urllib.request.urlopen(request, timeout=5) as response:
                if response.status != 204:
                    raise ValueError(response.status)
            query = urllib.parse.urlencode({'query': 'hades_probe_temperature', 'nocache': '1'})
            deadline = time.monotonic() + 10
            while True:
                with urllib.request.urlopen(base + '/api/v1/query?' + query, timeout=5) as response:
                    result = json.load(response)
                values = result.get('data', {}).get('result', [])
                if values:
                    if float(values[0]['value'][1]) != 67.2:
                        raise ValueError(result)
                    break
                if time.monotonic() > deadline:
                    raise TimeoutError(result)
                time.sleep(0.1)
        print(json.dumps({'versions': versions, 'mqtt_qos1': 'passed', 'mapping': 'passed',
                          'modbus_function3_scaling': 'passed', 'history_write_query': 'passed'}, indent=2))


if __name__ == '__main__':
    main()
