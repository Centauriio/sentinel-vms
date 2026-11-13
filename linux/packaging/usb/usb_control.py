#!/usr/bin/env python3
"""Local, narrowly scoped USB control broker. Never exposed over TCP."""
import argparse
from http.server import BaseHTTPRequestHandler
import json
import os
from pathlib import Path
import re
import socket
import socketserver
import subprocess
import threading
import time

import sentinel_usb as usb

RUNTIME = Path('/run/sentinel-usb-control')
PROFILE_KEYS = ('format', 'width', 'height', 'fps', 'bitrate')


def profile(value):
    if not isinstance(value, dict) or set(value) != set(PROFILE_KEYS):
        raise ValueError('Choose a supported quality mode and bitrate.')
    if value['format'] not in usb.FORMATS.values():
        raise ValueError('Unsupported capture format.')
    for key in ('width', 'height', 'bitrate'):
        if type(value[key]) is not int:
            raise ValueError('Invalid quality settings.')
    if type(value['fps']) not in (int, float) or not 1 <= value['fps'] <= 60:
        raise ValueError('Frame rate must be between 1 and 60 fps.')
    if not 160 <= value['width'] <= 7680 or not 120 <= value['height'] <= 4320:
        raise ValueError('Unsupported video dimensions.')
    if value['width'] % 2 or value['height'] % 2 or not 128 <= value['bitrate'] <= 50000:
        raise ValueError('Use even video dimensions and a bitrate between 128 and 50000 kbps.')
    return dict(value)


def service(name, action):
    usb.instance_name(name)
    if action not in ('start', 'stop', 'restart'):
        raise ValueError('Unsupported service action.')
    usb.command(['systemctl', action, 'sentinel-usb@' + name + '.service'])


def service_state(name):
    output = usb.command(['systemctl', 'show', 'sentinel-usb@' + usb.instance_name(name) + '.service',
                          '--property=ActiveState,SubState'])
    state = dict(line.split('=', 1) for line in output.splitlines() if '=' in line)
    return state.get('ActiveState', 'inactive')


def configs():
    for path in usb.CONFIG_DIR.glob('web-*.json'):
        value = json.loads(path.read_text())
        if value.get('webManaged') is True:
            yield value


