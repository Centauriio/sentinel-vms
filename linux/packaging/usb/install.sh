#!/usr/bin/env bash
# Install the optional capture add-on, never start a camera or restart the VMS.
set -euo pipefail
if [[ $# -ne 0 ]]; then echo 'Usage: sudo bash install.sh' >&2; exit 2; fi
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
if ! id sentinel-usb >/dev/null 2>&1; then
    useradd --system --user-group --home-dir /nonexistent --shell /usr/sbin/nologin sentinel-usb
fi
usermod -aG video sentinel-usb
install -d -o root -g sentinel-usb -m 0750 /etc/sentinel-usb
install -m 0755 "$here/sentinel_usb.py" /opt/sentinel-usb/sentinel_usb.py
install -m 0644 "$here/sentinel-usb@.service" /etc/systemd/system/sentinel-usb@.service
install -d -m 0755 /opt/sentinel-usb/windows /usr/local/bin
install -m 0644 "$here"/*.ps1 /opt/sentinel-usb/windows/
install -m 0644 "$here/README.md" /opt/sentinel-usb/README.md
cat > /usr/local/bin/sentinel-usb <<'WRAPPER'
#!/bin/sh
exec /usr/bin/python3 /opt/sentinel-usb/sentinel_usb.py "$@"
WRAPPER
chmod 0755 /usr/local/bin/sentinel-usb
"$python_bin" - "$ffmpeg_bin" "$v4l2_bin" "$mtx_bin" <<'PY'
import json, os, sys
path = '/etc/sentinel-usb/tools.json'
with open(path, 'w') as f:
    json.dump(dict(zip(('ffmpeg', 'v4l2', 'mediamtx'), sys.argv[1:])), f)
os.chmod(path, 0o640)
PY
chown root:sentinel-usb /etc/sentinel-usb/tools.json
systemctl daemon-reload
sentinel-usb doctor
echo 'USB add-on installed. Existing camera configurations are preserved. No camera was started.'
echo 'Next: sudo sentinel-usb devices'
if grep -qi microsoft /proc/sys/kernel/osrelease; then
    echo 'WSL detected: Windows setup scripts are in /opt/sentinel-usb/windows. Choose USB/IP or WindowsCapture there.'
fi
