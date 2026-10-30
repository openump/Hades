"""Loopback Beckhoff ADS fixture for Hades-040, built on pyads' AdsTestServer.

pyads' stock handler does not satisfy the PLC4X connect handshake (16-byte
device name, TwinCAT symbol/data-type tables, the OnlineChangeCnt symbol), so
this handler serves those tables plus typed GVL symbols. Reads are counted and
every write service is refused and recorded. Usage: ads_server.py PORT LEDGER
"""
import json
import struct
import sys
import time

from pyads import constants as C
from pyads.testserver import AdsTestServer
from pyads.testserver.handler import AbstractHandler, AmsResponseData

port, ledger_path = int(sys.argv[1]), sys.argv[2]
ledger = open(ledger_path, "a", buffering=1)


def record(kind, **fields):
    ledger.write(json.dumps({"kind": kind, **fields}) + "\n")


# ADS data type ids.
UINT8, INT16, UINT16, INT32, UINT32, REAL32, BIT = 17, 2, 18, 3, 19, 4, 33
TYPES = {  # name: (ads id, size, struct format)
    "BOOL": (BIT, 1, "<?"),
    "USINT": (UINT8, 1, "<B"),  # PLC4X subscribes to an offline version of this type
    "INT": (INT16, 2, "<h"),
    "UINT": (UINT16, 2, "<H"),
    "DINT": (INT32, 4, "<i"),
    "UDINT": (UINT32, 4, "<I"),
    "REAL": (REAL32, 4, "<f"),
}
APPINFO_TYPE = ("PlcAppSystemInfo", 65, 4, "<I")
GROUP = 0x4020  # PLC memory
SYMBOLS = [  # name, type, index group, offset, value
    ("GVL.fReal", "REAL", GROUP, 0, 3.25),
    ("GVL.nDint", "DINT", GROUP, 4, -123456),
    ("GVL.nInt", "INT", GROUP, 8, -42),
    ("GVL.nUint", "UINT", GROUP, 10, 4242),
    ("GVL.bFlag", "BOOL", GROUP, 12, True),
    ("TwinCAT_SystemInfoVarList._AppInfo", APPINFO_TYPE[0], 0x4040, 0, 0),
]
SYM_VALBYNAME, SYM_VERSION, SYM_UPLOAD = 0xF004, 0xF008, 0xF00B
DT_UPLOAD, UPLOAD_INFO2, SUMUP_READ = 0xF00E, 0xF00F, 0xF080
READ_ONLY_READWRITE = {SYM_VALBYNAME, SUMUP_READ}


def type_info(name):
    if name == APPINFO_TYPE[0]:
        return APPINFO_TYPE[1:]
    return TYPES[name]


def names(*values):
    return b"".join(v.encode() + b"\0" for v in values)


def data_type_entry(main, secondary, type_id, size, offset=0, children=b"", child_count=0):
    body = struct.pack("<IIIIIII", 1, 0, 0, size, offset, type_id, 0)
    body += struct.pack("<HHHHH", len(main), len(secondary), 0, 0, child_count)
    body += names(main, secondary, "") + children
    return struct.pack("<I", len(body) + 4) + body


def symbol_entry(name, type_name, group, offset):
    type_id, size, _ = type_info(type_name)
    body = struct.pack("<IIIII", group, offset, size, type_id, 0)
    body += struct.pack("<HHH", len(name), len(type_name), 0) + names(name, type_name, "")
    return struct.pack("<I", len(body) + 4) + body