class Broker:
    def __init__(self):
        self.lock = threading.RLock()
        self.windows_devices = []
        self.windows_status = {}
        self.last_windows_poll = 0
        self.device_cache = []
        self.device_cache_time = 0

    def inventory(self):
        if time.monotonic() - self.device_cache_time > 10:
            self.device_cache = []
            for d in usb.devices():
                modes = []
                for mode in d['modes']:
                    try:
                        profile(dict(mode, bitrate=4000))
                        modes.append(mode)
                    except ValueError:
                        pass
                if modes:
                    self.device_cache.append(dict(id=d['device'], name=Path(d['device']).name,
                                                  kind='linux', modes=modes))
            self.device_cache_time = time.monotonic()
        return self.device_cache + (self.windows_devices if self.windows_online() else [])

    def windows_online(self):
        return time.monotonic() - self.last_windows_poll < 15

    def managed(self, name):
        if not re.fullmatch(r'web-[a-f0-9]{24}', name):
            raise ValueError('Invalid managed camera identifier.')
        cfg = usb.load(name)
        if cfg.get('webManaged') is not True:
            raise ValueError('This camera is not managed by the web interface.')
        return cfg

    def view(self, cfg):
        active = service_state(cfg['name'])
        state = {'active': 'running', 'activating': 'starting', 'failed': 'failed'}.get(active, 'stopped')
        if cfg['mode'] == 'windows-publisher' and active == 'active':
            state = self.windows_status.get(cfg['name'], 'starting') if self.windows_online() else 'helper-offline'
        return dict(instance=cfg['name'], device=cfg.get('windowsDevice', cfg['device']),
                    kind='windows' if cfg['mode'] == 'windows-publisher' else 'linux',
                    profile={k: cfg[k] for k in PROFILE_KEYS}, state=state,
                    requested=cfg.get('captureRequested', False) and active != 'inactive')

    def save(self, cfg):
        usb.private_json(usb.CONFIG_DIR / (cfg['name'] + '.json'), cfg, group='sentinel-usb')

    def operate(self, method, path, body):
        with self.lock:
            if method == 'GET' and path == '/inventory':
                return {'devices': self.inventory(), 'windowsOnline': self.windows_online(),
                        'cameras': [self.view(c) for c in configs()]}
            if method == 'POST' and path == '/cameras':
                if set(body) != {'instance', 'device', 'profile'}:
                    raise ValueError('Invalid camera request.')
                name = body['instance']
                if not isinstance(name, str) or not re.fullmatch(r'web-[a-f0-9]{24}', name):
                    raise ValueError('Invalid managed camera identifier.')
                selected = next((d for d in self.inventory() if d['id'] == body['device']), None)
                if selected is None:
                    raise ValueError('Camera is no longer available. Refresh the camera list.')
                selected_profile = profile(body['profile'])
                usb.validate_profile(selected_profile, selected['modes'])
                used_ports = {json.loads(p.read_text()).get('port') for p in usb.CONFIG_DIR.glob('*.json')}
                listen = usb.tools().get('listen', '127.0.0.1')
                port = None
                for candidate in range(18555, 18655):
                    if candidate in used_ports:
                        continue
                    try:
                        with socket.socket(socket.AF_INET6 if ':' in listen else socket.AF_INET) as probe:
                            probe.bind((listen, candidate))
                        port = candidate
                        break
                    except OSError:
                        continue
                if port is None:
                    raise ValueError('No free camera ports are available.')
                if any(c.get('windowsDevice', c['device']) == body['device'] for c in configs()):
                    raise ValueError('This camera is already configured.')
                args = argparse.Namespace(name=name, mode='v4l2' if selected['kind'] == 'linux' else 'windows-publisher',
                                          device=body['device'] if selected['kind'] == 'linux' else None,
                                          listen=listen, port=port, **selected_profile)
                usb.configure(args)
                cfg = usb.load(name)
                cfg.update(webManaged=True, captureRequested=False)
                if selected['kind'] == 'windows':
                    cfg['windowsDevice'] = selected['id']
                self.save(cfg)
                # This URL is returned only to the privileged local web-server socket, never to browsers.
                return dict(self.view(cfg), url=usb.rtsp_url(cfg, 'reader'))
            match = re.fullmatch(r'/cameras/(web-[a-f0-9]{24})(?:/(start|stop))?', path)
            if not match:
                raise ValueError('Unsupported camera operation.')
            name, action = match.groups()
            if method == 'DELETE' and not action and not (usb.CONFIG_DIR / (name + '.json')).exists():
                return {}
            cfg = self.managed(name)
            if method == 'POST' and action in ('start', 'stop') and not body:
                if action == 'start':
                    selected = next((d for d in self.inventory() if d['id'] == cfg.get('windowsDevice', cfg['device'])), None)
                    if selected is None:
                        raise ValueError('Camera or Windows helper is offline. Reconnect it before starting.')
                    usb.validate_profile(cfg, selected['modes'])
                # Persist intent before signalling; helper polls stop even if a later service call fails.
                cfg['captureRequested'] = action == 'start'
                self.save(cfg)
                service(name, action)
                if action == 'stop':
                    usb.command(['systemctl', 'disable', 'sentinel-usb@' + name + '.service'])
                return self.view(cfg)
            if method == 'PUT' and not action:
                if set(body) != {'profile'}:
                    raise ValueError('Only quality settings may be changed.')
                selected_profile = profile(body['profile'])
                selected = next((d for d in self.inventory() if d['id'] == cfg.get('windowsDevice', cfg['device'])), None)
                if selected is None:
                    raise ValueError('Reconnect the camera before changing its quality.')
                usb.validate_profile(selected_profile, selected['modes'])
                running = cfg.get('captureRequested', False)
                service(name, 'stop')
                cfg.update(selected_profile)
                self.save(cfg)  # keep port and credentials stable
                if running:
                    service(name, 'start')
                return self.view(cfg)
            if method == 'DELETE' and not action and not body:
                cfg['captureRequested'] = False
                self.save(cfg)
                service(name, 'stop')
                usb.command(['systemctl', 'disable', 'sentinel-usb@' + name + '.service'])
                (usb.CONFIG_DIR / (name + '.json')).unlink()
                return {}
            raise ValueError('Unsupported camera operation.')

    def agent_poll(self, body):
        # Separate socket/OS group: agents cannot configure cameras or obtain reader credentials.
        if not isinstance(body, dict) or set(body) != {'devices', 'status'}:
            raise ValueError('Invalid agent report.')
        if not isinstance(body['devices'], list) or len(body['devices']) > 32 or not isinstance(body['status'], dict):
            raise ValueError('Invalid device inventory.')
        devices = []
        for item in body['devices']:
            if not isinstance(item, dict) or set(item) != {'id', 'name', 'modes'}:
                raise ValueError('Invalid device.')
            if not isinstance(item['id'], str) or not re.fullmatch(r'windows:[a-f0-9]{32}', item['id']):
                raise ValueError('Invalid Windows device identifier.')
            if not isinstance(item['name'], str) or not 1 <= len(item['name']) <= 200:
                raise ValueError('Invalid device name.')
            if not isinstance(item['modes'], list) or len(item['modes']) > 256:
                raise ValueError('Invalid quality modes.')
            modes = []
            for mode in item['modes']:
                value = profile(dict(mode, bitrate=4000))
                modes.append({k: value[k] for k in ('format', 'width', 'height', 'fps')})
            devices.append(dict(item, kind='windows', modes=modes))
        if len({d['id'] for d in devices}) != len(devices):
            raise ValueError('Duplicate device identifier.')
        with self.lock:
            self.windows_devices = devices
            self.windows_status = {k: v for k, v in body['status'].items()
                                   if re.fullmatch(r'web-[a-f0-9]{24}', k) and v in ('starting', 'running', 'failed')}
            self.last_windows_poll = time.monotonic()
            commands = []
            for cfg in configs():
                if cfg['mode'] != 'windows-publisher' or not cfg.get('captureRequested') or service_state(cfg['name']) != 'active':
                    continue
                if not any(d['id'] == cfg.get('windowsDevice') for d in devices):
                    continue
                commands.append(dict(instance=cfg['name'], device=cfg['windowsDevice'],
                                     profile={k: cfg[k] for k in PROFILE_KEYS}, url=usb.rtsp_url(cfg, 'publisher')))
            return {'cameras': commands}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass  # no request bodies, URLs or credentials in broker logs

    def do_GET(self): self.handle_request()
    def do_POST(self): self.handle_request()
    def do_PUT(self): self.handle_request()
    def do_DELETE(self): self.handle_request()

    def handle_request(self):
        try:
            length = int(self.headers.get('Content-Length', '0'))
            if length < 0 or length > 256 * 1024:
                raise ValueError('Request too large.')
            body = json.loads(self.rfile.read(length)) if length else {}
            if not isinstance(body, dict):
                raise ValueError('Expected an object.')
            if self.server.agent:
                if self.command != 'POST' or self.path != '/poll':
                    raise ValueError('Unsupported agent operation.')
                result = self.server.broker.agent_poll(body)
            else:
                result = self.server.broker.operate(self.command, self.path, body)
            self.respond(200, result)
        except (ValueError, FileNotFoundError) as error:
            self.respond(400, {'error': str(error)})
        except Exception:
            self.respond(503, {'error': 'USB operation failed. Check the capture service and try again.'})

    def setup(self):
        self.request.settimeout(30)
        super().setup()

    def respond(self, status, value):
        content = json.dumps(value).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(content)))
        self.end_headers()
        self.wfile.write(content)


class UnixHTTPServer(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


def main():
    import grp
    usb.require_root()
    broker = Broker()
    settings = usb.tools()
    for name, agent in [('control', False), ('agent', True)]:
        path = RUNTIME / (name + '.sock')
        path.unlink(missing_ok=True)
        server = UnixHTTPServer(str(path), Handler)
        if agent:
            os.chown(path, 0, grp.getgrnam('sentinel-usb-agent').gr_gid)
            path.chmod(0o660)
        else:
            os.chown(path, int(settings.get('webUID', 0)), 0)
            path.chmod(0o600)
        server.agent, server.broker = agent, broker
        threading.Thread(target=server.serve_forever, daemon=True).start()
    print('USB control broker ready.', flush=True)
    threading.Event().wait()


if __name__ == '__main__':
    main()
