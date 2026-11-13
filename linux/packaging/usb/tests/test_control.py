import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parents[1]))
import socketserver
# Windows runs the platform-independent broker tests; Unix transport is tested on Linux.
if not hasattr(socketserver, "UnixStreamServer"):
    socketserver.UnixStreamServer = socketserver.TCPServer
import usb_control as control

NAME = 'web-' + 'a' * 24
DEVICE = 'windows:' + 'b' * 32
PROFILE = dict(format='mjpeg', width=1920, height=1080, fps=30, bitrate=4000)
CONFIG = dict(PROFILE, name=NAME, mode='windows-publisher', device='', windowsDevice=DEVICE,
              webManaged=True, captureRequested=True, listen='127.0.0.1', port=18555,
              publisherPassword='c' * 48, readerPassword='d' * 48)


class BrokerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.config = Path(self.tmp.name)
        self.path = self.config / (NAME + '.json')
        self.path.write_text(json.dumps(CONFIG))
        for target, value in [('CONFIG_DIR', self.config)]:
            p = patch.object(control.usb, target, value); p.start(); self.addCleanup(p.stop)
        self.broker = control.Broker()

    def report(self):
        return dict(devices=[dict(id=DEVICE, name='USB Camera', modes=[{k:v for k,v in PROFILE.items() if k!='bitrate'}])], status={NAME:'running'})

    def test_agent_gets_only_publisher_credentials_and_only_requested_camera(self):
        with patch.object(control, 'service_state', return_value='active'):
            response = self.broker.agent_poll(self.report())
            self.assertIn('c'*48, json.dumps(response))
            self.assertNotIn('d'*48, json.dumps(response))
            self.path.write_text(json.dumps(dict(CONFIG, captureRequested=False)))
            self.assertEqual(self.broker.agent_poll(self.report())['cameras'], [])

    def test_inventory_has_no_secrets_and_expires_windows_devices(self):
        with patch.object(control, 'service_state', return_value='active'), patch.object(control.usb, 'devices', return_value=[]):
            self.broker.agent_poll(self.report())
            result = self.broker.operate('GET', '/inventory', {})
            self.assertEqual(result['cameras'][0]['state'], 'running')
            self.assertNotIn('c'*48, json.dumps(result)); self.assertNotIn('d'*48, json.dumps(result))
            self.broker.last_windows_poll = 0
            result = self.broker.operate('GET', '/inventory', {})
            self.assertEqual(result['devices'], [])
            self.assertEqual(result['cameras'][0]['state'], 'helper-offline')

    def test_rejects_unadvertised_60fps_and_invalid_numbers(self):
        for value in (True, float('nan'), float('inf'), 0, 61):
            with self.assertRaises(ValueError): control.profile(dict(PROFILE, fps=value))
        with patch.object(control, 'service_state', return_value='active'), patch.object(control.usb, 'devices', return_value=[]):
            self.broker.agent_poll(self.report())
            with self.assertRaises(ValueError):
                self.broker.operate('PUT', '/cameras/'+NAME, {'profile':dict(PROFILE, fps=60)})
        self.assertEqual(json.loads(self.path.read_text()), CONFIG)

    def test_stop_intent_survives_systemd_failure(self):
        def save(cfg): self.path.write_text(json.dumps(cfg))
        with patch.object(self.broker, 'save', side_effect=save), patch.object(control, 'service', side_effect=RuntimeError('failed')):
            with self.assertRaises(RuntimeError): self.broker.operate('POST', '/cameras/'+NAME+'/stop', {})
        self.assertFalse(json.loads(self.path.read_text())['captureRequested'])

    def test_routes_and_agent_reports_cannot_run_arbitrary_commands(self):
        for path in ('/cameras/../../etc/shadow', '/cameras/'+NAME+'/restart;reboot', '/install'):
            with self.assertRaises(ValueError): self.broker.operate('POST', path, {})
        report = self.report(); report['command']='reboot'
        with self.assertRaises(ValueError): self.broker.agent_poll(report)
        report=self.report(); report['devices']*=2
        with self.assertRaises(ValueError): self.broker.agent_poll(report)

    def test_missing_delete_is_idempotent(self):
        self.path.unlink()
        self.assertEqual(self.broker.operate('DELETE', '/cameras/'+NAME, {}), {})


if __name__ == '__main__': unittest.main()