class Plc4xAdsHandler(AbstractHandler):
    def __init__(self):
        self.memory = {(g, o): bytes(struct.pack(type_info(t)[2], v)) for _, t, g, o, v in SYMBOLS}
        self.memory[(SYM_VERSION, 0)] = b"\x01"
        member = data_type_entry("OnlineChangeCnt", "UDINT", UINT32, 4)
        appinfo = data_type_entry(APPINFO_TYPE[0], "", APPINFO_TYPE[1], 4, children=member, child_count=1)
        self.data_types = b"".join(data_type_entry(n, "", TYPES[n][0], TYPES[n][1]) for n in TYPES) + appinfo
        self.symbols = b"".join(symbol_entry(n, t, g, o) for n, t, g, o, _ in SYMBOLS)
        self.handles = 100

    def read(self, group, offset, length):
        if group == UPLOAD_INFO2:
            return struct.pack("<IIIIII", len(SYMBOLS), len(self.symbols), len(TYPES) + 1, len(self.data_types), 0, 0)
        if group == DT_UPLOAD:
            return self.data_types
        if group == SYM_UPLOAD:
            return self.symbols
        for (g, o), value in self.memory.items():
            if g == group and o <= offset < o + len(value):
                return value[offset - o:offset - o + length]
        raise KeyError((hex(group), offset))

    def handle_request(self, request):
        header = request.ams_header
        command = struct.unpack("<H", header.command_id)[0]
        state = struct.pack("<H", struct.unpack("<H", header.state_flags)[0] | 1)
        data = header.data
        ok = b"\0\0\0\0"
        try:
            if command == C.ADSCOMMAND_READDEVICEINFO:
                out = ok + bytes([3, 1]) + struct.pack("<H", 4024) + b"HadesAdsSim".ljust(16, b"\0")
            elif command == C.ADSCOMMAND_READ:
                group, offset, length = struct.unpack("<III", data[:12])
                if group == GROUP:
                    record("read", items=1)
                value = self.read(group, offset, length)
                out = ok + struct.pack("<I", len(value)) + value
            elif command == C.ADSCOMMAND_READWRITE:
                group, offset, _, write_length = struct.unpack("<IIII", data[:16])
                written = data[16:16 + write_length]
                if group not in READ_ONLY_READWRITE:
                    record("write", service="readwrite", group=hex(group))
                    raise PermissionError
                if group == SYM_VALBYNAME:
                    name = written.rstrip(b"\0").decode()
                    if name == "TwinCAT_SystemInfoVarList._AppInfo.OnlineChangeCnt":
                        name = "TwinCAT_SystemInfoVarList._AppInfo"
                    symbol = next(s for s in SYMBOLS if s[0] == name)
                    value = self.memory[(symbol[2], symbol[3])]
                else:  # SUMUP_READ: offset carries the item count
                    record("read", items=offset)
                    items = [struct.unpack("<III", written[i * 12:i * 12 + 12]) for i in range(offset)]
                    value = ok * offset + b"".join(self.read(*item) for item in items)
                out = ok + struct.pack("<I", len(value)) + value
            elif command in (C.ADSCOMMAND_WRITE, C.ADSCOMMAND_WRITECTRL):
                record("write", service="write" if command == C.ADSCOMMAND_WRITE else "writectrl")
                raise PermissionError
            elif command == C.ADSCOMMAND_READSTATE:
                out = ok + struct.pack("<HH", 5, 0)  # RUN
            elif command == C.ADSCOMMAND_ADDDEVICENOTE:
                self.handles += 1
                out = ok + struct.pack("<I", self.handles)
            elif command == C.ADSCOMMAND_DELDEVICENOTE:
                out = ok
            else:
                out = struct.pack("<I", 0x701)  # service not supported
        except PermissionError:
            out = struct.pack("<I", 0x704)  # ADSERR_DEVICE_INVALIDACCESS
        except (KeyError, StopIteration):
            out = struct.pack("<I", 0x710)  # ADSERR_DEVICE_SYMBOLNOTFOUND
        return AmsResponseData(state, header.error_code, out)


server = AdsTestServer(handler=Plc4xAdsHandler(), ip_address="127.0.0.1", port=port, logging=False)
server.start()
print("ready", port, flush=True)
while True:
    time.sleep(1)
