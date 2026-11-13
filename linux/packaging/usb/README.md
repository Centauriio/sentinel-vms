# USB camera add-on

This optional add-on turns a Linux V4L2 camera into an authenticated RTSP source
that Sentinel can record and display. It installs alongside the server and also
works on a separate Linux capture host. The Electron operator remains a viewer:
install this add-on on the machine physically connected to the camera.

Camera setup uses `sentinel-usb`; add its URL through Sentinel's existing camera
form. There is no new web setup page in this version. Installing the add-on does
not select a camera, start capture, or change the VMS database.

## Install

Supported automatic dependency setup: Debian/Ubuntu, amd64/arm64, with systemd
running. Other distributions can provide the dependencies themselves, then run
the installer. Dependencies are Python 3, V4L2 utilities, FFmpeg with V4L2 input
and libx264 encoding, curl, CA certificates, tar and gzip. The VMS's bundled
decode-oriented FFmpeg is not assumed to support USB encoding.

From a Linux release archive:

```sh
sudo ./install.sh --with-usb
```

To add capture without reinstalling the VMS, from this repository:

```sh
sudo bash linux/packaging/usb/install.sh
```

The installer checks executable capabilities, installs missing packages through
apt, and verifies the exact MediaMTX 1.21.3 archive SHA-256 before installing it.
It uses its own MediaMTX when the VMS has a different version. Reinstalling
preserves camera configurations and credentials and does not enable/start any
camera. Existing running capture instances should be stopped before upgrading
and started afterward to load the new files. OS packages are not removed by
camera removal because other applications may use them.

## Native Linux or an already attached WSL camera

```sh
sudo sentinel-usb doctor
sudo sentinel-usb devices
sudo sentinel-usb configure entrance \
  --device /dev/v4l/by-id/YOUR-CAMERA-video-index0 \
  --format mjpeg --width 1920 --height 1080 --fps 30
sudo sentinel-usb start entrance
sudo sentinel-usb url entrance
```

Choose a path and an advertised format/size/frame rate from `devices`; prefer
`/dev/v4l/by-id/` for stable identity after replugging. A metadata-only node is not
selectable. Discrete MJPEG, YUYV and NV12 modes are supported. Stepwise/continuous
mode ranges are not automatically guessed. Unsupported 1080p60 is rejected, not
silently replaced with 30 fps. Multiple instances require different `--port`
values; the default is 18555 because Sentinel already uses 18554 internally.

Paste the credential-bearing URL into Sentinel's Add camera form on the **same
host**, choose RTSP/TCP, and verify live view and recording. Treat `url` output as
a secret. The bridge does not save footage; recording is owned by Sentinel.

```sh
sudo sentinel-usb status entrance
sudo journalctl -u sentinel-usb@entrance -f
sudo sentinel-usb stop entrance
# Optional automatic startup, only when deliberately selected:
sudo sentinel-usb start entrance --on-boot
# Stop, disable and remove only this add-on instance and its credentials:
sudo sentinel-usb remove entrance
```

Stop also disables boot startup. A camera recording switch inside Sentinel does
not stop the add-on capture process. Missing/busy devices and terminated capture
processes trigger retries every ten seconds while the service remains enabled;
`stop` ends retries. A stable symlink allows the same device to resume after
replugging. Do not rely on `/dev/video0` retaining its identity.

## WSL setup

