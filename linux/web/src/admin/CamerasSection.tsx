import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ApiError as V1ApiError } from "../api";
import ScanDialog, { profileLabel, type ScanBackend, type ScanPick } from "../pages/ScanDialog";
import type { AlertOn, ApiClient, Camera, CameraGroup, CameraPatch, RecordingServer, StreamsDiscoverRequest } from "../shared/api";
import { useLoad } from "../shared/hooks";
import { Icon } from "../shared/Icons";
import {
  CLOUD_URL,
  NEEDS_CLOUD,
  isPhone,
  needsCloud,
  phoneCameraApi,
  phoneSetupSteps,
  phoneStatus,
  phoneStatusLine,
  type CloudAvailability,
  type SourcedCamera,
} from "../shared/phoneCameras";
import { cloudApi } from "./cloud";
import {
  effectiveModeOf,
  lowResDaysOf,
  modeBadge,
  recordingApi,
  recordingFormOf,
  recordingInput,
  recordingPatch,
  type RecCamera,
  type RecCameraInput,
  type RecordingForm,
  type RecordingSettings,
} from "../shared/recordingModes";
import { AUDIO_DEFAULT_DB, AUDIO_MAX_DB, AUDIO_MIN_DB, LOITER_MAX_S, audioFormOf, audioPatch, clampAudioDb, clampLoiter, loiterHint, loiterOf, loiterPatch } from "./audioSettings";
import { v2 } from "./client";
import RecordingFields from "./RecordingFields";
import UsbCameraDialog from "./UsbCameraDialog";
import { usbApi, usbStateLabel, usbShouldStop, type UsbCamera } from "../shared/usbCameras";
import { StreamPickerView, useFindStreams } from "./StreamPicker";
import StreamSettingsDialog from "./StreamSettingsDialog";
import { ChipPicker, Confirm, Empty, ErrorBanner, Loading, Modal, OnlineBadge, useLiveMessages, useLiveReload } from "./ui";

/** A camera picked from a scan, bound for the server that found it. */
interface ScannedCamera {
  pick: ScanPick;
  recorderId: string;
}

/** The scan dialog's calls, routed to one recording server over api/v2. */
function scanBackend(serverId: string): ScanBackend {
  return {
    discover: async (sweep) => {
      const r = await v2.servers.discover(serverId, sweep);
      return {
        devices: (r.devices ?? []).map((d) => ({ ...d, manufacturer: d.manufacturer ?? "", model: d.model ?? "", serviceURL: d.serviceURL ?? "" })),
        subnets: r.subnets ?? [],
        warnings: r.warnings ?? [],
      };
    },
    probe: async (host, serviceURL, username, password) => {
      const r = await v2.servers.probe(serverId, { host, serviceURL, username, password });
      if (r.error || !r.details) {
        throw new V1ApiError(r.error || "The camera couldn't be probed.", 422, { authFailed: !!r.authFailed, suggestedURL: r.suggestedURL });
      }
      return { details: r.details, mainURL: r.mainURL, subURL: r.subURL };
    },
  };
}

const alertChoices: { value: AlertOn; label: string }[] = [
  { value: "auto", label: "Automatic (people & vehicles when detection is installed)" },
  { value: "peopleAndVehicles", label: "People & vehicles" },
  { value: "people", label: "People only" },
  { value: "motion", label: "Any motion" },
];

