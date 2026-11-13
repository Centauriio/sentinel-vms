import type { ApiClient } from "./api";

export interface UsbMode { format: string; width: number; height: number; fps: number }
export interface UsbProfile extends UsbMode { bitrate: number }
export interface UsbDevice { id: string; name: string; kind: "linux" | "windows"; modes: UsbMode[] }
export interface UsbCamera { cameraId: string; instance: string; device: string; kind: string; profile: UsbProfile; state: string; requested: boolean }
export interface UsbInventory { available: boolean; message?: string; devices: UsbDevice[]; cameras: UsbCamera[]; windowsOnline: boolean }
export const usbModeKey = (m: UsbMode) => `${m.format}:${m.width}:${m.height}:${m.fps}`;
export const usbModeLabel = (m: UsbMode) => `${m.width} × ${m.height} · ${m.fps} fps · ${m.format === "mjpeg" ? "MJPEG" : m.format.toUpperCase()}`;
export const usbStateLabel = (s: string) => ({ running: "Capturing", stopped: "Stopped", starting: "Starting", failed: "Capture failed", "helper-offline": "Camera helper offline" }[s] ?? "Unavailable");
const path = (id: string) => `/api/v2/usb-cameras/${encodeURIComponent(id)}`;
export const usbApi = {
 list: (c: ApiClient) => c.request<UsbInventory>("get", "/api/v2/usb-cameras"),
 create: (c: ApiClient, body: { name: string; device: string; profile: UsbProfile }) => c.request<UsbCamera>("post", "/api/v2/usb-cameras", body),
 update: (c: ApiClient, id: string, profile: UsbProfile) => c.request<UsbCamera>("put", path(id), { profile }),
 control: (c: ApiClient, id: string, action: "start" | "stop") => c.request<UsbCamera>("post", `${path(id)}/${action}`, {}),
};
