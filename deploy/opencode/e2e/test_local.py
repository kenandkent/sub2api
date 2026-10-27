import importlib.util
import os
from pathlib import Path
import tempfile
import unittest

path = Path(__file__).resolve().parents[1] / 'local.py'
spec = importlib.util.spec_from_file_location('opencode_local', path)
local = importlib.util.module_from_spec(spec)
spec.loader.exec_module(local)

class LocalTests(unittest.TestCase):
    def test_init_is_private_stable_and_shared(self):
        with tempfile.TemporaryDirectory(dir=Path(__file__).resolve().parents[3] / '.cache') as d:
            root = Path(d)
            first = local.settings(root)
            second = local.settings(root)
            self.assertEqual(first, second)
            self.assertGreaterEqual(len(first['OPENCODE_FREE_BUN_TOKEN']), 32)
            self.assertEqual(first['OPENCODE_FREE_BUN_ENABLED'], 'true')
            self.assertEqual((root / '.dev/opencode-free.env').stat().st_mode & 0o777, 0o600)

    def test_rejects_insecure_existing_file(self):
        with tempfile.TemporaryDirectory(dir=Path(__file__).resolve().parents[3] / '.cache') as d:
            root = Path(d)
            local.settings(root)
            (root / '.dev/opencode-free.env').chmod(0o644)
            with self.assertRaises(ValueError): local.settings(root)

if __name__ == '__main__': unittest.main()
