"""Loopback S7 (ISO-on-TCP) fixture for Hades-040, built on python-snap7.

Serves DB1 with known big-endian values, counts read PDUs and items, and refuses
and records every write or PLC control request. Usage: s7_server.py PORT LEDGER
"""
import json
import struct
import sys
import time

import snap7
from snap7.type import SrvArea

port, ledger_path = int(sys.argv[1]), sys.argv[2]
ledger = open(ledger_path, "a", buffering=1)


def record(kind, **fields):
    ledger.write(json.dumps({"kind": kind, **fields}) + "\n")


class RecordingServer(snap7.server.Server):
    def _handle_read_area(self, request, client_address):
        items = request.get("parameters", {}).get("item_count", 1)
        record("read", items=items)
        return super()._handle_read_area(request, client_address)

    def _handle_write_area(self, request, client_address):
        record("write")
        return self._build_error_response(request, 0x8500)  # access denied

    def _handle_plc_control(self, request, client_address):
        record("write", service="plc_control")
        return self._build_error_response(request, 0x8500)

    def _handle_plc_stop(self, request, client_address):
        record("write", service="plc_stop")
        return self._build_error_response(request, 0x8500)


db = bytearray(16)
struct.pack_into(">f", db, 0, 3.25)  # %DB1:0:REAL
struct.pack_into(">i", db, 4, -123456)  # %DB1:4:DINT
struct.pack_into(">h", db, 8, -42)  # %DB1:8:INT
struct.pack_into(">H", db, 10, 4242)  # %DB1:10:UINT
db[12] = 0x01  # %DB1:12.0:BOOL
server = RecordingServer()
server.register_area(SrvArea.DB, 1, db)
server.start_to("127.0.0.1", tcp_port=port)
print("ready", port, flush=True)
while True:
    time.sleep(1)
