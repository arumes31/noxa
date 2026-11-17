// Native screen capture (Windows desktop): capture, H.264 encode, frame
// encryption and the publish-only "native" peer run in Go. The webview keeps
// the publication lifecycle and mirrors its "self|screen" and
// "self|screenaudio" media keys here.
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

const SOURCE_KEY = "noxa.nativeShareSource";

/** The last native source shared from this device: { surface, value, exe, title, size } or null. */
export function rememberedNativeSource() {
    try {
        const saved = JSON.parse(localStorage.getItem(SOURCE_KEY) || "null");
        return saved && typeof saved === "object" && typeof saved.surface === "string" ? saved : null;
    } catch { return null; }
}

/** Remembers the picker option a native share started with (per-device convenience). */
export function rememberNativeSource(surface, option) {
    if (!option) return;
    const saved = { surface, value: option.value, exe: option.exe || "", title: option.title || "", size: option.size || "" };
    try { localStorage.setItem(SOURCE_KEY, JSON.stringify(saved)); } catch { /* per-device convenience only */ }
}

/**
 * Picker options for a surface from NativeScreenShareStatusForTab, in
 * display order with exactly one selected (when any exist). Windows: a
 * running game (a window covering its monitor, the focused one first) comes
 * first, then the rest in z-order. Selection: the remembered source (same
 * executable and title, else the same executable), else the game, else the
 * first window; monitors: the remembered one if its size still matches,
 * else the primary.
 */
export function nativeSourceOptions(status, surface, remembered = rememberedNativeSource()) {
    const memory = remembered?.surface === surface ? remembered : null;
    let options = [];
    let pick = () => -1;
    if (surface === "monitor") {
        options = (status.monitors || []).map(m => ({ value: `m:${m.index}`, label: `${m.primary ? "★ " : ""}${m.width} × ${m.height}`,
            size: `${m.width}x${m.height}`, primary: !!m.primary }));
        pick = () => {
            const same = options.findIndex(o => memory && o.value === memory.value && o.size === memory.size);
            return same >= 0 ? same : options.findIndex(o => o.primary);
        };
    } else if (surface === "window") {
        const foreground = String(status.foreground || "");
        const windows = (status.windows || []).filter(w => w.title).map((w, z) => ({ value: `w:${w.handle}`, label: w.title, title: w.title,
            exe: w.exe || "", game: !!w.fullscreen, rank: w.fullscreen ? (String(w.handle) === foreground ? 0 : 1) : 2, z }));
        options = windows.sort((a, b) => a.rank - b.rank || a.z - b.z);
        pick = () => {
            const exe = memory?.exe?.toLowerCase();
            const exact = options.findIndex(o => exe && o.exe.toLowerCase() === exe && o.title === memory.title);
            if (exact >= 0) return exact;
            const sameApp = options.findIndex(o => exe && o.exe.toLowerCase() === exe);
            if (sameApp >= 0) return sameApp;
            return options.findIndex(o => o.game);
        };
    }
    const selected = Math.max(pick(), 0);
    return options.map(({ rank, z, ...o }, i) => ({ ...o, selected: i === selected }));
}

/** JPEG data URLs for picker values ("m:…"/"w:…"); missing entries have no thumbnail. */
export async function nativeThumbnails(values) {
    if (!values.length || typeof app()?.NativeScreenThumbnails !== "function") return {};
    try { return await app().NativeScreenThumbnails(values) || {}; } catch { return {}; }
}

let keyChain = Promise.resolve();
const base64 = bytes => btoa(String.fromCharCode(...bytes));

/**
 * Mirrors one media key engine operation for the own screen and screen
 * audio slots to Go.
 * owner is the tab of the channel media session that issued it (never the
 * active tab: a background engine's keys must not reach another tab).
 */
export function mirrorNativeKey(pid, op, owner) {
    const slot = pid === "self|screen" ? "screen" : pid === "self|screenaudio" ? "screenaudio" : "";
    if (!slot || typeof app()?.NativeScreenKeyForTab !== "function") return;
    const tabID = typeof owner === "string" ? owner : "";
    if (!tabID) return;
    const body = { slot, op: op.op, epoch: op.epoch || 0, kid: op.kid || 0, ...(op.key ? { key: base64(op.key) } : {}) };
    // Bound calls run concurrently in Go: keep key → activate → retire order.
    keyChain = keyChain.then(() => app().NativeScreenKeyForTab(tabID, body)).catch(() => {});
}

/** Whether a "native_share" event belongs to the installed native share. */
export function nativeEventIsCurrent(event, native) {
    return !!native && !!event && event.share === native.id && event.tab === native.tabID;
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
