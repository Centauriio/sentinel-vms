#!/usr/bin/env python3
"""Optional Linux USB capture service; stdlib only, no VMS database access."""
import argparse
import glob
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import socket
import stat
import subprocess
import sys
import threading
import time

CONFIG_DIR = Path('/etc/sentinel-usb')
TOOLS_FILE = CONFIG_DIR / 'tools.json'
FORMATS = {'MJPG': 'mjpeg', 'YUYV': 'yuyv422', 'NV12': 'nv12'}
NAME = re.compile(r'^[a-z][a-z0-9-]{0,31}$')


def command(args, timeout=15):
    return subprocess.run(args, text=True, stdout=subprocess.PIPE,
                          stderr=subprocess.STDOUT, timeout=timeout, check=True).stdout


def instance_name(value):
    if not NAME.fullmatch(value) or value == 'tools':
        raise ValueError('Name must start with a lowercase letter, contain at most 32 lowercase letters, digits or hyphens, and not be tools.')
    return value


def require_root():
    if os.geteuid() != 0:
        raise ValueError('Run this command with sudo.')


def load(name):
    path = CONFIG_DIR / (instance_name(name) + '.json')
    if not path.is_file():
        raise ValueError('Camera is not configured. Run sentinel-usb configure first.')
    return json.loads(path.read_text())


def tools():
    return json.loads(TOOLS_FILE.read_text())


def device_path(value):
    path = Path(value)
    if not (re.fullmatch(r'/dev/video\d+', str(path)) or
            (str(path).startswith('/dev/v4l/by-id/') and path.parent == Path('/dev/v4l/by-id'))):
        raise ValueError('Select /dev/videoN or a device listed under /dev/v4l/by-id/.')
    resolved = path.resolve(strict=True)
    if not re.fullmatch(r'/dev/video\d+', str(resolved)) or not stat.S_ISCHR(resolved.stat().st_mode):
        raise ValueError('The selected path is not a V4L2 video character device.')
    return str(path)  # retain stable by-id symlink for reconnection


def parse_formats(output):
    """Only advertise discrete modes actually returned by v4l2-ctl."""
    result, codec, size = [], None, None
    for line in output.splitlines():
        match = re.search(r"\[\d+\]: '([A-Z0-9 ]{4})'", line)
        if match:
            codec, size = FORMATS.get(match[1]), None
        match = re.search(r'Size: Discrete (\d+)x(\d+)', line)
        if match:
            size = [int(match[1]), int(match[2])]
        elif 'Size:' in line:
            size = None
        match = re.search(r'Interval: Discrete .*\(([0-9.]+) fps\)', line)
        if match and codec and size:
            mode = {'format': codec, 'width': size[0], 'height': size[1], 'fps': float(match[1])}
            if mode not in result:
                result.append(mode)
    return result


def profiles(device):
    return parse_formats(command([tools()['v4l2'], '--device', device_path(device), '--list-formats-ext']))


def devices():
    seen, result = set(), []
    for path in sorted(glob.glob('/dev/v4l/by-id/*')) + sorted(glob.glob('/dev/video*')):
        try:
            resolved = str(Path(device_path(path)).resolve())
            if resolved in seen:
                continue
            seen.add(resolved)
            caps = profiles(path)
            # Metadata-only nodes and unsupported/continuous-only modes are not selectable.
            if caps:
                result.append({'device': path, 'modes': caps})
        except (OSError, ValueError, subprocess.SubprocessError):
            continue
    return result


def validate_profile(config, modes):
    if not any(m['format'] == config['format'] and m['width'] == config['width'] and
               m['height'] == config['height'] and abs(m['fps'] - config['fps']) < 0.02 for m in modes):
        raise ValueError('The camera does not advertise this format, size and frame rate. Run sentinel-usb devices.')


def rtsp_url(config, role, host=None):
    address = host or config['listen']
    # Only IP literals are accepted by CLI, so URLs cannot smuggle userinfo or paths.
    parsed = ipaddress.ip_address(address)
    if parsed.is_unspecified or parsed.is_multicast:
        raise ValueError('Use a specific loopback/private host address.')
    if parsed.version == 6:
        address = '[' + address + ']'
    return 'rtsp://{}:{}@{}:{}/camera'.format(role, config[role + 'Password'], address, config['port'])


def media_config(config):
    address = config['listen']
    if ':' in address:
        address = '[' + address + ']'
    # MediaMTX accepts JSON (a YAML subset); serialization prevents YAML injection.
    return {
        'logLevel': 'info', 'rtsp': True, 'rtspAddress': '{}:{}'.format(address, config['port']),
        'rtspTransports': ['tcp'], 'rtmp': False, 'hls': False, 'webrtc': False,
        'srt': False, 'moq': False, 'api': False, 'metrics': False, 'pprof': False, 'playback': False,
        'authMethod': 'internal',
        'authInternalUsers': [
            {'user': role, 'pass': config[role + 'Password'],
             'permissions': [{'action': action, 'path': 'camera'}]}
            for role, action in [('publisher', 'publish'), ('reader', 'read')]
        ], 'paths': {'camera': {'source': 'publisher', 'overridePublisher': False, 'record': False}}
    }


