#!/bin/sh
# Installs Sentinel VMS as a systemd service. Run from the extracted release:
#   sudo ./install.sh
set -eu

with_usb=false
case "${1:-}" in
    --with-usb) with_usb=true; shift ;;
    --help|-h) echo 'Usage: sudo ./install.sh [--with-usb]'; exit 0 ;;
    '') ;;
    *) echo 'Usage: sudo ./install.sh [--with-usb]' >&2; exit 2 ;;
esac
[ "$#" -eq 0 ] || { echo 'Unexpected installer arguments' >&2; exit 2; }

if [ "$(id -u)" -ne 0 ]; then
    echo "Run as root: sudo ./install.sh" >&2
    exit 1
fi
if ! command -v systemctl >/dev/null 2>&1; then
    echo "systemd not found. Run ./sentinel -data /path/to/data directly, or use the Docker image." >&2
    exit 1
fi

here="$(cd "$(dirname "$0")" && pwd)"

if ! id sentinel >/dev/null 2>&1; then
    useradd --system --home-dir /var/lib/sentinel --shell /usr/sbin/nologin sentinel
fi
# Hardware-accelerated decode/detection: GPU render nodes (/dev/dri/renderD*)
# belong to "render" (or "video" on older distros).
for g in render video; do
    if getent group "$g" >/dev/null 2>&1; then usermod -aG "$g" sentinel; fi
done

install -d -m 0755 /opt/sentinel
install -m 0755 "$here/sentinel" /opt/sentinel/sentinel
install -m 0755 "$here/mediamtx" /opt/sentinel/mediamtx
# Optional: iPhone remote access (Cloudflare quick tunnel).
[ -f "$here/cloudflared" ] && install -m 0755 "$here/cloudflared" /opt/sentinel/cloudflared
# Motion detection and event snapshots.
[ -f "$here/ffmpeg" ] && install -m 0755 "$here/ffmpeg" /opt/sentinel/ffmpeg
# Person/vehicle detection (ONNX Runtime + YOLOX-Tiny).
if [ -f "$here/lib/libonnxruntime.so" ] && [ -f "$here/models/yolox_tiny.onnx" ]; then
    install -d -m 0755 /opt/sentinel/lib /opt/sentinel/models
    install -m 0755 "$here/lib/libonnxruntime.so" /opt/sentinel/lib/libonnxruntime.so
    for m in "$here"/models/*; do install -m 0644 "$m" "/opt/sentinel/models/$(basename "$m")"; done
fi
for f in THIRD-PARTY.txt ffmpeg-LICENSE.txt onnxruntime-LICENSE.txt; do [ -f "$here/$f" ] && install -m 0644 "$here/$f" /opt/sentinel/$f; done
install -m 0644 "$here/sentinel.service" /etc/systemd/system/sentinel.service

if [ "$with_usb" = true ]; then
    bash "$here/usb/install.sh"
fi

systemctl daemon-reload
systemctl enable sentinel
systemctl restart sentinel   # restart (not just start) so upgrades take effect

ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
echo
echo "Sentinel VMS is running at http://${ip:-this-machine}:8090"
echo "First install? Open it to create the administrator account."
echo "Firewall: allow TCP 8090, plus UDP+TCP 18189 for low-latency (WebRTC) live view."
echo "iPhone: install Sentinel VMS from the App Store, then pair it from the dashboard's iPhone page."
echo "Logs: journalctl -u sentinel -f"
