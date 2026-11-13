import { useState } from "react";
import type { UsbCamera, UsbInventory } from "../shared/usbCameras";
import { usbApi, usbModeKey, usbModeLabel } from "../shared/usbCameras";
import { v2 } from "./client";
import { Modal } from "./ui";

export default function UsbCameraDialog({ inventory, camera, onRefresh, onClose, onSaved }: {
 inventory: UsbInventory | null; camera?: UsbCamera; onRefresh: () => void; onClose: () => void; onSaved: () => void;
}) {
 const devices = inventory?.devices ?? [];
 const used = new Set(inventory?.cameras.filter(c => c.cameraId !== camera?.cameraId).map(c => c.device));
 const available = devices.filter(d => !used.has(d.id));
 const [deviceId, setDeviceId] = useState(camera?.device ?? available[0]?.id ?? "");
 const device = available.find(d => d.id === deviceId);
 const [modeKey, setModeKey] = useState(camera?.profile ? usbModeKey(camera.profile) : "");
 const modes = device?.modes ?? [];
 const mode = modes.find(m => usbModeKey(m) === modeKey) ?? modes.find(m => m.width === 1920 && m.height === 1080 && m.fps === 30) ?? modes[0];
 const [name, setName] = useState("");
 const [bitrate, setBitrate] = useState(camera?.profile?.bitrate ?? 4000);
 const [busy, setBusy] = useState(false);
 const [error, setError] = useState("");
 const save = async () => {
  if (!mode || !device) return;
  setBusy(true); setError("");
  try {
   const profile = { ...mode, bitrate };
   if (camera) await usbApi.update(v2, camera.cameraId, profile);
   else await usbApi.create(v2, { name: name.trim() || device.name, device: device.id, profile });
   onSaved();
  } catch (e) { setError(e instanceof Error ? e.message : "Camera could not be saved."); }
  finally { setBusy(false); }
 };
 return <Modal title={camera ? "USB camera quality" : "Add USB camera"} onClose={() => { if (!busy) onClose(); }} onSubmit={e => { e.preventDefault(); void save(); }}
  actions={<><button type="button" className="btn" disabled={busy} onClick={onClose}>Cancel</button><button className="btn primary" type="submit" disabled={busy || !inventory?.available || !mode}>{busy ? "Saving…" : camera ? "Save quality" : "Add camera"}</button></>}>
  {error && <div role="alert" className="error-banner">{error}</div>}
  {!inventory?.available && <p role="status">{inventory?.message ?? "Checking the USB camera helper…"}</p>}
  <p>Choose a camera connected to this server. On Windows with WSL, the installed Windows camera helper makes your cameras available here.</p>
  <label className="field"><span>Camera</span><select value={deviceId} disabled={busy || !!camera} onChange={e => { setDeviceId(e.target.value); setModeKey(""); }}>
   <option value="">Select a camera</option>
   {camera && !device && <option value={camera.device}>Camera disconnected</option>}
   {available.map(d => <option key={d.id} value={d.id}>{d.name} ({d.kind === "windows" ? "Windows" : "Linux"})</option>)}
  </select></label>
  <button className="btn small" type="button" disabled={busy} onClick={onRefresh}>Refresh cameras</button>
  {inventory?.available && !available.length && <p role="status">No available cameras found. Connect a camera and refresh. A camera already added to Sentinel appears in the camera list.</p>}
  {!camera && <label className="field"><span>Name</span><input value={name} maxLength={200} placeholder={device?.name ?? "USB camera"} disabled={busy} onChange={e => setName(e.target.value)} /></label>}
  <label className="field"><span>Quality</span><select value={mode ? usbModeKey(mode) : ""} disabled={busy || !modes.length} onChange={e => setModeKey(e.target.value)}>
   {!modes.length && <option value="">Reconnect the camera to see its quality options</option>}
   {modes.map(m => <option key={usbModeKey(m)} value={usbModeKey(m)}>{usbModeLabel(m)}</option>)}
  </select><small>Only modes reported by this camera are offered. Higher resolution and frame rate use more processing power and storage.</small></label>
  <label className="field"><span>Video bitrate (kbps)</span><input type="number" min={128} max={50000} step={1} required value={bitrate} disabled={busy} onChange={e => setBitrate(Number(e.target.value))} /></label>
  <p>{camera ? "Saving quality restarts capture if this camera is running." : "The camera is added stopped. Use Start capture in the camera list when you are ready."}</p>
 </Modal>;
}