/** `openId`: a deep link (#/admin/devices/cameras?camera=<id>, from Insights) opens that camera's settings. */
export default function CamerasSection({ openId }: { openId?: string }) {
  const usb = useLoad(() => usbApi.list(v2), []);
  const [usbEditing, setUsbEditing] = useState<UsbCamera | "new" | null>(null);
  const [usbBusy, setUsbBusy] = useState("");
  const [usbError, setUsbError] = useState("");
  const usbByID = new Map((usb.data?.cameras ?? []).map(c => [c.cameraId, c]));
  useEffect(() => { const timer = window.setInterval(() => { void usb.reload(); }, 5000); return () => window.clearInterval(timer); }, [usb.reload]);
  const controlUSB = async (camera: UsbCamera) => {
    setUsbBusy(camera.cameraId); setUsbError("");
    try { await usbApi.control(v2, camera.cameraId, usbShouldStop(camera) ? "stop" : "start"); await usb.reload(); }
    catch (e) { setUsbError(e instanceof Error ? e.message : "Camera operation failed."); }
    finally { setUsbBusy(""); }
  };
  const cameras = useLoad(() => v2.cameras.list(), []);
  const servers = useLoad(() => v2.servers.list(), []);
  const groups = useLoad(() => v2.groups.list(), []);
  // WAVE22(C): server recording defaults (badge and editor), and each camera's live ingest rate for the GB/day line.
  const recDefaults = useLoad(() => recordingApi.get(v2).catch(() => null), []);
  // WAVE23(J) Addendum 2: whether this site's Cloud plan lets an iPhone stream (unknown when not readable).
  const cloud = useLoad(() => (cloudApi.get() as Promise<CloudAvailability>).catch(() => null), []);
  const ingest = useLoad(async () => {
    const m = new Map<string, number>();
    const servers = await v2.system.health().catch(() => []);
    for (const s of servers) for (const c of s.cameras) if (c.kbps != null) m.set(c.id, c.kbps);
    return m;
  }, []);
  const [editing, setEditing] = useState<Camera | "new" | ScannedCamera | null>(null);
  const [moving, setMoving] = useState<Camera | null>(null);
  const [deleting, setDeleting] = useState<Camera | null>(null);
  const [streamCam, setStreamCam] = useState<Camera | null>(null);
  const [scanServer, setScanServer] = useState<string | null>(null);
  const [serverFilter, setServerFilter] = useState("");
  const [query, setQuery] = useState("");

  // WAVE14(C): open the linked camera once both lists are in, then drop the parameter so a reload doesn't reopen it.
  const linked = useRef("");
  useEffect(() => {
    const c = openId && servers.data ? cameras.data?.find((x) => x.id === openId) : undefined;
    if (!c || linked.current === c.id) return;
    linked.current = c.id;
    setEditing(c);
    history.replaceState(null, "", window.location.hash.split("?")[0]);
  }, [openId, cameras.data, servers.data]);

  useLiveReload(cameras.reload, ["config.changed"]);
  useLiveReload(servers.reload, ["recorder.status"]);
  useLiveMessages((m) => {
    if (m.type !== "camera.status") return;
    cameras.setData((list) => list?.map((c) => (c.id === m.data.cameraId ? { ...c, status: { ...c.status, online: m.data.online } } : c)) ?? list);
  });

  const serverName = useMemo(() => new Map((servers.data ?? []).map((s) => [s.id, s.name])), [servers.data]);
  const groupName = useMemo(() => new Map((groups.data ?? []).map((g) => [g.id, g.name])), [groups.data]);
  const all = cameras.data ?? [];
  const shown = all.filter(
    (c) =>
      (!serverFilter || c.recorderId === serverFilter) &&
      (!query || `${c.name} ${c.location}`.toLowerCase().includes(query.toLowerCase())),
  );
  const reloadAll = () => {
    void cameras.reload();
    void groups.reload();
    void usb.reload();
  };

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Cameras</h1>
          <div className="sub">
            {cameras.data ? `${all.length} on ${new Set(all.map((c) => c.recorderId)).size} server${new Set(all.map((c) => c.recorderId)).size === 1 ? "" : "s"}` : "Every camera on the site"}
          </div>
        </div>
        <input type="text" placeholder="Filter…" aria-label="Filter cameras" value={query} onChange={(e) => setQuery(e.target.value)} className="head-input" />
        <select aria-label="Recording server" value={serverFilter} onChange={(e) => setServerFilter(e.target.value)} className="head-input">
          <option value="">All servers</option>
          {(servers.data ?? []).map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </select>
        <button
          className="btn"
          onClick={() => {
            const list = servers.data ?? [];
            const def = list.find((x) => x.local && !x.revoked && x.online) ?? list.find((x) => !x.revoked && x.online);
            if (def) setScanServer(def.id);
          }}
          disabled={!(servers.data ?? []).some((x) => !x.revoked && x.online)}
        >
          Scan network
        </button>
        <button className="btn" onClick={() => setUsbEditing("new")}>+ Add USB camera</button>
        <button className="btn primary" onClick={() => setEditing("new")} disabled={!servers.data}>
          + Add camera
        </button>
      </div>
      {usbEditing && <UsbCameraDialog inventory={usb.data} camera={usbEditing === "new" ? undefined : usbEditing} onRefresh={() => { void usb.reload(); }} onClose={() => setUsbEditing(null)} onSaved={() => { setUsbEditing(null); reloadAll(); }} />}
      <ErrorBanner error={usbError || usb.error} onRetry={() => { setUsbError(""); void usb.reload(); }} />
      <ErrorBanner error={cameras.error || servers.error} onRetry={() => (cameras.reload(), servers.reload())} />
      <div className="card table-wrap">
        {cameras.data === null ? (
          cameras.error ? null : <Loading />
        ) : all.length === 0 ? (
          <Empty icon="◉" title="No cameras yet">
            Add one with its RTSP address, e.g. rtsp://192.168.1.20:554/stream1, or use an iPhone as a camera.
          </Empty>
        ) : shown.length === 0 ? (
          <Empty icon="◉" title="No matching cameras" />
        ) : (
          <table className="list">
            <thead>
              <tr>
                <th>Name</th>
                <th>Server</th>
                <th>Groups</th>
                <th>Status</th>
                <th>Recording</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {shown.map((c) => (
                <tr key={c.id}>
                  <td>
                    <strong>{c.name}</strong>
                    {isPhone(c) && <PhoneBadge />}
                    {usbByID.has(c.id) && <span className="badge blue">USB</span>}
                    {c.location && <div className="mono">{c.location}</div>}
                  </td>
                  <td>{serverName.get(c.recorderId) ?? <span className="mono">{c.recorderId || "—"}</span>}</td>
                  <td>
                    {c.groupIds.length === 0 ? (
                      <span className="muted small">None</span>
                    ) : (
                      <div className="chip-grid">
                        {c.groupIds.map((g) => (
                          <span key={g} className="badge blue">
                            {groupName.get(g) ?? "Removed group"}
                          </span>
                        ))}
                      </div>
                    )}
                  </td>
                  <td>
                    <StatusCell camera={c} />
                    {usbByID.has(c.id) && <div className="small" role="status">{usbStateLabel(usbByID.get(c.id)!.state)}</div>}
                  </td>
                  <td>
                    <RecordingCell camera={c} server={recDefaults.data ?? null} />
                  </td>
                  <td className="row-actions">
                    <button className="btn small" onClick={() => setEditing(c)}>
                      Edit
                    </button>
                    <button className="btn small" onClick={() => setMoving(c)} disabled={usbByID.has(c.id) || (servers.data ?? []).filter((s) => !s.revoked).length < 2}>
                      Move
                    </button>
                    {usbByID.has(c.id) && <>
                      <button className="btn small" onClick={() => setUsbEditing(usbByID.get(c.id)!)}>USB quality…</button>
                      <button className="btn small" disabled={!!usbBusy || !usb.data?.available} onClick={() => { void controlUSB(usbByID.get(c.id)!); }}>{usbBusy === c.id ? "Working…" : usbShouldStop(usbByID.get(c.id)!) ? "Stop capture" : "Start capture"}</button>
                    </>}
                    {!isPhone(c) && !usbByID.has(c.id) && (
                      <button className="btn small" onClick={() => setStreamCam(c)} title={c.onvifUrl ? undefined : "Added without ONVIF"} disabled={!c.onvifUrl}>
                        Stream settings…
                      </button>
                    )}
                    <button className="btn small danger" onClick={() => setDeleting(c)}>
                      Delete…
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {scanServer !== null && servers.data && (
        <ScanDialog
          backend={scanBackend(scanServer)}
          rescanKey={scanServer}
          header={
            <label className="field">
              <span>Scan from</span>
              <select value={scanServer} onChange={(e) => setScanServer(e.target.value)}>
                {servers.data
                  .filter((x) => !x.revoked)
                  .map((x) => (
                    <option key={x.id} value={x.id} disabled={!x.online}>
                      {x.name}
                      {x.online ? "" : " (offline)"}
                    </option>
                  ))}
              </select>
              <small>The scan runs on that server's own network — cameras live on the recording server's LAN.</small>
            </label>
          }
          onClose={() => setScanServer(null)}
          onPick={(p) => {
            const recorderId = scanServer;
            setScanServer(null);
            setEditing({ pick: p, recorderId });
          }}
        />
      )}
      {editing && servers.data && (
        <CameraEditor
          camera={editing === "new" || "pick" in editing ? null : editing}
          scanned={editing !== "new" && "pick" in editing ? editing : undefined}
          servers={servers.data}
          groups={groups.data ?? []}
          recDefaults={recDefaults.data ?? null}
          ingest={ingest.data}
          onClose={() => setEditing(null)}
          onKeyChanged={reloadAll}
          usbManaged={editing !== "new" && !("pick" in editing) && usbByID.has(editing.id)}
          phoneAvailable={cloud.data?.phoneCamerasAvailable}
          onSaved={() => {
            setEditing(null);
            reloadAll();
          }}
        />
      )}
      {moving && servers.data && (
        <MoveDialog
          camera={moving}
          servers={servers.data}
          onClose={() => setMoving(null)}
          onMoved={() => {
            setMoving(null);
            reloadAll();
          }}
        />
      )}
      {streamCam && <StreamSettingsDialog camera={streamCam} onClose={() => setStreamCam(null)} />}
      {deleting && (
        <Confirm
          title={`Delete ${deleting.name}?`}
          confirmLabel="Delete camera"
          onCancel={() => setDeleting(null)}
          run={async () => {
            await v2.cameras.remove(deleting.id);
            setDeleting(null);
            reloadAll();
          }}
        >
          <p>The camera stops recording and disappears from every view, group and rule.</p>
          <p className="muted small">Recordings already made are kept on the recording server until retention removes them.</p>
        </Confirm>
      )}
    </>
  );
}

/** WAVE23(J): the small "iPhone" tag next to a phone camera's name. */
export function PhoneBadge() {
  return (
    <span className="badge blue phone-badge" title="iPhone camera">
      <Icon name="phone" size={12} /> iPhone
    </span>
  );
}

/** The list's Status column. Phone cameras: "Streaming" / "Not streaming" and when the phone was last seen. */
export function StatusCell({ camera: c, now }: { camera: SourcedCamera; now?: number }) {
  if (isPhone(c)) {
    const s = phoneStatus(c, now);
    return (
      <>
        <OnlineBadge online={s.streaming} on={s.state} off={s.state} />
        {s.detail && <div className="mono">{s.detail}</div>}
      </>
    );
  }
  return (
    <>
      <OnlineBadge online={c.status.online} on="Online" off={c.status.message || "Offline"} />
      {c.status.analytics && <div className="mono">Analytics: {c.status.analytics}</div>}
    </>
  );
}

/** WAVE22(C): the list's Recording column: the mode badge and how long footage is kept. */
export function RecordingCell({ camera, server }: { camera: RecCamera; server: RecordingSettings | null }) {
  const mode = effectiveModeOf(camera, server?.defaultMode);
  const b = modeBadge(mode);
  const full = camera.retentionDays > 0 ? `Keep ${camera.retentionDays} d` : "Default retention";
  const low = mode === "dual" ? ` · low ${lowResDaysOf(camera.lowResRetentionDays ?? 0, server, camera.retentionDays || (server?.defaultRetentionDays ?? 7))} d` : "";
  return (
    <>
      <span className={`badge ${b.tone}`}>{b.text}</span>
      {mode !== "off" && <div className="mono">{full + low}</div>}
    </>
  );
}

const moveNote =
  "Footage recorded so far stays on the old server and stays playable: playback finds it there. New recordings go to the new server.";

function MoveDialog({ camera, servers, onClose, onMoved }: { camera: Camera; servers: RecordingServer[]; onClose: () => void; onMoved: () => void }) {
  const choices = servers.filter((s) => !s.revoked && s.id !== camera.recorderId);
  const [to, setTo] = useState(choices[0]?.id ?? "");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const from = servers.find((s) => s.id === camera.recorderId);
  return (
    <Modal
      title={`Move ${camera.name}`}
      onClose={onClose}
      onSubmit={async () => {
        setBusy(true);
        setError(null);
        try {
          await v2.cameras.update(camera.id, { recorderId: to });
          onMoved();
        } catch (e) {
          setError(e);
          setBusy(false);
        }
      }}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy || !to}>
            Move camera
          </button>
        </>
      }
    >
      <ErrorBanner error={error} />
      <p className="muted">Now on {from?.name ?? camera.recorderId}.</p>
      <label className="field">
        <span>Move to</span>
        <select value={to} onChange={(e) => setTo(e.target.value)} autoFocus>
          {choices.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
              {s.online ? "" : " (offline)"}
            </option>
          ))}
        </select>
      </label>
      <div className="notice">{moveNote}</div>
    </Modal>
  );
}

interface Form {
  name: string;
  location: string;
  rtspUrl: string;
  subRtspUrl: string;
  username: string;
  password: string;
  clearPassword: boolean;
  /** WAVE22(C): mode, trigger, pre/post-roll, retentionDays and lowResRetentionDays. */
  rec: RecordingForm;
  motionLevel: number;
  alertOn: AlertOn;
  onvifUrl: string;
  cameraAnalytics: boolean;
  audioDetect: boolean;
  audioThresholdDb: number;
  /** Wave 16: seconds a person track stays before a Loitering event; 0 = off. */
  loiterSeconds: number;
  recorderId: string;
  groupIds: string[];
  sortOrder: number;
}

function formOf(c: RecCamera | null, servers: RecordingServer[], scanned?: ScannedCamera): Form {
  if (c)
    return {
      name: c.name,
      location: c.location,
      rtspUrl: c.rtspUrl,
      subRtspUrl: c.subRtspUrl,
      username: c.username,
      password: "",
      clearPassword: false,
      rec: recordingFormOf(c),
      motionLevel: c.motionLevel,
      alertOn: c.alertOn,
      onvifUrl: c.onvifUrl,
      cameraAnalytics: c.cameraAnalytics,
      ...audioFormOf(c),
      loiterSeconds: loiterOf(c),
      recorderId: c.recorderId,
      groupIds: c.groupIds,
      sortOrder: c.sortOrder,
    };
  const def = servers.find((s) => s.local && !s.revoked) ?? servers.find((s) => !s.revoked);
  const blank: Form = {
    name: "",
    location: "",
    rtspUrl: "",
    subRtspUrl: "",
    username: "",
    password: "",
    clearPassword: false,
    rec: recordingFormOf(null),
    motionLevel: 2,
    alertOn: "auto",
    onvifUrl: "",
    cameraAnalytics: true,
    ...audioFormOf(null),
    loiterSeconds: 0,
    recorderId: def?.id ?? "",
    groupIds: [],
    sortOrder: 0,
  };
  if (!scanned) return blank;
  const i = scanned.pick.initial; // api/v1 field spellings (rtspURL …)
  return {
    ...blank,
    name: i.name,
    location: i.location ?? "",
    rtspUrl: i.rtspURL,
    subRtspUrl: i.subRTSPURL ?? "",
    username: i.username ?? "",
    password: i.password ?? "",
    onvifUrl: i.onvifURL ?? "",
    cameraAnalytics: i.cameraAnalytics !== false,
    recorderId: scanned.recorderId,
  };
}

/** Only what changed (absent fields are kept by the server). */
export function cameraPatch(c: RecCamera, f: Form): CameraPatch {
  const p: CameraPatch = {};
  const keys = ["name", "location", "rtspUrl", "subRtspUrl", "username", "motionLevel", "alertOn", "onvifUrl", "cameraAnalytics", "recorderId", "sortOrder"] as const;
  for (const k of keys) if (f[k] !== c[k]) (p as Record<string, unknown>)[k] = f[k];
  Object.assign(p, recordingPatch(c, f.rec), audioPatch(c, f), loiterPatch(c, f.loiterSeconds));
  if ([...f.groupIds].sort().join() !== [...c.groupIds].sort().join()) p.groupIds = f.groupIds;
  if (f.clearPassword) p.password = "";
  else if (f.password) p.password = f.password;
  return p;
}

export function CameraEditor({
  camera,
  scanned,
  servers,
  groups,
  recDefaults,
  ingest,
  onClose,
  onSaved,
  initialKind = "rtsp",
  usbManaged = false,
  onKeyChanged,
  phoneAvailable,
}: {
  camera: (RecCamera & SourcedCamera) | null;
  scanned?: ScannedCamera;
  servers: RecordingServer[];
  groups: CameraGroup[];
  recDefaults: RecordingSettings | null;
  /** Live ingest kbps by camera id (System Monitor); null when not readable. */
  ingest: Map<string, number> | null;
  onClose: () => void;
  onSaved: () => void;
  /** WAVE23(J): a new camera starts as a network camera; "phone" opens on the iPhone instructions. */
  initialKind?: "rtsp" | "phone";
  usbManaged?: boolean;
  /** GET /api/v2/cloud phoneCamerasAvailable; undefined = unknown (older server, or not readable). */
  phoneAvailable?: boolean;
  /** The phone key was rotated or revoked: reload the list, keep the editor open. */
  onKeyChanged?: () => void;
}) {
  const [f, setF] = useState<Form>(() => formOf(camera, servers, scanned));
  const [kind, setKind] = useState<"rtsp" | "phone">(initialKind);
  const phone = isPhone(camera);
  const profiles = scanned?.pick.profiles ?? [];
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setF((x) => ({ ...x, [k]: v }));
  const analytics = !!f.onvifUrl.trim() && f.cameraAnalytics;
  const moving = camera && f.recorderId !== camera.recorderId;
  const usable = servers.filter((s) => !s.revoked || s.id === f.recorderId);
  // Find streams tries a login typed into the editor before it is saved.
  const login = useCallback((): StreamsDiscoverRequest => {
    const l: StreamsDiscoverRequest = {};
    if (camera && f.username !== camera.username) l.username = f.username;
    if (f.password) l.password = f.password;
    return l;
  }, [camera, f.username, f.password]);
  const finder = useFindStreams(camera?.id, login);

  async function save() {
    setBusy(true);
    setError(null);
    try {
      if (camera) {
        const p = cameraPatch(camera, f);
        if (Object.keys(p).length) await v2.cameras.update(camera.id, p);
      } else {
        const body: RecCameraInput = {
          name: f.name.trim(),
          location: f.location,
          rtspUrl: f.rtspUrl.trim(),
          subRtspUrl: f.subRtspUrl.trim(),
          username: f.username,
          password: f.password || undefined,
          ...recordingInput(f.rec),
          motionLevel: f.motionLevel,
          alertOn: f.alertOn,
          onvifUrl: f.onvifUrl.trim(),
          cameraAnalytics: f.cameraAnalytics,
          audioDetect: f.audioDetect,
          audioThresholdDb: f.audioThresholdDb,
          loiterSeconds: f.loiterSeconds,
          recorderId: f.recorderId || undefined,
          groupIds: f.groupIds,
        };
        await v2.cameras.create(body as never); // WAVE24(D1): rolls may be null until v2.d.ts is regenerated
      }
      onSaved();
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  }

  const kindPicker = !camera && !scanned && <KindPicker kind={kind} onChange={setKind} phoneAvailable={phoneAvailable} />;

  if (!camera && kind === "phone")
    return (
      <Modal
        title="Add camera"
        wide
        onClose={onClose}
        onSubmit={onClose}
        actions={
          <button className="btn primary" autoFocus>
            Done
          </button>
        }
      >
        {kindPicker}
        <PhoneSetup phoneAvailable={phoneAvailable} />
      </Modal>
    );

  return (
    <Modal
      title={camera ? `Edit ${camera.name}` : scanned ? "Add discovered camera" : "Add camera"}
      wide
      onClose={onClose}
      onSubmit={save}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy}>
            {busy ? "Saving…" : camera ? "Save" : "Add camera"}
          </button>
        </>
      }
    >
      <ErrorBanner error={error} />
      {kindPicker}
      {camera && phone && (
        <div className={`notice phone-note${needsCloud(camera) ? " warn" : ""}`}>
          <Icon name="phone" size={14} /> {phoneStatusLine(camera)}
          {needsCloud(camera) && (
            <>
              {" "}
              ·{" "}
              <a href={CLOUD_URL} target="_blank" rel="noreferrer">
                Learn more
              </a>
            </>
          )}
        </div>
      )}
      <div className="grid-2">
        <label className="field">
          <span>Name</span>
          <input type="text" value={f.name} onChange={(e) => set("name", e.target.value)} required autoFocus />
        </label>
        <label className="field">
          <span>Location</span>
          <input type="text" value={f.location} onChange={(e) => set("location", e.target.value)} placeholder="Front door, Warehouse…" />
        </label>
      </div>
      <label className="field">
        <span>Recording server</span>
        <select disabled={usbManaged} value={f.recorderId} onChange={(e) => set("recorderId", e.target.value)} required>
          {!f.recorderId && <option value="">Choose…</option>}
          {usable.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
              {s.revoked ? " (revoked)" : s.online ? "" : " (offline)"}
            </option>
          ))}
        </select>
        {moving ? (
          <small className="move-note">{moveNote}</small>
        ) : (
          <small>{phone ? "The server the iPhone streams to, which records it." : "The server that connects to this camera and records it."}</small>
        )}
      </label>
      {usbManaged && <p>USB connection settings are managed automatically. Use USB quality and Start/Stop capture in the camera list.</p>}
      {!phone && !usbManaged && (
        <>
      {profiles.length > 0 && (
        <div className="notice">
          {profiles.length === 1
            ? "Found the camera's stream and filled it in below."
            : `Found ${profiles.length} streams on this camera. Pick which to record and which to watch live.`}
        </div>
      )}
      {profiles.length > 0 && (
        <label className="field">
          <span>Record this stream</span>
          <select value={f.rtspUrl} onChange={(e) => set("rtspUrl", e.target.value)}>
            {!profiles.some((p) => p.rtspURL === f.rtspUrl) && <option value={f.rtspUrl}>Custom address</option>}
            {profiles.map((p) => (
              <option key={p.token} value={p.rtspURL}>
                {profileLabel(p)}
              </option>
            ))}
          </select>
        </label>
      )}
      {profiles.length > 1 && (
        <label className="field">
          <span>Live view stream</span>
          <select value={f.subRtspUrl} onChange={(e) => set("subRtspUrl", e.target.value)}>
            <option value="">Same as recording</option>
            {profiles
              .filter((p) => p.rtspURL !== f.rtspUrl)
              .map((p) => (
                <option key={p.token} value={p.rtspURL}>
                  {profileLabel(p)}
                  {p.rtspURL === scanned?.pick.recommendedSub ? " (recommended)" : ""}
                </option>
              ))}
          </select>
          <small>A low-res stream for live view saves CPU, but opens a second connection to the camera.</small>
        </label>
      )}
      <label className="field">
        <span>Main stream (RTSP)</span>
        <input type="text" value={f.rtspUrl} onChange={(e) => set("rtspUrl", e.target.value)} placeholder="rtsp://192.168.1.20:554/stream1" required />
        <small>Recorded at full quality. A user name and password typed into the address move into the fields below.</small>
      </label>
      <div className="field sub-field">
        <label className="field-label" htmlFor="cam-sub">
          Second stream (low-res)
        </label>
        <div className="sub-input">
          <input id="cam-sub" type="text" value={f.subRtspUrl} onChange={(e) => set("subRtspUrl", e.target.value)} placeholder="rtsp://192.168.1.20:554/stream2" />
          {camera && (
            <button type="button" className="btn small" disabled={finder.state.kind === "loading"} onClick={() => void finder.find()}>
              Find streams
            </button>
          )}
        </div>
        <small>Used for live view, detection and low-res recording.</small>
        <StreamPickerView
          state={finder.state}
          mainUrl={f.rtspUrl}
          current={f.subRtspUrl.trim()}
          onUse={(u) => set("subRtspUrl", u)}
          onResize={(r) => void finder.resize(r)}
          onRetry={() => void finder.find()}
          onClose={finder.close}
        />
      </div>
      <div className="grid-2">
        <label className="field">
          <span>Username</span>
          <input type="text" value={f.username} onChange={(e) => set("username", e.target.value)} autoComplete="off" />
        </label>
        <label className="field">
          <span>Password</span>
          <input
            type="password"
            value={f.password}
            disabled={f.clearPassword}
            onChange={(e) => set("password", e.target.value)}
            placeholder={camera?.hasPassword ? "•••••• (unchanged)" : ""}
            autoComplete="new-password"
          />
        </label>
      </div>
      {camera?.hasPassword && (
        <label className="check">
          <input type="checkbox" checked={f.clearPassword} onChange={(e) => set("clearPassword", e.target.checked)} />
          Remove the saved password
        </label>
      )}
        </>
      )}
      <div className="field">
        <span className="field-label" id="cam-groups">
          Groups
        </span>
        <ChipPicker label="Groups" items={groups} selected={f.groupIds} onChange={(ids) => set("groupIds", ids)} empty="No groups yet. Create them under Groups." />
      </div>
      <RecordingFields
        form={f.rec}
        onChange={(r) => setF((x) => ({ ...x, rec: { ...x.rec, ...r } }))}
        hasSub={!!f.subRtspUrl.trim()}
        motionLevel={f.motionLevel}
        analytics={analytics}
        server={recDefaults}
        kbps={camera ? ingest?.get(camera.id) : undefined}
        legacy={camera ? camera.recordingMode === undefined : recDefaults === null}
        phone={phone}
        onFindStreams={
          camera && !phone
            ? () => {
                document.getElementById("cam-sub")?.scrollIntoView({ block: "center", behavior: "smooth" });
                void finder.find();
              }
            : undefined
        }
      />
      <div className="grid-2">
        <label className="field">
          <span>Motion detection</span>
          <select value={f.motionLevel} onChange={(e) => set("motionLevel", Number(e.target.value))}>
            <option value={0}>Off</option>
            <option value={1}>Low sensitivity</option>
            <option value={2}>Medium sensitivity</option>
            <option value={3}>High sensitivity</option>
          </select>
        </label>
      </div>
      <label className="field">
        <span>Raise alarms for</span>
        <select value={f.alertOn} onChange={(e) => set("alertOn", e.target.value as AlertOn)} disabled={f.motionLevel === 0 && !analytics}>
          {alertChoices.map((a) => (
            <option key={a.value} value={a.value}>
              {a.label}
            </option>
          ))}
        </select>
        <small>Everything still appears under Events; this only decides what becomes an alarm.</small>
      </label>
      {!phone && (
        <>
          <label className="field">
            <span>ONVIF address (optional)</span>
            <input type="text" value={f.onvifUrl} onChange={(e) => set("onvifUrl", e.target.value)} placeholder="http://192.168.1.20/onvif/device_service" />
          </label>
          <label className="check">
            <input type="checkbox" checked={analytics} disabled={!f.onvifUrl.trim()} onChange={(e) => set("cameraAnalytics", e.target.checked)} />
            Use the camera's built-in analytics (ONVIF Profile M)
          </label>
        </>
      )}
      <label className="check">
        <input type="checkbox" checked={f.audioDetect} onChange={(e) => set("audioDetect", e.target.checked)} />
        {phone ? "Detect loud and distinctive sounds (needs the iPhone's microphone on)" : "Detect loud and distinctive sounds (needs a camera with a microphone)"}
      </label>
      {f.audioDetect && (
        <label className="field">
          <span>Loud noise threshold (dB over background)</span>
          <input
            type="number"
            min={AUDIO_MIN_DB}
            max={AUDIO_MAX_DB}
            value={f.audioThresholdDb}
            onChange={(e) => set("audioThresholdDb", clampAudioDb(Number(e.target.value)))}
          />
          <small>
            Raises an "audio" event when the sound stays this far above the room's background level. Lower is more sensitive; {AUDIO_DEFAULT_DB} suits most rooms.
            {camera?.status.audio ? ` Status: ${camera.status.audio}.` : ""}
          </small>
        </label>
      )}
      <label className="field">
        <span>Loitering after (seconds, 0 = off)</span>
        <input type="number" min={0} max={LOITER_MAX_S} step={5} value={f.loiterSeconds} onChange={(e) => set("loiterSeconds", clampLoiter(Number(e.target.value)))} />
        <small>{loiterHint(f.loiterSeconds)}</small>
      </label>
      {camera && (
        <label className="field">
          <span>Display order</span>
          <input type="number" value={f.sortOrder} onChange={(e) => set("sortOrder", Number(e.target.value) || 0)} />
          <small>Lower numbers come first in camera lists and the live grid.</small>
        </label>
      )}
      {camera && phone && <PushKeyPanel camera={camera} onChanged={onKeyChanged ?? (() => {})} />}
    </Modal>
  );
}

