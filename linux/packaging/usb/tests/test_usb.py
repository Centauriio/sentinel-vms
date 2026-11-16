import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location('sentinel_usb', Path(__file__).parents[1] / 'sentinel_usb.py')
usb = importlib.util.module_from_spec(spec)
spec.loader.exec_module(usb)

CONFIG = dict(name='desk', mode='v4l2', device='/dev/v4l/by-id/usb-camera-video-index0',
              format='mjpeg', width=1920, height=1080, fps=30, bitrate=4000,
              listen='127.0.0.1', port=18555, publisherPassword='a' * 48, readerPassword='b' * 48)
MODES = """
 [0]: 'MJPG' (Motion-JPEG, compressed)
     Size: Discrete 1920x1080
         Interval: Discrete 0.033s (30.000 fps)
     Size: Discrete 1280x720
         Interval: Discrete 0.067s (15.000 fps)
 [1]: 'YUYV' (YUYV 4:2:2)
     Size: Discrete 1920x1080
         Interval: Discrete 0.200s (5.000 fps)
 [2]: 'NV12'
     Size: Stepwise 16x16 - 4096x4096
         Interval: Discrete 0.033s (30.000 fps)
"""


class USBTests(unittest.TestCase):
    def test_modes_do_not_invent_60fps_or_continuous_support(self):
        modes = usb.parse_formats(MODES)
        self.assertEqual(len(modes), 3)
        usb.validate_profile(CONFIG, modes)
        with self.assertRaisesRegex(ValueError, 'does not advertise'):
            usb.validate_profile(dict(CONFIG, fps=60), modes)
        with self.assertRaises(ValueError):
            usb.validate_profile(dict(CONFIG, format='yuyv422'), modes)

    def test_metadata_and_unknown_formats_not_selected(self):
        self.assertEqual(usb.parse_formats("[0]: 'META'\nSize: Discrete 1920x1080\nInterval: Discrete 0.033s (30.0 fps)"), [])

    def test_instance_names_cannot_escape_config_or_systemd(self):
        for name in ('../other', 'cam@foo', 'a;reboot', '-a', '', 'A', 'tools', 'a' * 33):
            with self.subTest(name=name), self.assertRaises(ValueError):
                usb.instance_name(name)

    def test_device_rejects_arbitrary_file_and_path_traversal(self):
        for path in ('/etc/shadow', '/dev/v4l/by-id/../../video0', '/dev/video0/../video1', 'video0'):
            with self.subTest(path=path), self.assertRaises(ValueError):
                usb.device_path(path)

    def test_media_has_no_anonymous_or_cross_role_permissions(self):
        cfg = usb.media_config(CONFIG)
        self.assertEqual(cfg['authInternalUsers'], [
            {'user': 'publisher', 'pass': 'a' * 48, 'permissions': [{'action': 'publish', 'path': 'camera'}]},
            {'user': 'reader', 'pass': 'b' * 48, 'permissions': [{'action': 'read', 'path': 'camera'}]},
        ])
        self.assertEqual(cfg['rtspAddress'], '127.0.0.1:18555')
        self.assertFalse(cfg['paths']['camera']['overridePublisher'])
        for key in ('api', 'hls', 'rtmp', 'webrtc', 'moq', 'metrics', 'pprof', 'srt', 'playback'):
            self.assertFalse(cfg[key])

    def test_ffmpeg_device_argument_is_literal_no_shell_and_profile_is_selected(self):
        args = usb.ffmpeg_args(CONFIG, '/usr/bin/ffmpeg')
        self.assertEqual(args[args.index('-i') + 1], CONFIG['device'])
        self.assertEqual(args[args.index('-input_format') + 1], 'mjpeg')
        self.assertEqual(args[args.index('-video_size') + 1], '1920x1080')
        self.assertEqual(args[-1], usb.rtsp_url(CONFIG, 'publisher'))
        self.assertIn('scale=in_range=full:out_range=limited', args)
        self.assertNotIn('scale=in_range=full:out_range=limited', usb.ffmpeg_args(dict(CONFIG, format='yuyv422'), '/usr/bin/ffmpeg'))

    def test_secrets_are_redacted_from_both_processes(self):
        line = usb.rtsp_url(CONFIG, 'publisher') + usb.rtsp_url(CONFIG, 'reader')
        result = usb.redact(line, CONFIG)
        self.assertNotIn('a' * 48, result)
        self.assertNotIn('b' * 48, result)
        self.assertEqual(result.count('[REDACTED]'), 2)

    def test_ipv6_url_and_listener_are_bracketed(self):
        cfg = dict(CONFIG, listen='::1')
        self.assertIn('@[::1]:18555/', usb.rtsp_url(cfg, 'reader'))
        self.assertEqual(usb.media_config(cfg)['rtspAddress'], '[::1]:18555')
        with self.assertRaises(ValueError):
            usb.rtsp_url(CONFIG, 'reader', 'host@evil/camera')

    def test_private_write_replaces_complete_file(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'cam.json'
            usb.private_json(path, CONFIG)
            self.assertEqual(json.loads(path.read_text()), CONFIG)
            self.assertFalse(path.with_suffix('.tmp').exists())

    @patch.object(usb, 'require_root')
    @patch.object(usb, 'load', return_value=CONFIG)
    @patch.object(usb.subprocess, 'run')
    def test_stop_disables_boot_and_stops_only_selected_unit(self, run, load, root):
        usb.main(['stop', 'desk'])
        run.assert_called_once_with(['systemctl', 'disable', '--now', 'sentinel-usb@desk.service'], check=True)

    @patch.object(usb, 'require_root')
    @patch.object(usb, 'load', return_value=CONFIG)
    @patch.object(usb.subprocess, 'run')
    def test_start_does_not_enable_boot_implicitly(self, run, load, root):
        usb.main(['start', 'desk'])
        run.assert_called_once_with(['systemctl', 'start', 'sentinel-usb@desk.service'], check=True)

    @patch.object(usb, 'require_root')
    @patch.object(usb, 'load', return_value=dict(CONFIG, mode='windows-publisher'))
    def test_windows_connection_does_not_expose_reader_credential(self, load, root):
        with patch('sys.stdout', new_callable=io.StringIO) as out:
            usb.main(['connection', 'desk'])
        connection = json.loads(out.getvalue())
        self.assertIn('a' * 48, connection['url'])
        self.assertNotIn('b' * 48, out.getvalue())

    def test_cleanup_terminates_and_escalates_stuck_child(self):
        fast, stuck = Mock(), Mock()
        fast.poll.return_value = stuck.poll.return_value = None
        stuck.wait.side_effect = [subprocess.TimeoutExpired('capture', 5), 0]
        usb.stop_children([fast, stuck])
        fast.terminate.assert_called_once()
        stuck.terminate.assert_called_once()
        stuck.kill.assert_called_once()
        fast.kill.assert_not_called()

    def test_configure_refuses_existing_instance_without_overwrite(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'desk.json'
            path.write_text(json.dumps(CONFIG))
            with patch.object(usb, 'CONFIG_DIR', Path(directory)), patch.object(usb, 'require_root'):
                with self.assertRaisesRegex(ValueError, 'already exists'):
                    usb.main(['configure', 'desk', '--mode', 'windows-publisher'])
            self.assertEqual(json.loads(path.read_text()), CONFIG)

    def test_configure_rejects_wildcard_public_and_reserved_ports(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(usb, 'require_root'):
            with patch.object(usb, 'CONFIG_DIR', Path(directory)):
                for flags in (['--listen', '0.0.0.0'], ['--listen', '8.8.8.8'], ['--port', '18554'], ['--port', '80'], ['--width', '1919']):
                    with self.subTest(flags=flags), self.assertRaises(ValueError):
                        usb.main(['configure', 'desk', '--mode', 'windows-publisher'] + flags)


if __name__ == '__main__':
    unittest.main()
