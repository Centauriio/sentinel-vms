# Sentinel VMS for Linux

A headless video management server with a web dashboard. Same recording
layout, roles and audit model as the Mac app; the iPhone app will pair to it
the same way (milestone M4). See [ROADMAP.md](ROADMAP.md).

## Install (systemd)

```sh
tar xzf sentinel-<version>-linux-amd64.tar.gz   # or -arm64 for Raspberry Pi 4/5, ARM servers
cd sentinel-<version>-linux-amd64
sudo ./install.sh
```

Then open `http://<server-ip>:8090` and create the administrator account.

- Service: `systemctl status sentinel`, logs: `journalctl -u sentinel -f`
- Data (database, install key, recordings): `/var/lib/sentinel`
- Runs as the unprivileged `sentinel` user with a hardened systemd sandbox.
- Firewall: open TCP 8090 (dashboard) and, for low-latency live view,
  **UDP + TCP 18189** (WebRTC media), e.g.
  `sudo ufw allow 8090/tcp && sudo ufw allow 18189`. Without 18189 the
  dashboard still works; live view falls back to HLS (a few seconds behind).

## Optional USB cameras

Install the capture add-on with `sudo ./install.sh --with-usb`, or add it to an
existing host using `sudo bash linux/packaging/usb/install.sh` from this repository.
It checks and installs missing prerequisites on Debian/Ubuntu, provides independent
camera start/stop controls, and supplies an authenticated RTSP source for Sentinel.
WSL helpers support USB/IP provider detection and Windows camera capture while
preserving existing applications such as StarDesk. See the
[USB add-on guide](packaging/usb/README.md) for device selection, Windows scripts,
Docker networking, and supported platforms. Camera selection, quality and capture
start/stop are in the Cameras web page; installation alone does not start capture.

## Live view: WebRTC and HLS

The Live page plays cameras over **WebRTC** (about half a second behind real
time) and falls back to **HLS** (several seconds behind) automatically: when
WebRTC can't connect within a few seconds, or the browser can't decode the
camera's codec over WebRTC (common with H.265 cameras outside Safari). Each
viewer can pin *Auto / WebRTC / HLS* on the Live page (remembered per
browser). Grid tiles use the camera's sub-stream when it has one.

- Signaling goes through Sentinel's authenticated proxy (`/webrtc/<camera>/whep`)
  like HLS. Media goes straight from MediaMTX to the browser on port **18189**,
  UDP first, TCP when UDP is blocked.
- No STUN/TURN servers are used, so nothing leaves your network; it's meant
  for the LAN or a VPN. (Across the internet, use HLS or the iPhone app.)
- `-webrtc-port N` / `SENTINEL_WEBRTC_PORT=N` changes the port (it is 18189,
  not MediaMTX's 8189, so it can't clash with the Mac app); `0` turns WebRTC
  off.