/** WAVE23(J): "Network camera" or "Use an iPhone", at the top of Add camera. */
function KindPicker({ kind, onChange, phoneAvailable }: { kind: "rtsp" | "phone"; onChange: (k: "rtsp" | "phone") => void; phoneAvailable?: boolean }) {
  return (
    <div className="seg kind-picker" role="radiogroup" aria-label="Kind of camera">
      <label className={kind === "rtsp" ? "on" : ""}>
        <input type="radio" name="cameraKind" value="rtsp" checked={kind === "rtsp"} onChange={() => onChange("rtsp")} />
        Network camera
      </label>
      <label className={kind === "phone" ? "on" : ""}>
        <input type="radio" name="cameraKind" value="phone" checked={kind === "phone"} onChange={() => onChange("phone")} />
        <Icon name="phone" size={14} /> Use an iPhone
        {phoneAvailable === false && <span className="kind-note">· {NEEDS_CLOUD}</span>}
      </label>
    </div>
  );
}

/** "Needs Sentinel Cloud. Learn more" (Addendum 2). */
export function NeedsCloud({ more }: { more?: string }) {
  return (
    <>
      {NEEDS_CLOUD}.{more ? ` ${more}` : ""}{" "}
      <a href={CLOUD_URL} target="_blank" rel="noreferrer">
        Learn more
      </a>
    </>
  );
}

