#!/usr/bin/env bash
# Install the optional capture add-on, never start a camera or restart the VMS.
set -euo pipefail
web_uid=""
listen_address=""
skip_windows=0
while [[ $# -gt 0 ]]; do
    case "$1" in
        --web-uid) web_uid="${2:?Missing web-server UID}"; shift 2 ;;
        --listen-address) listen_address="${2:?Missing private listen address}"; shift 2 ;;
        --skip-windows-helper) skip_windows=1; shift ;;
        --help) echo 'Usage: sudo bash install.sh [--web-uid UID] [--listen-address PRIVATE_IP] [--skip-windows-helper]'; exit 0 ;;
        *) echo 'Unknown USB installer option.' >&2; exit 2 ;;
    esac
done
if [[ $(id -u) -ne 0 ]]; then echo 'Run with sudo.' >&2; exit 1; fi
here="$(cd "$(dirname "$0")" && pwd)"
if [[ ! -d /run/systemd/system ]] || ! command -v systemctl >/dev/null; then
    echo 'A running systemd instance is required. For WSL, enable systemd first; no WSL restart is performed by this installer.' >&2
    exit 1
fi

ffmpeg_usable() {
    [[ -n "$1" && -x "$1" ]] &&
        "$1" -hide_banner -devices 2>/dev/null | grep 'video4linux2' >/dev/null &&
        "$1" -hide_banner -encoders 2>/dev/null | grep -w 'libx264' >/dev/null
}
missing=()
[[ -x /usr/bin/python3 ]] || missing+=(python3)
command -v v4l2-ctl >/dev/null || missing+=(v4l-utils)
ffmpeg_bin=/usr/bin/ffmpeg
if ! ffmpeg_usable "$ffmpeg_bin"; then ffmpeg_bin="$(command -v ffmpeg || true)"; fi
if ! ffmpeg_usable "$ffmpeg_bin"; then missing+=(ffmpeg); fi
command -v curl >/dev/null || missing+=(curl)
[[ -s /etc/ssl/certs/ca-certificates.crt ]] || missing+=(ca-certificates)
command -v tar >/dev/null || missing+=(tar)
command -v gzip >/dev/null || missing+=(gzip)
if [[ ${#missing[@]} -gt 0 ]]; then
    if ! command -v apt-get >/dev/null; then
        printf 'Install these prerequisites with your distribution package manager, then rerun: %s\n' "${missing[*]}" >&2
        echo 'Automatic package installation currently supports Debian/Ubuntu. FFmpeg must have V4L2 input and libx264.' >&2
        exit 1
    fi
    echo "Installing missing prerequisites: ${missing[*]}"
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "${missing[@]}"
else
    echo 'Required packages are already available; no package installation needed.'
fi
# Prefer the distro binary after apt installation, avoiding a decode-only binary shadowing PATH.
ffmpeg_bin=/usr/bin/ffmpeg
if ! ffmpeg_usable "$ffmpeg_bin"; then ffmpeg_bin="$(command -v ffmpeg || true)"; fi
if ! ffmpeg_usable "$ffmpeg_bin"; then
    echo 'FFmpeg still lacks V4L2/libx264. Install an encoding-capable build; no service was started.' >&2
    exit 1
fi
python_bin=/usr/bin/python3
v4l2_bin="$(command -v v4l2-ctl)"

install -d -m 0755 /opt/sentinel-usb
if [[ -f /etc/sentinel-usb/tools.json ]]; then
    if [[ -z "$web_uid" ]]; then web_uid="$("$python_bin" -c 'import json; print(json.load(open("/etc/sentinel-usb/tools.json")).get("webUID", ""))')"; fi
    if [[ -z "$listen_address" ]]; then listen_address="$("$python_bin" -c 'import json; print(json.load(open("/etc/sentinel-usb/tools.json")).get("listen", ""))')"; fi
fi
listen_address="${listen_address:-127.0.0.1}"
if [[ -z "$web_uid" ]]; then web_uid="$(id -u sentinel 2>/dev/null || echo 0)"; fi
"$python_bin" - "$web_uid" "$listen_address" <<'VALIDATE'
import ipaddress, sys
assert sys.argv[1].isdigit() and 0 <= int(sys.argv[1]) <= 2147483647, 'Invalid web-server UID'
ip = ipaddress.ip_address(sys.argv[2])
assert not ip.is_unspecified and not ip.is_multicast and (ip.is_loopback or ip.is_private), 'Use a specific private listen address'
VALIDATE
mtx_bin=/opt/sentinel-usb/mediamtx
# Reuse a compatible bundled version, but never overwrite the VMS's MediaMTX.
for candidate in /opt/sentinel/mediamtx /opt/sentinel-usb/mediamtx; do
    if [[ -x "$candidate" ]] && [[ "$("$candidate" --version 2>/dev/null)" == v1.21.3 ]]; then
        mtx_bin="$candidate"
        break
    fi
done
if [[ ! -x "$mtx_bin" ]] || [[ "$("$mtx_bin" --version 2>/dev/null)" != v1.21.3 ]]; then
    case "$(uname -m)" in
        x86_64) arch=amd64; digest=0a2ea7eba96023631efa1bea7804823a9145b043b6646a8a05798d4ac24ed635 ;;
        aarch64|arm64) arch=arm64; digest=27f038237cb5f77a724f6afc58eca2798f2bc4edf9989895631a80eeb41a4fe2 ;;
        *) echo 'USB add-on supports Linux amd64 and arm64.' >&2; exit 1 ;;
    esac
    temp_dir="$(mktemp -d)"
    trap 'rm -rf -- "$temp_dir"' EXIT
    curl --fail --location --proto '=https' --tlsv1.2 --retry 3 \
        "https://github.com/bluenviron/mediamtx/releases/download/v1.21.3/mediamtx_v1.21.3_linux_${arch}.tar.gz" \
        -o "$temp_dir/mediamtx.tar.gz"
    printf '%s  %s\n' "$digest" "$temp_dir/mediamtx.tar.gz" | sha256sum --check --status
    tar -xzf "$temp_dir/mediamtx.tar.gz" -C "$temp_dir" mediamtx LICENSE
    install -m 0755 "$temp_dir/mediamtx" /opt/sentinel-usb/mediamtx
    install -m 0644 "$temp_dir/LICENSE" /opt/sentinel-usb/MediaMTX-LICENSE
