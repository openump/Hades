"""Loopback BACnet/IP device fixture for Hades-040, built on bacpypes3.

Serves typed value objects, counts ReadProperty(Multiple) requests, and refuses
and records every WriteProperty(Multiple). Negative IntegerValue fixtures are
avoided: the pinned plc4go does not sign-extend BACnet SignedInteger values.
Usage: bacnet_server.py PORT LEDGER
"""
import asyncio
import json
import sys

from bacpypes3.app import Application
from bacpypes3.argparse import SimpleArgumentParser
from bacpypes3.errors import ExecutionError
from bacpypes3.local.analog import AnalogValueObject
from bacpypes3.local.binary import BinaryValueObject
from bacpypes3.object import IntegerValueObject, PositiveIntegerValueObject

port, ledger_path = int(sys.argv[1]), sys.argv[2]
ledger = open(ledger_path, "a", buffering=1)


def record(kind, **fields):
    ledger.write(json.dumps({"kind": kind, **fields}) + "\n")


class RecordingApplication(Application):
    async def do_ReadPropertyRequest(self, apdu):
        record("read", items=1)
        return await super().do_ReadPropertyRequest(apdu)

    async def do_ReadPropertyMultipleRequest(self, apdu):
        record("read", items=sum(len(spec.listOfPropertyReferences) for spec in apdu.listOfReadAccessSpecs))
        return await super().do_ReadPropertyMultipleRequest(apdu)

    async def do_WritePropertyRequest(self, apdu):
        record("write", service="writeProperty")
        raise ExecutionError(errorClass="property", errorCode="writeAccessDenied")

    async def do_WritePropertyMultipleRequest(self, apdu):
        record("write", service="writePropertyMultiple")
        raise ExecutionError(errorClass="property", errorCode="writeAccessDenied")


async def main():
    args = SimpleArgumentParser().parse_args(["--address", f"127.0.0.1:{port}", "--instance", "4040", "--name", "hades-sim"])
    app = RecordingApplication.from_args(args)
    app.add_object(AnalogValueObject(objectIdentifier=("analogValue", 1), objectName="av1", presentValue=3.25, statusFlags=[0, 0, 0, 0], units="degreesCelsius"))
    app.add_object(BinaryValueObject(objectIdentifier=("binaryValue", 1), objectName="bv1", presentValue="active", statusFlags=[0, 0, 0, 0]))
    app.add_object(IntegerValueObject(objectIdentifier=("integerValue", 1), objectName="iv1", presentValue=4200, statusFlags=[0, 0, 0, 0]))
    app.add_object(IntegerValueObject(objectIdentifier=("integerValue", 2), objectName="iv2", presentValue=-123456, statusFlags=[0, 0, 0, 0]))
    app.add_object(PositiveIntegerValueObject(objectIdentifier=("positiveIntegerValue", 1), objectName="piv1", presentValue=4242, statusFlags=[0, 0, 0, 0]))
    print("ready", port, flush=True)
    await asyncio.Future()


asyncio.run(main())
