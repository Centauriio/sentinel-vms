# USB cameras in Sentinel

The optional USB add-on installs on the Linux recording host. In **Admin > Cameras**, choose
**Add USB camera**, select a connected camera and one of its reported quality
modes, then add it. The camera starts stopped. Use **Start capture**, **Stop capture**,
and **USB quality** in the camera list. Sentinel creates and protects the connection
automatically; users never need a terminal command or a stream URL for routine use.
The ordinary Edit form still controls name, groups, recording and retention.
Capture and recording are separate: stopping capture also removes the live feed.
Check the new camera's recording policy; new USB registrations currently default
to continuous (24/7) recording. On narrow screens, scroll the camera table right
to reach its controls. An older camera entered manually with an RTSP URL is not
automatically converted to a managed USB camera; add it through **Add USB camera**.
Deleting a USB camera stops/removes its capture service but keeps existing footage.

## Installation

Automatic dependencies support Debian/Ubuntu on amd64/arm64 with systemd running.
From a Linux release archive built from this branch:

```sh
sudo ./install.sh --with-usb
```

To add USB support to an existing host running this branch's VMS:

```sh
sudo bash linux/packaging/usb/install.sh
```

The installer checks Python 3, V4L2 utilities, FFmpeg V4L2/libx264 capabilities,
curl, CA certificates, tar and gzip; apt installs missing prerequisites. MediaMTX
1.21.3 is pinned and its archive checksum verified. Other distributions must
supply these dependencies themselves. A Docker image rebuild alone does not
install host services or expose physical devices.

A small standalone package is also built by the **USB add-on** GitHub Actions
workflow: download its `sentinel-usb-addon` artifact, verify the ZIP checksum, and
extract `sentinel-usb-addon.zip`. It contains the Linux/Windows installers and
diagnostic scripts without VMS binaries, credentials, or downloaded dependencies.
Build the same package from a checkout with:

```sh
python3 linux/scripts/build-usb-addon.py
```

Outputs are `linux/dist/sentinel-usb-addon.zip` and its `.sha256` checksum.
The full Linux build also produces this package. Files in it have Linux-compatible
line endings even when packaged from a Windows checkout.

Reinstallation preserves camera credentials and host networking/UID settings.
It refreshes the control service; it does not choose a camera or start a new one.
Stop running captures in the web UI before upgrading, then start them afterward
so they load the new capture code. USB cameras remain stopped after a host reboot;
start them from the camera list. The control service starts automatically.

## Native Linux

Supported inputs are V4L2 capture devices with discrete MJPEG, YUYV or NV12 modes.
Stable `/dev/v4l/by-id` identities are preferred over numbered video nodes. Metadata
nodes and unsupported modes are omitted. Only reported resolution/frame-rate
combinations are offered: a 1080p30 camera will not be offered 1080p60. Range-only
capabilities are not guessed. Encoding is H.264 software encoding with no audio;
higher quality needs more CPU. Camera selection targets the server's devices,
not cameras connected to the browser's computer.

## WSL and Windows cameras

For an existing VMS in WSL, run the package's host installer from **Windows
PowerShell on the PC hosting that WSL distribution**. Keep all extracted files
together. For a native systemd installation:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\Install-SentinelUsbHost.ps1 -Distribution Ubuntu
```

For Docker/Dokploy, first configure the socket-directory mount shown below, then
pass the actual VMS container name:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\Install-SentinelUsbHost.ps1 -Distribution Ubuntu -Container YOUR_VMS_CONTAINER
```

The installer detects the container UID and WSL private address, checks/installs
Linux dependencies, installs the WindowsCapture helper, and waits for a real
Windows inventory heartbeat. No camera is started. Named container users or UID
remapping require an explicit `-WebUid HOST_UID`; `-ListenAddress PRIVATE_IP`
overrides address detection. It refuses remote Docker daemons or an absent socket
mount. It does not edit Compose, change firewall rules, or restart WSL/the VMS.
An already running Windows helper is stopped/replaced during upgrade; stop capture
in the web interface before reinstalling. The same installer upgrades the files.

The Linux installer detects WSL and, when Windows interop is available, invokes
the included Windows installer. It checks/installs FFmpeg through winget, enrolls
the default WSL user for helper access, and registers a hidden per-user Windows
logon task. Windows camera inventory then appears in the same web camera picker.
An initial winget/UAC prompt may be required by the Windows package installer.

For an installation over SSH or without Windows interop, finish the one-time
Windows helper installation from Windows PowerShell using all the shipped `.ps1`
files together (also available at `/opt/sentinel-usb/windows`):

```powershell
.\Install-SentinelWslUsb.ps1 -Backend WindowsCapture -Distribution Ubuntu
```

The Linux add-on must already be installed in that distribution. Normal camera
operations thereafter are entirely in the web interface. The helper runs while
that Windows user is signed in, including a locked session/display off. Windows
sleep, sign-out and shutdown interrupt capture. The helper polls for changes,
retries failed capture with a delay and stops its owned processes when it loses
broker communication. A Windows job object cleans up FFmpeg if the helper is
forcibly stopped. It never kills unrelated FFmpeg processes.

Windows capture reads the camera through DirectShow, avoiding USB/IP conflicts
with StarDesk or other remote-access tools. It does not replace drivers, stop
providers or restart WSL. WSL2 must already have systemd enabled; the installer
diagnoses absent systemd but does not change its configuration or reboot it.

### Optional USB/IP passthrough

If a camera should instead be attached to Linux, use `--skip-windows-helper` with
the Linux USB installer. The advanced Windows installer retains a USB/IP mode:

