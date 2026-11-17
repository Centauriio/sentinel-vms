import { useEffect, useState } from "react";
import { api, type Camera } from "../api";
import { sectionHash } from "../admin/navModel";
import { modeLabel } from "../shared/recordingModes";
import { PageHeader } from "../shared/Cards"; // WAVE21

export default function CamerasPage() {
  const [cameras, setCameras] = useState<Camera[]>([]);
  const [error, setError] = useState("");

  const load = () => api.cameras().then(setCameras).catch((e) => setError(e.message));
  useEffect(() => {
    load();
  }, []);

  return (
    <>
      <PageHeader title="Cameras" subtitle={`${cameras.length} configured`}>
        {/* Cameras are added in Sentinel Admin, where the recording server is
            chosen; editing uses the same full editor and USB protections. */}
        <a className="btn primary" href={sectionHash("cameras")}>
          Add cameras in Admin
        </a>
      </PageHeader>
      {error && <div className="error">{error}</div>}
      <div className="card table-wrap">
        {cameras.length === 0 ? (
          <div className="empty">
            <div className="big">◉</div>
            No cameras yet. Open <a href={sectionHash("cameras")}>Sentinel Admin</a> to scan the network for ONVIF cameras or add one with its RTSP address.
          </div>
        ) : (
          <table className="list">
            <thead>
              <tr>
                <th>Name</th>
                <th>Status</th>
                <th>Stream</th>
                <th>Recording</th>
                <th>Keep</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {cameras.map((c) => (
                <CameraRow key={c.id} camera={c} />
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}

export function CameraRow({ camera: c }: { camera: Camera }) {
  const recordingLabel = c.recordingMode === "default" ? "Server default"
    : c.recordingMode ? modeLabel(c.recordingMode)
    : c.recording ? "Continuous" : "Off (live only)";
  return (
    <tr>
      <td>
        <strong>{c.name}</strong>
        {c.location && <div className="mono">{c.location}</div>}
      </td>
      <td>
        <span className={`badge ${c.online ? "green" : "red"}`}>
          <span className="dot" />
          {c.status}
        </span>
        {c.analyticsStatus && <div className="mono">Analytics: {c.analyticsStatus}</div>}
      </td>
      <td className="mono">
        {c.rtspURL}
        {c.subRTSPURL && <div>sub: {c.subRTSPURL}</div>}
      </td>
      <td><span className="badge blue">{recordingLabel}</span></td>
      <td>{c.retentionDays > 0 ? `${c.retentionDays} day${c.retentionDays === 1 ? "" : "s"}` : "Default"}</td>
      <td style={{ textAlign: "right" }}>
        <a className="btn small" href={sectionHash("cameras", { camera: c.id })}>
          Edit
        </a>
      </td>
    </tr>
  );
}
