#!/usr/bin/env python3
"""Package the USB add-on without building the VMS or downloading dependencies."""
import argparse
import hashlib
from pathlib import Path
import zipfile


def build(output):
    source = Path(__file__).resolve().parents[1] / 'packaging' / 'usb'
    output.mkdir(parents=True, exist_ok=True)
    archive = output / 'sentinel-usb-addon.zip'
    files = sorted(p for p in source.iterdir() if p.is_file() and
                   (p.suffix in ('.ps1', '.py', '.sh', '.service') or p.name == 'README.md'))
    manifest = []
    with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_DEFLATED) as bundle:
        for path in files:
            # Normalize Git's Windows checkout line endings for bash/Python on Linux.
            content = path.read_text(encoding='utf-8').encode('utf-8')
            entry = zipfile.ZipInfo('sentinel-usb-addon/' + path.name, date_time=(2026, 1, 1, 0, 0, 0))
            entry.compress_type = zipfile.ZIP_DEFLATED
            entry.create_system = 3
            entry.external_attr = (0o100644 << 16)
            bundle.writestr(entry, content)
            manifest.append(hashlib.sha256(content).hexdigest() + '  ' + path.name)
        entry = zipfile.ZipInfo('sentinel-usb-addon/SHA256SUMS', date_time=(2026, 1, 1, 0, 0, 0))
        entry.compress_type = zipfile.ZIP_DEFLATED
        bundle.writestr(entry, '\n'.join(manifest) + '\n')
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    (output / 'sentinel-usb-addon.zip.sha256').write_text(digest + '  ' + archive.name + '\n', encoding='utf-8')
    return archive


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, default=Path(__file__).resolve().parents[1] / 'dist')
    print(build(parser.parse_args().output))
