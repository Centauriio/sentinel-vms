"""Exercise host preflight/routing with fake commands; never install host services."""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


@unittest.skipUnless(sys.platform == 'linux' and Path('/run/systemd/system').exists(), 'Linux systemd preflight required')
class HostInstallTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        shutil.copyfile(Path(__file__).parents[1] / 'install-host.sh', self.root / 'install-host.sh')
        (self.root / 'install.sh').write_text('#!/bin/bash\nprintf "%s\\n" "$@" > "$TEST_ARGS"\n')
        self.fake('id', 'if [[ "$*" == "-u sentinel" ]]; then [[ "$SCENARIO" != missing_native ]] || exit 1; echo 1001; else echo 0; fi')
        self.fake('ip', 'echo "1.1.1.1 via 172.20.0.1 dev eth0 src 172.20.0.2 uid 0"')
        self.fake('curl', 'exit 0')
        self.fake('docker', '''
case "$*" in
 "context inspect "*) echo 'unix:///var/run/docker.sock' ;;
 "info "*) if [[ "$SCENARIO" == userns ]]; then echo '["name=userns"]'; else echo '[]'; fi ;;
 *State.Running*) echo true ;;
 *Config.User*) if [[ "$SCENARIO" == named_user ]]; then echo nobody; else echo 65532:65532; fi ;;
 *Mounts*) if [[ "$SCENARIO" != missing_mount ]]; then echo '/run/sentinel-usb-control /run/sentinel-usb-control'; fi ;;
 *) exit 88 ;;
esac
''')

    def fake(self, name, body):
        path = self.bin / name
        path.write_text('#!/bin/bash\nset -eu\n' + body + '\n')
        path.chmod(0o755)

    def run_install(self, args, scenario='', docker_host=''):
        env = dict(os.environ, PATH=str(self.bin) + ':' + os.environ['PATH'], SCENARIO=scenario,
                   TEST_ARGS=str(self.root / 'args'), DOCKER_HOST=docker_host, DOCKER_CONTEXT='')
        return subprocess.run(['bash', str(self.root / 'install-host.sh'), *args], env=env, capture_output=True, text=True)

    def test_docker_uid_and_private_route_pass_to_installer(self):
        result = self.run_install(['--container', 'my-vms'])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.root / 'args').read_text().splitlines(),
                         ['--web-uid', '65532', '--listen-address', '172.20.0.2', '--skip-windows-helper'])

    def test_native_uid(self):
        result = self.run_install([])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('1001', (self.root / 'args').read_text().splitlines())

    def test_unsafe_or_ambiguous_setup_stops_before_install(self):
        for scenario in ('missing_mount', 'named_user', 'userns'):
            with self.subTest(scenario=scenario):
                self.assertNotEqual(self.run_install(['--container', 'my-vms'], scenario).returncode, 0)
                self.assertFalse((self.root / 'args').exists())
        self.assertNotEqual(self.run_install([], 'missing_native').returncode, 0)
        self.assertNotEqual(self.run_install(['--container', 'my-vms'], docker_host='tcp://other-host:2376').returncode, 0)
        self.assertFalse((self.root / 'args').exists())

    def test_explicit_host_uid_for_user_mapping(self):
        result = self.run_install(['--container', 'my-vms', '--web-uid', '165532'], 'userns')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('165532', (self.root / 'args').read_text().splitlines())