/** What to do on the iPhone. Nothing is typed here: the phone adds itself. */
export function PhoneSetup({ phoneAvailable }: { phoneAvailable?: boolean }) {
  return (
    <div className="phone-setup">
      <p>An iPhone can stream its camera to Sentinel, on this network or through Sentinel Cloud. It then works like any camera: live view, recording, detection and rules.</p>
      <div className={`notice${phoneAvailable === false ? " warn" : ""}`}>
        <NeedsCloud more={phoneAvailable === false ? "This site isn't on Sentinel Cloud, so the iPhone can't stream yet." : undefined} />
      </div>
      <ol>
        {phoneSetupSteps.map((s) => (
          <li key={s}>{s}</li>
        ))}
      </ol>
      <p className="muted small">Nothing to type here. The camera shows up in this list when the iPhone starts. Setting it up needs a user who can manage cameras.</p>
    </div>
  );
}

export type KeyAction = "rotate" | "revoke";

/** Rotate or revoke a phone camera's push key; answers with what to tell the admin. */
export async function runKeyAction(client: ApiClient, cameraId: string, action: KeyAction): Promise<string> {
  if (action === "rotate") {
    await phoneCameraApi.rotateKey(client, cameraId);
    return "Key rotated. The iPhone has stopped streaming; set it up again to resume.";
  }
  await phoneCameraApi.revokeKey(client, cameraId);
  return "Key revoked. The iPhone can't stream to this camera until it is set up again.";
}

