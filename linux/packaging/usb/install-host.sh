#!/usr/bin/env bash
# Configure the existing recording host; never restart Docker, WSL or the VMS.
set -euo pipefail
container=""
web_uid=""
listen_address=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --container) container="${2:?Missing container name}"; shift 2 ;;
        --web-uid) web_uid="${2:?Missing web UID}"; shift 2 ;;
        --listen-address) listen_address="${2:?Missing private listen address}"; shift 2 ;;
        --help) echo 'Usage: sudo bash install-host.sh [--container NAME] [--web-uid UID] [--listen-address PRIVATE_IP]'; exit 0 ;;
        *) echo 'Unknown host installer option.' >&2; exit 2 ;;
    esac
done
[[ $(id -u) == 0 ]] || { echo 'Run with sudo or through Install-SentinelUsbHost.ps1.' >&2; exit 1; }
[[ -d /run/systemd/system ]] || { echo 'systemd must already be running. No host restart attempted.' >&2; exit 1; }
if [[ -n "$container" ]]; then
    [[ "$container" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || { echo 'Invalid container name.' >&2; exit 2; }
    # A local Unix socket is required: a remote daemon has a different host filesystem.
    if [[ -n "${DOCKER_CONTEXT:-}" ]]; then
        endpoint="$(docker context inspect "$DOCKER_CONTEXT" --format '{{.Endpoints.docker.Host}}')"
    else
        endpoint="${DOCKER_HOST:-$(docker context inspect --format '{{.Endpoints.docker.Host}}')}"
    fi
    [[ "$endpoint" == unix://* ]] || { echo 'Use a Docker daemon on this Linux host.' >&2; exit 1; }
    [[ "$(docker inspect --format '{{.State.Running}}' "$container")" == true ]] || { echo 'The selected VMS container must be running.' >&2; exit 1; }
    mounts="$(docker inspect --format '{{range .Mounts}}{{println .Source .Destination}}{{end}}' "$container")"
    grep -Fxq '/run/sentinel-usb-control /run/sentinel-usb-control' <<< "$mounts" || {
        echo 'Add the /run/sentinel-usb-control directory bind mount to the VMS first; see README.md.' >&2; exit 1;
    }
    if [[ -z "$web_uid" ]]; then
        security="$(docker info --format '{{json .SecurityOptions}}')"
        [[ "$security" != *userns* && "$security" != *rootless* ]] || {
            echo 'UID mapping is enabled. Supply the corresponding host UID with --web-uid.' >&2; exit 1;
        }
        web_user="$(docker inspect --format '{{.Config.User}}' "$container")"
        [[ "$web_user" =~ ^[0-9]+(:[0-9]+)?$ ]] || {
            echo 'Container user is not an explicit numeric UID. Supply --web-uid after checking the VMS process.' >&2; exit 1;
        }
        web_uid="${web_user%%:*}"
    fi
elif [[ -z "$web_uid" ]]; then
    web_uid="$(id -u sentinel 2>/dev/null)" || {
        echo 'Install the native VMS first, or select its Docker container with --container.' >&2; exit 1;
    }
fi
[[ "$web_uid" =~ ^[0-9]+$ ]] || { echo 'Invalid web UID.' >&2; exit 2; }
if [[ -z "$listen_address" ]]; then
    # Route lookup does not send packets. This address must be reachable from Windows/container.
    listen_address="$(ip -4 route get 1.1.1.1 | awk '{for(i=1;i<=NF;i++) if($i=="src") {print $(i+1);exit}}')"
    [[ -n "$listen_address" ]] || { echo 'Supply --listen-address with a private host IP.' >&2; exit 1; }
fi
here="$(cd "$(dirname "$0")" && pwd)"
bash "$here/install.sh" --web-uid "$web_uid" --listen-address "$listen_address" --skip-windows-helper
for attempt in {1..30}; do
    if curl --fail --silent --max-time 3 --unix-socket /run/sentinel-usb-control/control.sock http://localhost/inventory >/dev/null; then break; fi
    sleep 1
done
curl --fail --silent --max-time 5 --unix-socket /run/sentinel-usb-control/control.sock http://localhost/inventory >/dev/null
printf 'Linux USB broker ready (web UID %s, private stream address %s).\n' "$web_uid" "$listen_address"
