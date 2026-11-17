import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { Camera } from "../api";
import { CameraRow } from "./CamerasPage";
import { buildTree, hashParams, parseHash, sectionHash } from "../admin/navModel";

const camera = { id: "camera /one", name: "USB camera", location: "", rtspURL: "", subRTSPURL: "", recording: true, retentionDays: 0, online: true, status: "Online" } as Camera;

describe("dashboard camera editing", () => {
  it("opens the selected camera in the full Admin editor", () => {
    const html = renderToStaticMarkup(<table><tbody><CameraRow camera={camera} /></tbody></table>);
    const href = html.match(/href="([^"]+)"/)![1];
    expect(href).toBe(sectionHash("cameras", { camera: camera.id }));
    expect(hashParams(href).get("camera")).toBe(camera.id);
    const route = parseHash(href, buildTree("Admin", [{id:"cameras", label:"Cameras", adminOnly:true}]));
    expect(route.kind === "leaf" && route.leaf.id).toBe("cameras");
  });
  it("does not label event recording as continuous", () => {
    const html = renderToStaticMarkup(<table><tbody><CameraRow camera={{...camera, recordingMode:"events"}} /></tbody></table>);
    expect(html).toContain("Events only");
    expect(html).not.toContain("24/7");
    const inherited = renderToStaticMarkup(<table><tbody><CameraRow camera={{...camera, recordingMode:"default"}} /></tbody></table>);
    expect(inherited).toContain("Server default");
  });
});
