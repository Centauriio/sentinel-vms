import hashlib
import importlib.util
from pathlib import Path
import tempfile
import unittest
import zipfile

spec = importlib.util.spec_from_file_location('usb_package', Path(__file__).parents[3] / 'scripts' / 'build-usb-addon.py')
package = importlib.util.module_from_spec(spec)
spec.loader.exec_module(package)


class PackageTests(unittest.TestCase):
    def test_portable_package_integrity_and_reproducibility(self):
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp)
            archive = package.build(output)
            first = archive.read_bytes()
            digest = hashlib.sha256(first).hexdigest()
            self.assertEqual((output / 'sentinel-usb-addon.zip.sha256').read_text().split()[0], digest)
            with zipfile.ZipFile(archive) as bundle:
                names = bundle.namelist()
                self.assertEqual(len(names), len(set(names)))
                self.assertFalse(any('/tests/' in n or '__pycache__' in n for n in names))
                prefix = 'sentinel-usb-addon/'
                for name in ('Install-SentinelUsbHost.ps1', 'install-host.sh', 'install.sh', 'usb_control.py',
                             'sentinel_usb.py', 'Run-SentinelWindowsAgent.ps1', 'Test-SentinelWindowsHelper.ps1'):
                    self.assertIn(prefix + name, names)
                manifest = bundle.read(prefix + 'SHA256SUMS').decode().splitlines()
                self.assertEqual(len(manifest), len(names) - 1)
                for line in manifest:
                    expected, name = line.split('  ', 1)
                    data = bundle.read(prefix + name)
                    self.assertEqual(hashlib.sha256(data).hexdigest(), expected)
                    self.assertNotIn(b'\r\n', data, 'Windows checkout broke Linux line endings')
            self.assertEqual(package.build(output).read_bytes(), first)