Use WSL2 with systemd enabled. The Linux installer diagnoses absent systemd but
does not change `/etc/wsl.conf`, shut down WSL, or restart Dokploy. Follow
[Microsoft's systemd instructions](https://learn.microsoft.com/en-us/windows/wsl/systemd)
if a planned WSL restart is needed. Run the Linux installer in the selected
distribution. Copy **all four PowerShell files, including WslUsb.Common.ps1**, from
this directory (also installed under `/opt/sentinel-usb/windows`) to one folder
on the Windows host.

### Windows capture (recommended when another application owns USB/IP)

In Windows PowerShell:

```powershell
.\Install-SentinelWslUsb.ps1 -Backend WindowsCapture -Distribution Ubuntu
ffmpeg -hide_banner -list_devices true -f dshow -i dummy
ffmpeg -hide_banner -list_options true -f dshow -i 'YOUR CAMERA NAME'
```

The setup installs Gyan.FFmpeg through winget only if FFmpeg is absent and checks
DirectShow/libx264 support. Enumeration commands normally return a nonzero exit
code after printing modes. If winget is missing, install Microsoft's App Installer
first. Existing incompatible FFmpeg builds are reported rather than replaced.

In Ubuntu:

```sh
sudo sentinel-usb configure desk --mode windows-publisher \
  --width 1920 --height 1080 --fps 30 --format mjpeg
sudo sentinel-usb start desk
```

In Windows PowerShell, select the exact camera name and start streaming:

```powershell
.\Start-SentinelWindowsCamera.ps1 -Name desk -CameraName 'YOUR CAMERA NAME'
```

The script reads the instance's publisher credential through `sudo` in WSL and
redacts it from FFmpeg output. Enter the Ubuntu password when prompted. The
requested profile must be supported by the Windows camera; DirectShow rejects
unsupported modes. Keep the window open; Ctrl+C stops Windows capture. No startup
task is installed. Windows must remain awake; display-off is different from sleep.

In a second PowerShell window, copy the reader URL privately:

```powershell
.\Copy-SentinelUsbCameraUrl.ps1 -Name desk
# Paste into Sentinel only, then clear:
Set-Clipboard -Value ''
```

Camera credentials are protected in `/etc/sentinel-usb` and runtime files; FFmpeg
still receives the publishing URL as a process argument. Local administrators
can inspect it. Do not paste credential-bearing URLs into chat/issues or logs.

### USB/IP passthrough

In **elevated** Windows PowerShell:

```powershell
.\Install-SentinelWslUsb.ps1 -Backend UsbIp -Distribution Ubuntu
# Read the listing and choose the camera's current bus ID and VID:PID:
.\Install-SentinelWslUsb.ps1 -Backend UsbIp -Distribution Ubuntu -BusId '1-4' -HardwareId '32e4:4801'
```

The example IDs must be replaced with the user's selected camera.
The helper inspects TCP 3240 and its executable owner before making
changes. An existing healthy compatible provider is reused by absolute path,
including an app-managed helper. It never kills that process or replaces its
driver. If no provider exists it installs standalone usbipd through an interactive
winget installer, allowing a required reboot to be deferred. A provider with
warnings, an unknown owner or a conflicting listener blocks attachment with an
actionable WindowsCapture alternative. This does not promise compatibility with
every vendor fork.

Only the explicitly selected bus ID with matching VID:PID is bound/attached.
Re-run after replugging to verify identity. Identical VID:PID devices require
manual selection of the correct physical camera. No force-bind, automatic detach,
unrelated-device change, WSL shutdown or provider-restart loop is performed.

In Ubuntu, verify `sudo sentinel-usb devices` returns camera modes, then use the
native Linux setup above. USB enumeration alone is insufficient: the WSL kernel
must support UVC/V4L2. A missing driver may require a supported kernel update or
the WindowsCapture backend; the installer does not build/replace kernels. While
attached to WSL the device is unavailable to Windows consumers. See
[Microsoft's USB/IP documentation](https://learn.microsoft.com/en-us/windows/wsl/connect-usb).

## Docker / remote VMS

An add-on listening on 127.0.0.1 is reachable only on its Linux host. Container
localhost is **not** host localhost. For a VMS in Docker, explicitly choose a
reachable private host address with `configure --listen PRIVATE_IP --port 18555`
and use that address in the camera URL. For native capture on a Docker host, a
dedicated Docker bridge's gateway address is one option. For WindowsCapture into
WSL, use a WSL address reachable from both Windows and the VMS container, and
recheck it after WSL restarts. The tool rejects wildcard and public bind addresses.
It does not edit Docker networks or firewall rules automatically.

This RTSP link is authenticated but not encrypted: keep it on the same host or a
trusted private/VPN network. The VMS's HTTPS viewer does not encrypt this separate
capture hop. The earlier manually deployed Docker bridge is separate and is not
adopted, stopped or removed by this installer.

## Validation

```sh
python3 -m unittest discover -s linux/packaging/usb/tests -v
bash -n linux/packaging/usb/install.sh
```

Windows: `powershell -NoProfile -File linux/packaging/usb/tests/Test-WslProvider.ps1`.
CI also installs the add-on on Linux and tests synthetic RTSP publish/read plus
authentication rejection. Hardware acceptance still requires a real Linux USB
camera and WSL/Windows device tests: unplug/replug, busy camera, reboot, sustained
capture, image color range, Sentinel playback and recording. These are not proven
by syntax/unit tests or by the earlier Windows proof of concept.
