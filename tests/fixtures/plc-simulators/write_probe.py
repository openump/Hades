"""Negative control for the Hades-040 no-write proof: sends one write to a
running fixture with that protocol's reference client, so the test can assert
the fixture ledger records it. Usage: write_probe.py s7|ads|bacnet PORT
"""
import asyncio
import sys

protocol, port = sys.argv[1], int(sys.argv[2])
if protocol == "s7":
    import snap7

    client = snap7.client.Client()
    client.connect("127.0.0.1", 0, 1, tcp_port=port)
    try:
        client.db_write(1, 0, bytearray(4))
    except Exception:
        pass  # the fixture refuses the write; only the attempt matters
    client.disconnect()
elif protocol == "ads":
    import struct
    import socket

    # Raw AMS/TCP ADS Write (command 3) to index group 0x4020, offset 0.
    ams = bytes([127, 0, 0, 1, 1, 1]) + struct.pack("<H", 851) + bytes([127, 0, 0, 1, 1, 2]) + struct.pack("<H", 33000)
    payload = struct.pack("<III", 0x4020, 0, 4) + b"\0\0\0\0"
    ams += struct.pack("<HHIII", 3, 4, len(payload), 0, 1) + payload
    with socket.create_connection(("127.0.0.1", port), timeout=5) as conn:
        conn.sendall(struct.pack("<HI", 0, len(ams)) + ams)
        conn.recv(1024)
elif protocol == "bacnet":
    from bacpypes3.app import Application
    from bacpypes3.argparse import SimpleArgumentParser
    from bacpypes3.pdu import Address
    from bacpypes3.primitivedata import ObjectIdentifier, Real

    async def main():
        args = SimpleArgumentParser().parse_args(["--address", "127.0.0.1:0", "--instance", "4041", "--name", "probe"])
        app = Application.from_args(args)
        try:
            await asyncio.wait_for(app.write_property(Address(f"127.0.0.1:{port}"), ObjectIdentifier("analogValue,1"), "presentValue", Real(1.0)), 5)
        except BaseException as error:  # bacpypes3 raises the BACnet Error PDU itself
            if isinstance(error, (KeyboardInterrupt, SystemExit)):
                raise
        app.close()

    asyncio.run(main())
