import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import UsbCameraDialog from "./UsbCameraDialog";
import { usbApi, usbModeLabel, usbShouldStop, type UsbInventory, type UsbCamera } from "../shared/usbCameras";
import type { ApiClient } from "../shared/api";

const inventory: UsbInventory = { available: true, windowsOnline: true, cameras: [], devices: [{ id: "windows:123", name: "Desk camera", kind: "windows", modes: [{ format: "mjpeg", width: 1920, height: 1080, fps: 30 }] }] };
const noop = () => {};
describe("USB camera setup", () => {
 it("keeps Stop available after a failed stop until capture actually stops", () => {
  expect(usbShouldStop({requested:false, state:"running"} as UsbCamera)).toBe(true);
  expect(usbShouldStop({requested:true, state:"helper-offline"} as UsbCamera)).toBe(true);
  expect(usbShouldStop({requested:false, state:"stopped"} as UsbCamera)).toBe(false);
 });
 it("offers advertised quality without connection URL fields", () => {
  const html=renderToStaticMarkup(<UsbCameraDialog inventory={inventory} onRefresh={noop} onClose={noop} onSaved={noop} />);
  expect(html).toContain("Desk camera (Windows)");
  expect(html).toContain("30 fps"); expect(html).not.toContain("60 fps");
  expect(html).not.toContain("rtsp://"); expect(html).not.toContain('type="password"');
  expect(html).toContain("camera is added stopped");
 });
 it("explains missing helper and disables adding", () => {
  const html=renderToStaticMarkup(<UsbCameraDialog inventory={{...inventory, available:false, devices:[], message:"USB helper unavailable"}} onRefresh={noop} onClose={noop} onSaved={noop} />);
  expect(html).toContain("USB helper unavailable");
  expect(html).toMatch(/type="submit" disabled=""/);
 });
 it("routes capture controls to the USB API with a JSON body", async () => {
  const calls: unknown[][]=[];
  const client={ request: async (...args: unknown[]) => { calls.push(args); return {}; } } as unknown as ApiClient;
  await usbApi.control(client,"camera/one","stop");
  expect(calls).toEqual([["post","/api/v2/usb-cameras/camera%2Fone/stop",{}]]);
  expect(usbModeLabel(inventory.devices[0].modes[0])).toContain("1920 × 1080");
 });
});