def ffmpeg_args(config, binary):
    args = [binary, '-hide_banner', '-nostdin', '-loglevel', 'warning',
            '-f', 'v4l2', '-thread_queue_size', '64', '-input_format', config['format'],
            '-video_size', '{}x{}'.format(config['width'], config['height']),
            '-framerate', str(config['fps']), '-i', config['device'], '-an']
    if config['format'] == 'mjpeg':
        args += ['-vf', 'scale=in_range=full:out_range=limited', '-color_range', 'tv']
    return args + ['-c:v', 'libx264', '-preset', 'veryfast', '-tune', 'zerolatency',
                   '-pix_fmt', 'yuv420p', '-b:v', str(config['bitrate']) + 'k',
                   '-maxrate', str(config['bitrate']) + 'k', '-bufsize', str(config['bitrate'] * 2) + 'k',
                   '-g', str(round(config['fps'] * 2)), '-sc_threshold', '0', '-vsync', '0',
                   '-f', 'rtsp', '-rtsp_transport', 'tcp', rtsp_url(config, 'publisher')]


def redact(text, config):
    for role in ('publisher', 'reader'):
        text = text.replace(config[role + 'Password'], '[REDACTED]')
    return text


def private_json(path, value, group=None):
    # Parent is root-owned or systemd's per-instance runtime directory.
    tmp = path.with_suffix('.tmp')
    fd = os.open(str(tmp), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, 'w') as stream:
            json.dump(value, stream, indent=2)
            stream.write('\n')
        if group:
            shutil.chown(tmp, user='root', group=group)
            tmp.chmod(0o640)
        os.replace(str(tmp), str(path))
    finally:
        if tmp.exists():
            tmp.unlink()


def configure(args):
    require_root()
    name = instance_name(args.name)
    path = CONFIG_DIR / (name + '.json')
    if path.exists():
        raise ValueError('This name already exists. Stop and remove it before creating a replacement; credentials were preserved.')
    address = ipaddress.ip_address(args.listen)
    if address.is_unspecified or address.is_multicast or not (address.is_loopback or address.is_private):
        raise ValueError('Listen must be a specific loopback or private IP address (not 0.0.0.0/::).')
    if not 1024 <= args.port <= 65535 or args.port == 18554:
        raise ValueError('Use port 1024..65535 other than Sentinel media port 18554; default is 18555.')
    if not 1 <= args.fps <= 60 or not 160 <= args.width <= 7680 or not 120 <= args.height <= 4320:
        raise ValueError('Unsupported video size or frame rate (1..60 fps).')
    if args.width % 2 or args.height % 2 or not 128 <= args.bitrate <= 50000:
        raise ValueError('H.264 requires even dimensions and a bitrate of 128..50000 kbps.')
    config = {'name': name, 'mode': args.mode, 'device': '', 'format': args.format,
              'width': args.width, 'height': args.height, 'fps': args.fps, 'bitrate': args.bitrate,
              'listen': str(address), 'port': args.port,
              'publisherPassword': secrets.token_hex(24), 'readerPassword': secrets.token_hex(24)}
    if args.mode == 'v4l2':
        config['device'] = device_path(args.device or '')
        validate_profile(config, profiles(config['device']))
    elif args.device:
        raise ValueError('Windows-publisher mode does not take a Linux device.')
    for other_path in CONFIG_DIR.glob('*.json'):
        if other_path == TOOLS_FILE:
            continue
        other = json.loads(other_path.read_text())
        if other['port'] == config['port'] and other['listen'] == config['listen']:
            raise ValueError('Another configured camera uses this address and port.')
        if config['device'] and other.get('device') and Path(other['device']).resolve() == Path(config['device']).resolve():
            raise ValueError('This video device is already configured for another camera.')
    family = socket.AF_INET6 if address.version == 6 else socket.AF_INET
    with socket.socket(family) as probe:
        probe.bind((str(address), args.port))
    private_json(path, config, group='sentinel-usb')
    print('Configured {}. Capture is stopped. Run: sudo sentinel-usb start {}'.format(name, name))


def consume(proc, label, config):
    for line in iter(proc.stdout.readline, ''):
        print(label + ': ' + redact(line.rstrip(), config), flush=True)


def stop_children(children):
    for proc in reversed(children):
        if proc.poll() is None:
            proc.terminate()
    for proc in reversed(children):
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()


