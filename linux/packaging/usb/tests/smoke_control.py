"""Linux CI: real broker sockets, systemd lifecycle and OS permission boundaries."""
import http.client
from contextlib import closing
import json
import os
from pathlib import Path
import socket
import subprocess
import time


class Connection(http.client.HTTPConnection):
    def __init__(self, name):
        super().__init__('localhost', timeout=20)
        self.name = name
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(20)
        self.sock.connect('/run/sentinel-usb-control/' + self.name + '.sock')


def request(name, method, path, data=None, expected=200):
    with closing(Connection(name)) as client:
        client.request(method, path, json.dumps(data or {}), {'Content-Type':'application/json'})
        response = client.getresponse()
        body = json.loads(response.read())
        if response.status != expected: raise RuntimeError('Broker request failed: '+method+' '+path)
        return body


def main():
    if os.geteuid() != 0: raise RuntimeError('Run isolated CI smoke as root.')
    name = 'web-' + 'e'*24
    device = 'windows:' + 'f'*32
    profile = dict(format='mjpeg', width=1920, height=1080, fps=30, bitrate=4000)
    report = dict(devices=[dict(id=device, name='CI synthetic camera', modes=[{k:v for k,v in profile.items() if k!='bitrate'}])], status={})
    created = False
    for _ in range(40):
        if Path('/run/sentinel-usb-control/control.sock').exists(): break
        time.sleep(.25)
    try:
        request('agent','GET','/inventory',expected=400)
        request('agent','POST','/poll',report)
        data=request('control','POST','/cameras',dict(instance=name,device=device,profile=profile));created=True
        assert data['state']=='stopped' and not data['requested']
        assert 'reader:' in data['url']
        public=request('control','GET','/inventory')
        assert 'Password' not in json.dumps(public) and 'rtsp://' not in json.dumps(public)
        request('control','POST','/cameras/'+name+'/start')
        commands=request('agent','POST','/poll',report)['cameras']
        assert len(commands)==1 and 'publisher:' in commands[0]['url'] and 'reader:' not in json.dumps(commands)
        request('control','PUT','/cameras/'+name,{'profile':dict(profile,bitrate=2000)})
        request('control','POST','/cameras/'+name+'/stop')
        assert request('agent','POST','/poll',report)['cameras']==[]
        denied=subprocess.run(['runuser','-u','nobody','--','curl','--silent','--unix-socket','/run/sentinel-usb-control/control.sock','http://localhost/inventory'],capture_output=True)
        assert denied.returncode!=0, 'Unprivileged user accessed control socket'
        denied=subprocess.run(['runuser','-u','nobody','-g','sentinel-usb-agent','--','curl','--silent','--unix-socket','/run/sentinel-usb-control/control.sock','http://localhost/inventory'],capture_output=True)
        assert denied.returncode!=0, 'Agent group accessed control socket'
        allowed=subprocess.run(['runuser','-u','nobody','-g','sentinel-usb-agent','--','curl','--silent','--unix-socket','/run/sentinel-usb-control/agent.sock','-H','Content-Type: application/json','-d','{"devices":[],"status":{}}','http://localhost/poll'],capture_output=True)
        assert allowed.returncode==0 and json.loads(allowed.stdout)=={'cameras':[]}
        print('Broker lifecycle, credential separation, and Unix socket permissions passed.')
    finally:
        if created: request('control','DELETE','/cameras/'+name)


if __name__ == '__main__': main()