export const keyActionCopy: Record<KeyAction, { ask: string; confirm: string }> = {
  rotate: {
    ask: "Rotate the key? The iPhone stops streaming at once. To resume, set it up again on the iPhone: Settings → Use this iPhone as a camera.",
    confirm: "Rotate key",
  },
  revoke: {
    ask: "Revoke the key? The iPhone stops streaming and can't send video until it is set up again. The camera, its recordings and settings stay.",
    confirm: "Revoke key",
  },
};

/** The phone camera's key: rotate (a lost or passed-on phone) or revoke, each confirmed in place. */
export function PushKeyPanel({
  camera,
  onChanged,
  client = v2,
  initialAsk = null,
}: {
  camera: SourcedCamera;
  onChanged: () => void;
  client?: ApiClient;
  initialAsk?: KeyAction | null;
}) {
  const [ask, setAsk] = useState<KeyAction | null>(initialAsk);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [done, setDone] = useState("");
  const revoked = camera.hasPushKey === false;

  async function run(a: KeyAction) {
    setBusy(true);
    setError(null);
    try {
      setDone(await runKeyAction(client, camera.id, a));
      setAsk(null);
      onChanged();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <fieldset className="field push-key">
      <legend>iPhone key</legend>
      <ErrorBanner error={error} />
      {done ? (
        <div className="notice" role="status">
          {done}
        </div>
      ) : revoked ? (
        <p className="muted small">Key revoked. Set the iPhone up again to resume streaming.</p>
      ) : ask ? (
        <div className="notice warn" role="alertdialog" aria-label={keyActionCopy[ask].confirm}>
          <p>{keyActionCopy[ask].ask}</p>
          <div className="row-actions">
            <button type="button" className="btn small" onClick={() => setAsk(null)} disabled={busy}>
              Cancel
            </button>
            <button type="button" className="btn small danger" onClick={() => void run(ask)} disabled={busy}>
              {busy ? "Working…" : keyActionCopy[ask].confirm}
            </button>
          </div>
        </div>
      ) : (
        <>
          <p className="muted small">The iPhone streams with a key it keeps. Rotate it if the phone was lost or passed on; the phone then has to be set up again.</p>
          <div className="row-actions">
            <button type="button" className="btn small" onClick={() => setAsk("rotate")}>
              Rotate key…
            </button>
            <button type="button" className="btn small danger" onClick={() => setAsk("revoke")}>
              Revoke key…
            </button>
          </div>
        </>
      )}
    </fieldset>
  );
}