def run(name):
    config, bins = load(name), tools()
    runtime = Path('/run/sentinel-usb-' + instance_name(name))
    config_path = runtime / 'mediamtx.json'
    # systemd creates the directory; direct root invocation is intentionally unsupported.
    if not runtime.is_dir():
        raise ValueError('Use sentinel-usb start; the service creates its private runtime directory.')
    private_json(config_path, media_config(config))
    stopping, children = threading.Event(), []
    def on_stop(signum, frame):
        stopping.set()
    for signum in (signal.SIGTERM, signal.SIGINT):
        signal.signal(signum, on_stop)
    def launch(args, label):
        proc = subprocess.Popen(args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                text=True, errors='replace')
        children.append(proc)
        threading.Thread(target=consume, args=(proc, label, config), daemon=True).start()
        return proc
    try:
        media = launch([bins['mediamtx'], str(config_path)], 'bridge')
        deadline = time.monotonic() + 15
        while not stopping.is_set():
            if media.poll() is not None:
                raise ValueError('Camera bridge exited; check the service log.')
            try:
                with socket.create_connection((config['listen'], config['port']), timeout=0.5):
                    break
            except OSError:
                if time.monotonic() >= deadline:
                    raise ValueError('Camera bridge did not become ready.')
                stopping.wait(0.25)
        if stopping.is_set():
            return
        if config['mode'] == 'v4l2':
            device_path(config['device'])
            validate_profile(config, profiles(config['device']))
            launch(ffmpeg_args(config, bins['ffmpeg']), 'capture')
        print('Camera service started (' + config['mode'] + ').', flush=True)
        while not stopping.wait(0.5):
            if any(p.poll() is not None for p in children):
                raise ValueError('Camera process exited; systemd will retry after its restart delay.')
    finally:
        stop_children(children)


def doctor():
    bins = tools()
    result = {'wsl': 'microsoft' in Path('/proc/sys/kernel/osrelease').read_text().lower(),
              'tools': bins, 'systemd': Path('/run/systemd/system').exists()}
    for key, path in bins.items():
        if not Path(path).is_file() or not os.access(path, os.X_OK):
            raise ValueError('Missing executable: ' + key + '. Re-run the USB installer.')
    if not re.search(r'\blibx264\b', command([bins['ffmpeg'], '-hide_banner', '-encoders'])):
        raise ValueError('FFmpeg has no libx264 encoder. Re-run the USB installer.')
    print(json.dumps(result, indent=2))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    sub.add_parser('doctor', help='check installed tools and WSL/systemd status')
    sub.add_parser('devices', help='list accessible V4L2 capture devices and discrete modes')
    setup = sub.add_parser('configure', help='save a camera without starting capture')
    setup.add_argument('name', type=instance_name)
    setup.add_argument('--mode', choices=['v4l2', 'windows-publisher'], default='v4l2')
    setup.add_argument('--device')
    setup.add_argument('--format', choices=list(FORMATS.values()), default='mjpeg')
    setup.add_argument('--width', type=int, default=1920)
    setup.add_argument('--height', type=int, default=1080)
    setup.add_argument('--fps', type=float, default=30)
    setup.add_argument('--bitrate', type=int, default=4000, help='kbps')
    setup.add_argument('--listen', default='127.0.0.1')
    setup.add_argument('--port', type=int, default=18555)
    for action in ['start', 'stop', 'status', 'remove', 'run', 'url', 'connection']:
        child = sub.add_parser(action)
        child.add_argument('name', type=instance_name)
        if action == 'start':
            child.add_argument('--on-boot', action='store_true', help='also enable startup after reboot')
        if action == 'url':
            child.add_argument('--role', choices=['reader', 'publisher'], default='reader')
            child.add_argument('--host', help='override destination IP for the client')
    args = parser.parse_args(argv)
    if args.command == 'doctor':
        doctor()
    elif args.command == 'devices':
        print(json.dumps(devices(), indent=2))
    elif args.command == 'configure':
        configure(args)
    elif args.command == 'run':
        run(args.name)
    else:
        require_root()
        config = load(args.name)
        unit = 'sentinel-usb@' + args.name + '.service'
        if args.command == 'url':
            print(rtsp_url(config, args.role, args.host))
        elif args.command == 'connection':
            if config['mode'] != 'windows-publisher':
                raise ValueError('This camera is configured for Linux capture, not Windows publishing.')
            output = {k: config[k] for k in ('width', 'height', 'fps', 'bitrate', 'format')}
            output['url'] = rtsp_url(config, 'publisher')
            print(json.dumps(output))
        elif args.command == 'status':
            return subprocess.call(['systemctl', '--no-pager', 'status', unit])
        elif args.command == 'start':
            if args.on_boot:
                subprocess.run(['systemctl', 'enable', unit], check=True)
            subprocess.run(['systemctl', 'start', unit], check=True)
        elif args.command == 'stop':
            # Stop is persistent across reboots; start --on-boot explicitly re-enables it.
            subprocess.run(['systemctl', 'disable', '--now', unit], check=True)
        elif args.command == 'remove':
            subprocess.run(['systemctl', 'disable', '--now', unit], check=True)
            (CONFIG_DIR / (args.name + '.json')).unlink()
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        # CalledProcessError may contain secret-bearing command arguments; never stringify it.
        message = 'A required command failed. Check prerequisites and the service log.' if isinstance(error, subprocess.SubprocessError) else str(error)
        print('sentinel-usb: ' + message, file=sys.stderr)
        sys.exit(1)
