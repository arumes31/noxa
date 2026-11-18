// Native screen capture (Windows desktop), the desktop's only way to share:
// capture, H.264 encode, frame encryption and the publish-only "native" peer
// run in Go. The webview keeps the publication lifecycle and mirrors its
// "self|screen" and "self|screenaudio" media keys here.
// Frames never pass through the webview.
import { observeMediaKeys } from "./media-e2ee.js";

const app = () => window.go?.main?.App;

// The former "Screen capture: Browser/Native" choice is ignored: drop it once.
try { globalThis.localStorage?.removeItem("noxa.screenCaptureBackend"); } catch { /* storage is optional */ }

const UNAVAILABLE = ["unsupported_platform", "unsupported_windows", "server_outdated", "media_e2ee_unavailable"];

/**
 * The i18n key explaining why a native share cannot start from status
 * (NativeScreenShareStatusForTab), or "" when it can. call: a private call
 * share, which needs no server native peer.
 */
export function nativeUnavailableKey(status, call = false) {
    if (call ? status?.call_available : status?.available) return "";
    // A call share is only refused for the platform, Windows or call encryption.
    const reason = call && status?.reason === "server_outdated" ? "media_e2ee_unavailable" : status?.reason;
    return UNAVAILABLE.includes(reason) ? `share.unavailable.${reason}` : "share.unavailable";
}

/** The i18n key of the notice for a share Go ended after a failure ("failed" event reason). */
export function nativeFailureKey(reason) {
    return ["capture", "encoder", "peer", "stalled", "codec"].includes(reason) ? `share.failed.${reason}` : "share.failed";
}

export async function nativeShareStatus(tabID) {
    if (typeof app()?.NativeScreenShareStatusForTab !== "function") return { available: false, reason: "unsupported_platform", monitors: [], windows: [] };
    try {
        const status = await app().NativeScreenShareStatusForTab(tabID);
        if (status && typeof status === "object") return status;
    } catch { /* reported as unavailable below */ }
    return { available: false, reason: "unavailable", monitors: [], windows: [] };
}

/** The native source for a picker surface and option value ("m:<index>" / "w:<handle>"). */
export function nativeSource(surface, value) {
    const [kind, id] = String(value || "").split(":");
    if (surface === "monitor" && kind === "m" && /^\d+$/.test(id)) return { monitor: Number(id), window: "0" };
    if (surface === "window" && kind === "w" && /^[1-9]\d{0,19}$/.test(id)) return { monitor: 0, window: id };
    return null;
}

/**
 * The native source of a Region share: the picked monitor ("m:<index>")
 * cropped to region (fractions of the monitor, from the share dialog).
 */
export function nativeRegionSource(value, region) {
    const monitor = nativeSource("monitor", value);
    const unit = v => Number.isFinite(v) && v >= 0 && v <= 1;
    if (!monitor || !region || ![region.x, region.y, region.width, region.height].every(unit) || !(region.width > 0 && region.height > 0)) return null;
    return { ...monitor, region: { x: region.x, y: region.y, width: region.width, height: region.height } };
}

/** A larger JPEG data URL of one picker source to draw a Region on, or "". */
export async function nativeScreenPreview(value) {
    if (typeof app()?.NativeScreenPreview !== "function") return "";
    try { return await app().NativeScreenPreview(value) || ""; } catch { return ""; }
}

/** The cameras a native Studio can compose: [{ id, name }]. */
export async function nativeCameras() {
    if (typeof app()?.NativeCameras !== "function") return [];
    try { return (await app().NativeCameras() || []).filter(c => c && typeof c.id === "string" && c.id); } catch { return []; }
}

/**
 * One picker tile (radio, thumbnail, title, detail) for option o in radio
 * group name; the caller fills title and detail.
 */
export function nativeTile(o, name, onChange) {
    const tile = document.createElement("label");
    tile.className = "sp-native-tile";
    const input = document.createElement("input");
    input.type = "radio"; input.name = name; input.value = o.value;
    input.addEventListener("change", onChange);
    const img = document.createElement("img");
    img.alt = ""; img.dataset.value = o.value; img.decoding = "async";
    const title = document.createElement("span");
    title.className = "sp-native-title";
    const detail = document.createElement("span");
    detail.className = "sp-native-detail";
    tile.append(input, img, title, detail);
    return tile;
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
 * active tab: a background engine's keys must not reach another tab), or
 * { tabID, callID } for a private call's media session (call keyring).
 */
export function mirrorNativeKey(pid, op, owner) {
    const slot = pid === "self|screen" ? "screen" : pid === "self|screenaudio" ? "screenaudio" : "";
    if (!slot || typeof app()?.NativeScreenKeyForTab !== "function") return;
    const tabID = typeof owner === "string" ? owner : typeof owner?.tabID === "string" ? owner.tabID : "";
    const call = typeof owner === "object" && typeof owner?.callID === "string" ? owner.callID : "";
    if (!tabID || (typeof owner === "object" && !call)) return;
    const body = { slot, op: op.op, epoch: op.epoch || 0, kid: op.kid || 0, ...(op.key ? { key: base64(op.key) } : {}), ...(call ? { call } : {}) };
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
