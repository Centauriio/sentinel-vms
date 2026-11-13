"""Run only in a disposable Linux CI VM after installing the add-on (no camera needed)."""
import json
import os
from pathlib import Path
import socket
import subprocess
import time


def call(args, **kwargs):
    return subprocess.run(args, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                          timeout=30, **kwargs)


def require(result, description):
    if result.returncode != 0:
        # Do not print secret-bearing argv, stdout or stderr on failure.
        raise RuntimeError(description + ' failed (exit ' + str(result.returncode) + ')')
    return result.stdout


def main():
    if os.environ.get('SENTINEL_USB_INTEGRATION') != '1':
        raise RuntimeError('Set SENTINEL_USB_INTEGRATION=1 in a disposable Linux CI VM.')
    publisher = None
    configured = False
    base = ['sudo', 'sentinel-usb']
    try:
        require(call(base + ['configure', 'ci-smoke', '--mode', 'windows-publisher', '--port', '18565']), 'configure')
        configured = True
        cfg = json.loads(require(call(['sudo', 'cat', '/etc/sentinel-usb/ci-smoke.json']), 'read test configuration'))
        # Reinstall must preserve credentials and leave the configured camera stopped.
        install = Path(__file__).resolve().parents[1] / 'install.sh'
        require(call(['sudo', 'bash', str(install)]), 'idempotent install')
        again = json.loads(require(call(['sudo', 'cat', '/etc/sentinel-usb/ci-smoke.json']), 'read preserved configuration'))
        assert cfg == again, 'Reinstall replaced the camera configuration'
        assert call(['systemctl', 'is-active', 'sentinel-usb@ci-smoke']).returncode != 0, 'Installer started capture'
        require(call(base + ['start', 'ci-smoke']), 'start')
        deadline = time.monotonic() + 15
        while True:
            try:
                with socket.create_connection(('127.0.0.1', 18565), timeout=0.5):
                    break
            except OSError:
                if time.monotonic() > deadline:
                    raise RuntimeError('Bridge did not listen')
                time.sleep(0.25)
        def url(role, password):
            return 'rtsp://{}:{}@127.0.0.1:18565/camera'.format(role, password)
        publish_url = url('publisher', cfg['publisherPassword'])
        read_url = url('reader', cfg['readerPassword'])
        command = ['ffmpeg', '-nostdin', '-hide_banner', '-loglevel', 'error', '-re',
                   '-f', 'lavfi', '-i', 'testsrc2=size=320x240:rate=10', '-c:v', 'libx264',
                   '-preset', 'ultrafast', '-tune', 'zerolatency', '-g', '10',
                   '-f', 'rtsp', '-rtsp_transport', 'tcp']
        publisher = subprocess.Popen(command + [publish_url], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        probe = ['ffprobe', '-v', 'error', '-rtsp_transport', 'tcp', '-select_streams', 'v:0',
                 '-show_entries', 'stream=codec_name,width,height', '-of', 'json']
        for attempt in range(20):
            result = call(probe + [read_url])
            if result.returncode == 0:
                break
            if publisher.poll() is not None:
                raise RuntimeError('Synthetic publisher exited')
            time.sleep(0.25)
        streams = json.loads(require(result, 'authenticated reader'))['streams']
        assert streams[0]['codec_name'] == 'h264' and streams[0]['width'] == 320
        for denied in ('rtsp://127.0.0.1:18565/camera', publish_url, url('reader', 'wrong')):
            result = call(probe + [denied])
            assert result.returncode != 0 and '401' in result.stderr, 'Unauthorized read was not rejected with 401'
        # Reader credentials cannot publish, even though valid for viewing.
        result = call(command + [read_url])
        assert result.returncode != 0 and '401' in result.stderr, 'Reader was not denied publication'
        require(call(base + ['stop', 'ci-smoke']), 'stop')
        assert call(['systemctl', 'is-active', 'sentinel-usb@ci-smoke']).returncode != 0
        try:
            with socket.create_connection(('127.0.0.1', 18565), timeout=0.5):
                raise AssertionError('Bridge still listening after stop')
        except OSError:
            pass
        print('PASS: reinstall preservation, synthetic publish/read, authentication boundaries, and stop cleanup.')
    finally:
        if publisher is not None:
            publisher.terminate()
            try:
                publisher.wait(timeout=5)
            except subprocess.TimeoutExpired:
                publisher.kill()
                publisher.wait()
        if configured:
            require(call(base + ['remove', 'ci-smoke']), 'cleanup')


if __name__ == '__main__':
    main()