```powershell
.\Install-SentinelWslUsb.ps1 -Backend UsbIp -Distribution Ubuntu
# Supply the selected camera's CURRENT bus ID and VID:PID:
.\Install-SentinelWslUsb.ps1 -Backend UsbIp -Distribution Ubuntu -BusId '1-4' -HardwareId '32e4:4801'
```

Those example identifiers must be replaced for the selected physical camera.
The script checks the owner of TCP 3240, reuses a compatible healthy provider by
absolute path, and installs standalone usbipd only when no provider exists. It
refuses unknown/conflicting providers or driver warnings. Only the explicitly
selected matching device is attached. No force-bind, provider restart or blind
retry loop is used. Kernel UVC/V4L2 support is still required; USB enumeration
alone does not prove capture support. Once attached and supported, the camera
appears in the web picker as Linux. Reattachment after reboot/replug can require
this advanced installer step; Windows capture is the default for routine web use.

## Docker / Dokploy setup

The broker runs on the host, alongside Docker, and the VMS must run this branch's
web/API build. One-time administrator setup must provide the actual VMS process
UID and a specific private host address reachable from both the VMS container
and, for Windows capture, Windows:

```sh
sudo bash linux/packaging/usb/install.sh --web-uid 65532 --listen-address PRIVATE_HOST_IP
```

`65532` is an example, not a guaranteed image UID. Mount the runtime directory in
the VMS container at the same path (mount the directory, not the socket inode,
so broker restarts work):

```yaml
volumes:
  - /run/sentinel-usb-control:/run/sentinel-usb-control:ro
```

The broker unit uses `RuntimeDirectoryPreserve=yes` so stopping/restarting it
keeps this mounted directory. The directory is still recreated after host reboot.
For a native Linux Docker host, the same preflight can infer UID/address:

```sh
sudo bash linux/packaging/usb/install-host.sh --container YOUR_VMS_CONTAINER
```

Read-only mounting still permits Unix-socket connections; file ownership limits
control access to the configured VMS UID. Container user-namespace remapping
requires the corresponding host UID. Do not mount the Docker socket or grant the
VMS privileged mode for this feature. The local recorder must be connected.

The host address is used automatically when each camera is created. Container
localhost is not host localhost; default loopback only works for native installs.
WSL NAT addresses can change after restart. Use stable host networking or recreate
affected cameras after correcting the install address. Existing camera endpoints
are preserved on upgrade. Docker networks and firewall rules are not rewritten.
The earlier manually deployed test bridge is separate and is not adopted or stopped.

## Control and credential boundaries

The root broker exposes only a fixed camera API on local Unix sockets. The VMS
socket is mode 0600 owned by its configured UID; a separate `sentinel-usb-agent`
group socket accepts only Windows inventory/status polls and returns publisher
credentials for requested cameras. It cannot configure cameras or read reader
credentials. Both browser authentication and Manage Cameras permissions apply;
device inventory/setup additionally require site-wide camera scope.

Per-camera capture services run as the unprivileged `sentinel-usb` user with video
group access. Credentials are protected in `/etc/sentinel-usb`, reader passwords
are encrypted by Sentinel's existing camera store, and browser USB responses
contain no secrets. FFmpeg still receives its publisher URL as an argument, so
local administrators can inspect it. RTSP/TCP is authenticated but unencrypted;
keep the host/container hop on the same machine or trusted private/VPN network.
No stream port is exposed to the public Internet by the installer.

## Diagnostics and validation

For Windows discovery or polling problems, run the shipped diagnostic in a
temporary process (no persistent execution-policy change):

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\Test-SentinelWindowsHelper.ps1 -Distribution Ubuntu
```

It prints the failing phase and script location without publisher credentials.
It makes one inventory poll; that is not proof the scheduled helper stays online.
`Running` in Task Scheduler only means the process is alive. An empty camera list
with `windowsOnline: false` means it is not reporting to Linux. The launcher keeps
WSL switches unquoted and uses `--exec`, avoiding the failure where WSL tries to
run `-d` as a Linux command. Stop/restart only Sentinel's own task during repair;
do not restart StarDesk or its USB/IP service.

The `sentinel-usb` CLI remains available for administrator diagnostics and legacy
standalone capture hosts. Legacy CLI cameras are not silently adopted by the web
UI. Do not edit their credentials or manually repurpose `web-*` configurations.

```sh
sudo sentinel-usb doctor
sudo journalctl -u sentinel-usb-control
python3 -m unittest discover -s linux/packaging/usb/tests -v
```

CI checks real Linux installation/reinstallation, systemd lifecycle, Unix socket
permissions, authenticated synthetic RTSP publish/read, role rejection, Windows
device/mode parsing, argument quoting and owned-process cleanup. Go API tests
cover permissions, secret boundaries, camera registration, updates, stop/delete,
and failure cleanup. Dashboard tests/build cover the selection and API contract.

Physical acceptance on 2026-11-15 confirmed Windows DirectShow discovery of a
48MP USB Camera with eight modes, registration through the web UI, and
Start > Stop > Start with Online/Capturing > No signal/Stopped > Online/Capturing
at 1920x1080 MJPEG 30fps input and H.264 4000kbps output. The VMS ran in Docker on
Ubuntu/WSL2; StarDesk and its USB/IP provider stayed running. This evidence covers
that device and setup, not every platform or camera.

Further hardware acceptance is required: other Windows and native Linux devices,
busy/unplug/replug, WSL restart, sustained capture, image quality, and Sentinel
live view/recording/playback. Automated checks do not establish those results.
