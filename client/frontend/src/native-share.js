// Native screen capture (Windows desktop): capture, H.264 encode, frame
// encryption and the publish-only "native" peer run in Go. The webview keeps
// the publication lifecycle and mirrors its "self|screen" media keys here.
// Frames never pass through the webview.
import { observeMediaKeys } from "./media-e2ee.js";

const STORAGE_KEY = "noxa.screenCaptureBackend";
const app = () => window.go?.main?.App;

/** "native" or "browser" (the default in this release). */
export function screenCaptureBackend() {
    try { return localStorage.getItem(STORAGE_KEY) === "native" ? "native" : "browser"; } catch { return "browser"; }
}
export function setScreenCaptureBackend(value) {
    try { localStorage.setItem(STORAGE_KEY, value === "native" ? "native" : "browser"); } catch { /* per-device convenience only */ }
}

export async function nativeShareStatus(tabID) {
    if (typeof app()?.NativeScreenShareStatusForTab !== "function") return { available: false, reason: "unsupported_platform", monitors: [], windows: [] };
    try { return await app().NativeScreenShareStatusForTab(tabID); }
    catch { return { available: false, reason: "unavailable", monitors: [], windows: [] }; }
}

/** The native source for a picker surface and option value ("m:<index>" / "w:<handle>"). */
export function nativeSource(surface, value) {
    const [kind, id] = String(value || "").split(":");
    if (surface === "monitor" && kind === "m" && /^\d+$/.test(id)) return { monitor: Number(id), window: "0" };
    if (surface === "window" && kind === "w" && /^[1-9]\d{0,19}$/.test(id)) return { monitor: 0, window: id };
    return null;
}

/** Picker options for a surface from NativeScreenShareStatusForTab. */
export function nativeSourceOptions(status, surface) {
    if (surface === "monitor") return (status.monitors || []).map(m => ({ value: `m:${m.index}`, label: `${m.primary ? "★ " : ""}${m.width} × ${m.height}`, selected: !!m.primary }));
    if (surface === "window") return (status.windows || []).filter(w => w.title && w.title !== document.title).map(w => ({ value: `w:${w.handle}`, label: w.title, selected: false }));
    return [];
}

let keyChain = Promise.resolve();
const base64 = bytes => btoa(String.fromCharCode(...bytes));

/** Mirrors one media key engine operation for the own screen slot to Go. */
export function mirrorNativeKey(pid, op) {
    if (pid !== "self|screen" || typeof app()?.NativeScreenKeyForTab !== "function") return;
    const tabID = window.__noxa?.state?.activeTabID;
    if (!tabID) return;
    const body = { op: op.op, epoch: op.epoch || 0, kid: op.kid || 0, ...(op.key ? { key: base64(op.key) } : {}) };
    // Bound calls run concurrently in Go: keep key → activate → retire order.
    keyChain = keyChain.then(() => app().NativeScreenKeyForTab(tabID, body)).catch(() => {});
}

/** The native sender's diagnostics row, or null. */
export async function nativeSenderRow(tabID) {
    if (!tabID || typeof app()?.NativeScreenDiagnosticsForTab !== "function") return null;
    try { return await app().NativeScreenDiagnosticsForTab(tabID) || null; } catch { return null; }
}

/** A still placeholder track: the publication identity of a native share (never sent). */
export function nativeStandInStream(label) {
    const canvas = document.createElement("canvas");
    canvas.width = 320; canvas.height = 180;
    const g = canvas.getContext("2d");
    g.fillStyle = "#111"; g.fillRect(0, 0, 320, 180);
    g.fillStyle = "#ddd"; g.font = "16px sans-serif"; g.textAlign = "center";
    g.fillText(label, 160, 95);
    return canvas.captureStream(1);
}

observeMediaKeys(mirrorNativeKey);