- The server advertises its own interface addresses (and the address you
  opened the dashboard with, if that's an IP). If browsers reach it at some
  other address (Docker bridge network, port forwarding, VPN), add that under
  **System → Live view → Extra server addresses**, or with
  `-webrtc-hosts 192.168.1.20,nvr.lan` / `SENTINEL_WEBRTC_HOSTS`.

## Updates

Once a day the server asks `https://dl.sentvms.com/latest` for the newest
version (the request carries only `User-Agent: SentinelLinux/<version>`).
Admins see a banner in the dashboard when one is out; **System → Updates** has
the upgrade commands, a *Check now* button and the on/off switch. To upgrade,
unpack the new release and run `sudo ./install.sh` again; data is kept.
`SENTINEL_UPDATE_URL` points the check somewhere else (testing, mirrors).

## Alarm rules and iPhone push

Each camera's alarm setting decides *whether* an alarm is raised. **Alarm
Rules** decide what happens next: matching rules add operator instructions to
the alarm (dashboard and iPhone app) and, when set to *Dashboard + iPhone
push*, notify every paired iPhone. Rules filter by camera, alarm kind, minimum
severity, hours (server time; 22:00–06:00 runs overnight) and days. With no
rules, alarms behave exactly as before and nothing is pushed.

Push delivery (Alarm Rules → iPhone push notifications, Admins only):

- **Sentinel push relay** — for the App Store app. The server registers once
  with `https://dl.sentvms.com/push/v1` and may only push to iPhones paired
  with it. `SENTINEL_PUSH_RELAY_URL` overrides the relay address.
- **Direct to Apple** — paste your own `AuthKey_<KEYID>.p8`, key ID, team ID
  and bundle ID if you build Sentinel Mobile yourself.
- **Sentinel Cloud** — when this server is linked to Sentinel Cloud with push
  in its plan (see below). Linking switches to it unless you send direct.

Optional quiet hours let only Critical alarms through. A burst from one camera
pushes at most once a minute; tokens Apple reports as dead are dropped.

## Sentinel Cloud

Sentinel Cloud is an optional hosted service from Handoffgrid. A Management
Server linked to a subscription gets:

- **Remote access** at a stable `https://<id>-cloud.sentvms.com` address, with
  nothing to install and no port forwarding.
- **Push notifications** to the App Store Sentinel Mobile app through the
  Sentinel push relay.
- **Offsite backup** of each new alarm (its details and snapshot) and of
  locked evidence clips up to 100 MB, viewable from the iPhone even when the
  server is off.

Nothing else depends on it: cameras, detectors, clients and use on your own
network stay free and unlimited. To link, open **Sentinel Admin → Basics →
Sentinel Cloud**, choose *Link to Sentinel Cloud* and enter the code it shows
at sentvms.com with the account that has the subscription. Unlink on the same
page.

The server opens one outbound WebSocket to Sentinel Cloud; no inbound port is
needed. Requests from outside reach a loopback-only listener that serves the
client API v2, the video gateway, the iPhone API, share links and `/health`,
never the dashboard or `/api/v1`. The server token is stored encrypted with
the install key.

Only Management Servers link (`--role all` or `management`); Recording Servers
never contact Sentinel Cloud. For development against another control plane,
`SENTINEL_CLOUD_URL` replaces `https://cloud.sentvms.com`, and
`SENTINEL_CLOUD_INSECURE=1` allows `http://` and `ws://` addresses.

## Run without installing

```sh
./sentinel -data ~/sentinel-data -listen :8090
```

`mediamtx` must sit next to `sentinel`, in `<data>/bin/`, or on `PATH`.

## Docker

Multi-arch image (amd64, arm64) with everything bundled: MediaMTX, ffmpeg,
cloudflared, ONNX Runtime and the YOLOX model.

```sh
mkdir sentinel && cd sentinel
curl -fsSLO https://raw.githubusercontent.com/Hackgiz/sentinel-vms/main/linux/packaging/docker-compose.yml
# edit TZ (and the network/volume options) in docker-compose.yml, then:
docker compose up -d
```

Open `http://<server-ip>:8090` and create the administrator account.
`docker-compose.yml` is also in each release archive and in `dist/docker/`.

- **Networking.** The compose file uses `network_mode: host` (recommended on
  Linux). ONVIF camera discovery is WS-Discovery UDP multicast, which does not
  pass through Docker's default bridge network, and in host mode the iPhone
  pairing QR code carries the host's real LAN address. The trade-off is no
  network isolation: port 8090 and MediaMTX's internal loopback ports
  (18554/18888/19997) are used on the host directly. On a bridge network
  instead, publish `8090:8090`, set `SENTINEL_LAN_URL=http://<host-ip>:8090`
  for iPhone pairing, and add cameras by IP or with the subnet sweep.
- **WebRTC live view** needs port 18189 (UDP and TCP). Host networking just
  works. On a bridge network, publish it 1:1 (`18189:18189/udp` and
  `18189:18189/tcp`) and set `SENTINEL_WEBRTC_HOSTS=<host LAN IP>`; without
  them live view falls back to HLS.
- **Data** (database, `install.key`, recordings) is in `/data` — a named volume
  by default. For a bind mount, the directory must be writable by uid/gid
  65532 (the image's non-root user): `sudo chown 65532:65532 /srv/sentinel`.
- **Time zone:** set `TZ` (e.g. `America/New_York`); the default is UTC.
- **Health:** the image has a `HEALTHCHECK` (`sentinel -healthcheck` probes
  `/api/v1/status`), so `docker ps` shows `healthy`.
- **Hardware:** the image has no VA drivers yet, so GPU decode falls back to
  the CPU in Docker (see [Hardware acceleration](#hardware-acceleration)). The
  compose file has commented `/dev/dri`, Coral and Hailo entries for later.
- **Upgrade:** `docker compose pull && docker compose up -d` (data is kept).

Plain `docker run`:

```sh
docker run -d --name sentinel --network host -e TZ=Etc/UTC \
  -v sentinel-data:/data --restart unless-stopped ghcr.io/hackgiz/sentinel-vms:latest
```

Build the image yourself from a release build:

```sh
VERSION=1.0.0 scripts/build.sh
docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=1.0.0 -t sentinel dist/docker
```

Tagged releases (`v*`) are built and pushed to GHCR by
`.github/workflows/docker.yml` in the public repository.

## Home Assistant (MQTT)

**System → MQTT & Home Assistant**: turn it on, enter your broker (e.g. the
Mosquitto add-on: host `homeassistant.local`, port 1883, an MQTT user), press
*Test connection*, then *Save*. With the MQTT integration set up in Home
Assistant, every camera appears as a device on its own (MQTT discovery under
`homeassistant/…`, retained), with:

- binary sensors **Motion**, **Person**, **Vehicle** (turn off 30 s after the
  last detection) and **Online**
- sensor **Last event** (kind; all event fields as attributes) and image
  **Last event snapshot** (the JPEG of the newest event)
- optional switches **Recording** and **Detection** — only when *Allow Home
  Assistant to switch recording and detection* is on (off by default: anyone
  who can publish to your broker could stop recording). Commands go through
  the same validation as the dashboard and are written to the audit log as
  "Home Assistant (MQTT)".

Topics (prefix `sentinel` by default; `<camera>` is the camera ID):

| Topic | Payload |
|---|---|
| `sentinel/available` | `online` / `offline` (retained; `offline` is the last will) |
| `sentinel/<camera>/motion`, `/person`, `/vehicle`, `/audio`, `/online` | `ON` / `OFF` (retained; `audio` follows audio detection events, OFF 30 s after the last) |
| `sentinel/<camera>/last_event` | JSON of the newest event (retained) |
| `sentinel/<camera>/snapshot` | JPEG of the newest event (retained) |
| `sentinel/<camera>/recording/state`, `/detection/state` | `ON` / `OFF` (retained); `…/set` to switch |
| `sentinel/events` | JSON per event: `type` (`new`, then `update` once the snapshot / AI description is in), `id`, `cameraId`, `camera`, `kind`, `label`, `score`, `time`, `timeISO`, `alarmId`, `description`, `threat`, `snapshot` (dashboard path) |
| `sentinel/alarms` | JSON when an alarm is raised (`type: new`) or changes state (`type: state`) |

Example automation (notify with the snapshot when a person is seen):

```yaml
automation:
  - alias: Person at the front door
    trigger:
      - platform: state
        entity_id: binary_sensor.front_door_person
        to: "on"
    action:
      - service: notify.mobile_app_my_phone
        data:
          message: "{{ state_attr('sensor.front_door_last_event', 'description') or 'Person detected' }}"
          data:
            image: /api/image_proxy/image.front_door_last_event_snapshot
```

Or react to the raw event stream:

```yaml
trigger:
  - platform: mqtt
    topic: sentinel/events
    value_template: "{{ value_json.kind }}"
    payload: Vehicle
```

The broker password is encrypted with the install key and never sent back to
the browser; settings changes and connection tests are audited. A slow or
offline broker never holds up detection: messages wait in a bounded queue
(oldest dropped first) and Sentinel reconnects with backoff.

## Hardware acceleration

Motion and person/vehicle detection decode every camera's stream (the
low-resolution sub-stream when the camera has one). On a small box such as an
Intel N100 mini-PC, moving that decode to the GPU is what lets it handle more
cameras. **System → Hardware acceleration** shows what was found, what each
camera is using (VAAPI / Quick Sync / CPU) and the average detection time per
frame; the *Auto / Off* switch (admins, audited) applies to decode immediately
and to the detection engine after a restart.

**Intel (N100, Core) and AMD — VAAPI decode.** The bundled ffmpeg (amd64 and
arm64) has VAAPI built in, and the amd64 one Quick Sync too; they load the
host's libva and VA driver at runtime, so install them:

```sh
sudo apt install intel-media-va-driver   # Intel Broadwell and newer (iHD); AMD: mesa-va-drivers
sudo apt install vainfo && vainfo        # optional check: should list H264/HEVC VLD profiles
```

Quick Sync is only used when VAAPI can't be (VAAPI drives the same Intel
decoder) and also needs the oneVPL GPU runtime (`libmfx-gen1.2`).

The GPU's render node (`/dev/dri/renderD128`) belongs to the `render` group.
`install.sh` adds the `sentinel` user to `render` and `video`, and the systemd
unit allows DRM devices (`DeviceAllow=char-drm rw`, device policy closed
otherwise). After installing drivers, `sudo systemctl restart sentinel`;
`journalctl -u sentinel | grep -i "hardware decode"` shows the result.

How it works: at start Sentinel lists ffmpeg's hwaccels, picks the first
Intel/AMD render node and opens it once (`-init_hw_device
vaapi=va:/dev/dri/renderD128,connection_type=drm`). The GPU only decodes:
frames are copied back to system memory and the same filters as before
produce the detection frames, so a GPU that can't handle a stream simply
makes ffmpeg decode it in software. If hardware decode fails for a camera
(device error, no frame within 20 s, or the GPU rejects the stream) that
camera switches to CPU decode for an hour, then hardware is tried again.

Environment overrides (systemd: `sudo systemctl edit sentinel`, then
`[Service]` / `Environment=NAME=value`):

| Variable | Use |
|---|---|
| `SENTINEL_FFMPEG` | Use another ffmpeg (e.g. `/usr/bin/ffmpeg`) wherever Sentinel runs ffmpeg |
| `SENTINEL_HWACCEL_DEVICE` | Render node to decode on (multi-GPU boxes; default: first Intel/AMD node) |
| `SENTINEL_ORT_LIB` | ONNX Runtime library to use instead of the bundled CPU-only one |
| `SENTINEL_OPENVINO_DEVICE` | OpenVINO device: `GPU` (default), `NPU`, `CPU`, `AUTO:GPU,CPU` |

**Detection on an Intel GPU/NPU (OpenVINO).** The bundled ONNX Runtime is
Microsoft's CPU-only build. Point `SENTINEL_ORT_LIB` at an ONNX Runtime (1.17
or newer) built with the OpenVINO execution provider — `libonnxruntime.so` with
`libonnxruntime_providers_shared.so` and `libonnxruntime_providers_openvino.so`
beside it, and the OpenVINO runtime on the library path (e.g. built with
`./build.sh --use_openvino GPU --build_shared_lib`). With *Auto*, Sentinel
appends the OpenVINO provider, runs a test inference, and uses it only if that
works; otherwise it stays on the CPU and the System page says why. Compiled
models are cached in `<data>/cache/openvino`.

**Raspberry Pi 5.** The Pi 5 has no H.264 hardware decoder, and its HEVC
decoder needs Raspberry Pi OS's own ffmpeg fork (V4L2 request API), which
Sentinel doesn't use yet — detection decodes on the CPU. Give each camera a
low-resolution sub-stream (e.g. 640×360) so that stays cheap.

**Coral / Hailo.** Detected and shown on the System page, but not used yet:
both need their own model formats and runtimes (see ROADMAP.md).

**Docker.** The current image (distroless) contains no libva or VA drivers,
so hardware decode doesn't work in it even with `/dev/dri` passed through
(the compose file has commented `devices: /dev/dri` and `group_add` entries
for when it does); detection falls back to the CPU automatically. Use the
systemd install for GPU decode for now.

## Security model

- Only port 8090 is exposed for HTTP. MediaMTX's HTTP, RTSP and API
  listeners are on 127.0.0.1; HLS and WebRTC signaling go through Sentinel's
  authenticated proxy (session cookie + live-view permission, camera ID
  checked, same-origin only, WebRTC session creation rate-limited per user).
  The one exception is WebRTC media on port 18189 (UDP/TCP): it only carries
  encrypted (DTLS-SRTP) streams for sessions Sentinel signed in, using
  per-session ICE credentials.
- Passwords: bcrypt. Sessions: random tokens, stored hashed, HttpOnly +
  SameSite=Strict cookies. Sign-in lockout after 5 failures (per user and IP).
- Camera passwords are encrypted at rest (AES-256-GCM) with a per-install key
  (`install.key`, mode 0600). Back it up together with `sentinel.db`.
- Audit log entries are SHA-256 hash-chained; *Audit Log → Verify chain*
  detects edits or deletions made outside Sentinel.
- For access from outside your network, use remote access: Sentinel Cloud
  (above) or, without a subscription, a temporary `trycloudflare.com` address
  through `cloudflared` when it is installed. Both reach only the loopback
  listener described under Sentinel Cloud, never the dashboard or `/api/v1`.
  Don't port-forward plain HTTP.

## Develop

```sh
export PATH=$HOME/.local/go/bin:$PATH
go test ./...
go run ./cmd/sentinel -listen 127.0.0.1:8091 -data /tmp/sentinel-dev   # API + embedded dashboard
(cd web && npm run dev)                                                 # hot-reload UI on :5173
VERSION=0.1.0 scripts/build.sh                                         # release archives
```
