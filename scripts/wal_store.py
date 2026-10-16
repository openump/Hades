#!/usr/bin/env python3

import binascii
import json
import pathlib
import struct

FRAME_HEADER_SIZE = 28


def _segment_paths(directory: pathlib.Path):
    return sorted(directory.glob("segment-*.wal"))


def _read_frames(path: pathlib.Path):
    with path.open("rb") as handle:
        while True:
            header = handle.read(FRAME_HEADER_SIZE)
            if not header:
                return
            if len(header) != FRAME_HEADER_SIZE:
                raise ValueError(f"truncated WAL frame in {path}")
            if header[:4] != b"HWAL":
                raise ValueError(f"bad WAL magic in {path}")
            kind = header[4]
            timestamp = header[8:16]
            key_len = struct.unpack("<I", header[16:20])[0]
            value_len = struct.unpack("<I", header[20:24])[0]
            checksum = struct.unpack("<I", header[24:28])[0]
            body = handle.read(key_len + value_len)
            if len(body) != key_len + value_len:
                raise ValueError(f"truncated WAL body in {path}")
            want = binascii.crc32(bytes([kind]))
            want = binascii.crc32(timestamp, want)
            want = binascii.crc32(body, want) & 0xFFFFFFFF
            if want != checksum:
                raise ValueError(f"bad WAL checksum in {path}")
            key = body[:key_len].decode("utf-8")
            value = body[key_len:]
            yield kind, key, value


def load_values(directory: str):
    path = pathlib.Path(directory)
    records = {}
    for segment in _segment_paths(path):
        for kind, key, value in _read_frames(segment):
            if kind == 1:
                records[key] = json.loads(value)
            elif kind == 2:
                records.pop(key, None)
            else:
                raise ValueError(f"unknown WAL frame kind {kind} in {segment}")
    return [records[key] for key in sorted(records)]