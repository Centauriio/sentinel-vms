// WAVE22(C): the camera editor's Recording section, the list badge, and the patch it sends.
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { recordingDefaults, recordingFormOf, type RecCamera, type RecordingForm } from "../shared/recordingModes";
import { CameraEditor, RecordingCell, cameraPatch } from "./CamerasSection";
import { settingsPatch, minFreeLabel } from "./RecordingDefaults";
import RecordingFields from "./RecordingFields";

const camera = (over: Partial<RecCamera> = {}) =>
  ({
    id: "c1",
    name: "Door",
    location: "",
    rtspUrl: "rtsp://cam/main",
    subRtspUrl: "rtsp://cam/sub",
    username: "",
    recording: true,
    retentionDays: 0,
    motionLevel: 2,
    alertOn: "auto",
    onvifUrl: "",
    cameraAnalytics: false,
    recorderId: "local",
    groupIds: [],
    sortOrder: 0,
    recordingMode: "default",
    recordTrigger: "default",
    preRollSeconds: 0,
    postRollSeconds: 0,
    lowResRetentionDays: 0,
    ...over,
  }) as unknown as RecCamera;

const render = (form: Partial<RecordingForm>, props: Partial<Parameters<typeof RecordingFields>[0]> = {}) =>
  renderToStaticMarkup(
    <RecordingFields
      form={{ ...recordingFormOf(null), ...form }}
      onChange={() => {}}
      hasSub
      motionLevel={2}
      analytics={false}
      server={recordingDefaults}
      {...props}
    />,
  );

describe("Recording section", () => {
  it("names the server default and recommends dual with a sub-stream", () => {
    const html = render({});
    expect(html).toContain("Use server default (Continuous)");
    expect(html).toMatch(/Low-res 24\/7 \+ full-res on events<\/strong><span class="badge green rec-tag">Recommended/);
    expect(html).not.toContain("Nothing will start a recording");
  });

  it("recommends continuous and disables dual without a sub-stream", () => {
    const html = render({}, { hasSub: false });
    expect(html).toMatch(/Continuous<\/strong><span class="badge green rec-tag">Recommended/);
    expect(html).toMatch(/disabled="" name="recordingMode" value="dual"/);
    expect(html).toContain("Needs a second stream");
  });

  it("shows trigger and pre/post-roll for events, low-res keep days for dual", () => {
    const events = render({ recordingMode: "events" });
    expect(events).toContain("People and vehicles only");
    expect(events).toContain("Server default (5 s)");
    expect(events).toContain("Server default (15 s)");
    expect(events).not.toContain("Keep low-res");
    const dual = render({ recordingMode: "dual" });
    expect(dual).toContain("Keep full-res");
    expect(dual).toContain("Keep low-res");
    expect(render({ recordingMode: "continuous" })).not.toContain("Before the event");
    expect(render({ recordingMode: "off" })).not.toContain("Keep recordings");
  });

  it("warns when nothing can start a recording", () => {
    expect(render({ recordingMode: "events" }, { motionLevel: 0, analytics: false })).toContain("Nothing will start a recording. Turn on motion detection or camera analytics.");
    expect(render({ recordingMode: "events" }, { motionLevel: 0, analytics: true })).not.toContain("Nothing will start");
  });

  it("shows GB a day only when the ingest rate is known", () => {
    expect(render({ recordingMode: "continuous" }, { kbps: 4000 })).toContain("About 43 GB a day");
    expect(render({ recordingMode: "continuous" })).not.toContain("GB a day");
  });

  it("offers only 24/7 or off to a server without modes", () => {
    const html = render({}, { legacy: true });
    expect(html).toContain("Record 24/7");
    expect(html).not.toContain("Events only");
  });
});

describe("camera list badge", () => {
  it("shows the mode", () => {
    expect(renderToStaticMarkup(<RecordingCell camera={camera()} server={recordingDefaults} />)).toContain(">24/7<");
    expect(renderToStaticMarkup(<RecordingCell camera={camera({ recordingMode: "events" })} server={recordingDefaults} />)).toContain(">Events<");
    const dual = renderToStaticMarkup(<RecordingCell camera={camera({ effectiveRecordingMode: "dual", retentionDays: 7 })} server={recordingDefaults} />);
    expect(dual).toContain("24/7 low + events");
    expect(dual).toContain("Keep 7 d · low 14 d");
    expect(renderToStaticMarkup(<RecordingCell camera={camera({ recordingMode: "off", effectiveRecordingMode: "off" })} server={null} />)).toContain("Live only");
  });
});

describe("cameraPatch", () => {
  it("sends the changed recording fields and no stale recording flag", () => {
    const c = camera();
    const form = { ...c, password: "", clearPassword: false, rec: recordingFormOf(c), audioDetect: false, audioThresholdDb: 25, loiterSeconds: 0 };
    expect(cameraPatch(c, form as never)).toEqual({});
    expect(cameraPatch(c, { ...form, rec: { ...form.rec, recordingMode: "events", postRollSeconds: 30 } } as never)).toEqual({ recordingMode: "events", postRollSeconds: 30 });
  });
});

describe("server defaults", () => {
  it("patches only what changed", () => {
    expect(settingsPatch(recordingDefaults, recordingDefaults)).toEqual({});
    expect(settingsPatch(recordingDefaults, { ...recordingDefaults, defaultMode: "dual", minFreePercent: 10 })).toEqual({ defaultMode: "dual", minFreePercent: 10 });
    expect(minFreeLabel(0)).toBe("Off");
    expect(minFreeLabel(10)).toBe("10%");
  });
});

describe("existing USB camera recording settings", () => {
  it("offers events-only recording while keeping stream credentials managed", () => {
    const html = renderToStaticMarkup(<CameraEditor camera={{...camera({subRtspUrl:""}), preRollSeconds: 0, postRollSeconds: 0}} servers={[]} groups={[]} recDefaults={recordingDefaults} ingest={null} usbManaged onClose={() => {}} onSaved={() => {}} />);
    expect(html).toContain("Events only");
    expect(html).toContain('value="events"');
    expect(html).toContain("USB connection settings are managed automatically");
    expect(html).not.toContain('type="password"');
  });
});