fi
getent group video >/dev/null || groupadd --system video
getent group sentinel-usb-agent >/dev/null || groupadd --system sentinel-usb-agent
if ! id sentinel-usb >/dev/null 2>&1; then
    useradd --system --user-group --home-dir /nonexistent --shell /usr/sbin/nologin sentinel-usb
fi
usermod -aG video sentinel-usb
install -d -o root -g sentinel-usb -m 0750 /etc/sentinel-usb
install -m 0755 "$here/sentinel_usb.py" /opt/sentinel-usb/sentinel_usb.py
install -m 0755 "$here/usb_control.py" /opt/sentinel-usb/usb_control.py
install -m 0644 "$here/sentinel-usb@.service" /etc/systemd/system/sentinel-usb@.service
install -m 0644 "$here/sentinel-usb-control.service" /etc/systemd/system/sentinel-usb-control.service
install -d -m 0755 /opt/sentinel-usb/windows /usr/local/bin
install -m 0644 "$here"/*.ps1 /opt/sentinel-usb/windows/
install -m 0644 "$here/README.md" /opt/sentinel-usb/README.md
cat > /usr/local/bin/sentinel-usb <<'WRAPPER'
#!/bin/sh
exec /usr/bin/python3 /opt/sentinel-usb/sentinel_usb.py "$@"
WRAPPER
chmod 0755 /usr/local/bin/sentinel-usb
"$python_bin" - "$ffmpeg_bin" "$v4l2_bin" "$mtx_bin" "$web_uid" "$listen_address" <<'PY'
import json, os, sys
path = '/etc/sentinel-usb/tools.json'
with open(path, 'w') as f:
    value = dict(zip(('ffmpeg', 'v4l2', 'mediamtx'), sys.argv[1:4]))
    value.update(webUID=int(sys.argv[4]), listen=sys.argv[5])
    json.dump(value, f)
os.chmod(path, 0o640)
PY
chown root:sentinel-usb /etc/sentinel-usb/tools.json
systemctl daemon-reload
systemctl enable --now sentinel-usb-control.service
# Upgrade only the control broker; camera capture units remain under explicit user control.
systemctl restart sentinel-usb-control.service
sentinel-usb doctor
echo 'USB add-on installed. Existing camera configurations are preserved. No camera was started.'
echo 'Next: open Sentinel Cameras and choose Add USB camera.'
if grep -qi microsoft /proc/sys/kernel/osrelease; then
    if [[ "$skip_windows" -eq 0 ]]; then
        powershell=/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe
        if [[ ! -x "$powershell" || -z "${WSL_DISTRO_NAME:-}" ]] || ! command -v wslpath >/dev/null; then
            echo 'Linux setup completed. Windows interop is unavailable; run the included Install-SentinelWslUsb.ps1 once from Windows to install its camera helper.' >&2
            exit 1
        fi
        "$powershell" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$(wslpath -w /opt/sentinel-usb/windows/Install-SentinelWslUsb.ps1)" -Distribution "$WSL_DISTRO_NAME"
    fi
fi
