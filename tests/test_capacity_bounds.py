"""Invalid operator workloads must fail before builds or disposable resources."""
import contextlib
import importlib.util
import io
import pathlib
import sys
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('stack_capacity', pathlib.Path(__file__).resolve().parents[1]/'benchmarks/stack/run.py')
capacity = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capacity)


class CapacityBoundsTests(unittest.TestCase):
    def test_invalid_workloads_never_start_resources(self):
        cases = [
            ['--count', '-4', '--burst-seconds', '1'],
            ['--assets', '0', '--burst-seconds', '1'],
            ['--envelope-bytes', '65537'],
            ['--count', '1000', '--rate', '.01'],
            ['--measurements', '65'],
        ]
        for flags in cases:
            with self.subTest(flags=flags), mock.patch.object(sys, 'argv', ['benchmark-stack', '--host-label', 'test', '--output', '/tmp/hcap-invalid-unused']+flags), mock.patch.object(capacity, 'command', side_effect=AssertionError('must reject before build')), contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as error:
                    capacity.main()
                self.assertEqual(error.exception.code, 2)
