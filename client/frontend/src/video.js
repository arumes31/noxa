// video.js — video grid (61), focus mode with filmstrip (73), per-subscriber
// quality selector (63), share dialog (69-72), camera on/off + stop-share
// confirm (85), and the low-bandwidth mode (88). Everything here works against
// the shared namespace (window.__noxa) populated by main.js.
import { GridCompositor } from "./grid-compositor.js";
import { captureMediaScope, mediaScopeIsCurrent } from "./media-controls.js";
import { startPublication, stopPublication } from "./stream-publication.js";

import { isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { setSafeImage } from "./safe-media.js";
import { labelButton } from "./icons.js";
import { renderMicStatus } from "./audio.js";
import { videoConstraints, trackFitsVideoLimits, capVideoEncodings } from "./media-limits.js";

import { t as tLabel, currentLanguage } from "./i18n.js";

const V = () => window.__noxa;

// (70/73) Track-identity contract with the router: a publisher's media arrives
// as one track per SLOT, so camera and screen share are separate tiles and
// shared system audio is a separate source from the microphone.
//   microphone ("mic")            track id "<clientID>"
//                                  msid stream "noxa-<clientID>"
//   other slots ("cam", "screenaudio", "screen")
//                                  track id "<clientID>|<slot>"
//                                  msid stream "noxa-<clientID>|<slot>"
// The separator is "|" because an msid id is an RFC 4566 token and "/" is not
// a token character. Microphones keep the bare publisher ID, so parsing
// yields slot "" for them and a router that labels nothing still resolves.
const SLOT_SCREEN = "screen";
export const SLOT_SCREEN_AUDIO = "screenaudio"; // main.js routes this slot's audio
const SLOT_SEP = "|";

// parseTrackID splits a media track id into publisher id and slot.
export function parseTrackID(id) {
    const s = String(id ?? "");
    const i = s.indexOf(SLOT_SEP);
    return i < 0 ? { clientID: s, slot: "" } : { clientID: s.slice(0, i), slot: s.slice(i + 1) };
}

// tiles maps media track id -> {el, video, nameEl, kindEl, badgeEl, track,
// stream, clientID, slot, frames, stalls, flowing}. The key is the TRACK id,
// not the publisher, so one publisher can own several tiles (73). track is
// that slot's own video track and stream the per-tile MediaStream wrapping it
// (never the shared stream from ontrack).
const tiles = new Map();
let focusedID = null; // track id of the focused tile

document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape" || !document.fullscreenElement?.classList.contains("vtile")) return;
    event.preventDefault();
    event.stopPropagation();
    void document.exitFullscreen().catch(() => {});
}, true);
document.addEventListener("fullscreenchange", () => videoRefreshNames());
window.addEventListener("noxa-language-changed", () => {
    videoRefreshNames();
    syncCameraButton();
    syncShareButton();
});

// Connection-wide video quality preference. NOTE: the server's simulcast
// routing keeps ONE layer preference per subscriber (all publishers), so the
// tile context menu sets a shared preference, not a per-tile one — the menu
// says so. "auto" maps: focused tile -> high, grid view -> mid, low-bandwidth
// -> low.
let qualityPref = "auto"; // auto | high | mid | low
let lastSentQuality = "";
let qualityRequest = null;
let idleOverride = false; // (342) window idle: force low without losing the pref
let cpuPressure = false;
let receiveCpuPressure = false;
let sendCpuPressure = false;
let cpuRecoverySince = null;
let statsPollRequest = null;
let autoNetworkQuality = "high";
let statsTimer = null;
let compositeTimer = null;
let compositeVideo = null;

// ---------------------------------------------------------------------------
// Grid (61)
// ---------------------------------------------------------------------------

function gridEl() { return document.getElementById("video-grid"); }

// videoTrackAdded registers (or re-registers) one publisher video slot as a
// grid tile. trackID follows the slot contract above, so a publisher sending
// camera and screen at once gets one tile per slot (73).
export function videoTrackAdded(trackID, stream, publisher, watchControls) {
    const key = String(trackID);
    const parsed = parseTrackID(key);
    const clid = String(publisher?.client_id || parsed.clientID);
    let t = tiles.get(key);
    if (!t) {
        const el = document.createElement("div");
        el.className = "vtile";
        el.dataset.clid = clid;
        el.dataset.slot = parsed.slot;
        el.innerHTML = `
            <video autoplay playsinline></video>
            <div class="vtile-fallback"><div class="avatar"></div></div>
            <div class="vtile-label">
                <span class="vtile-name"></span>
                <span class="vtile-kind hidden"></span>
                <span class="vtile-badge hidden"></span>
                <span class="vtile-preview-age hidden"></span>
            </div>
            <button class="vtile-pip icon-btn" title="floating always-on-top video">▣</button>
            <button class="vtile-fullscreen icon-btn" title="fullscreen (Esc exits)">⛶</button>`;
        const video = el.querySelector("video");
        const nameEl = el.querySelector(".vtile-name");
        const kindEl = el.querySelector(".vtile-kind");
        const badgeEl = el.querySelector(".vtile-badge");
        // The fallback panel is opaque and paints over the <video>, so the
        // has-video class has to follow the element's decode state.
        video.onloadeddata = video.onplaying = video.onemptied = () => {
            updateTileVideo(t);
            // (88) a paused tile must show a still image, not an avatar: the
            // point of the mode is to stop decoding while still showing who is
            // on camera. So pausing waits for the first decoded frame — an
            // element paused before that has nothing to hold and would sit on
            // the avatar for the rest of the call.
            if (lowBandwidth && t.video.readyState >= 2) t.video.pause();
            updatePreviewAge(t);
        };
        // (340) fullscreen video tile.
        el.querySelector(".vtile-fullscreen").onclick = async (e) => {
            e.stopPropagation();
            try {
                if (document.fullscreenElement === el) await document.exitFullscreen();
                else await el.requestFullscreen();
            } catch (error) {
                V().toast(tLabel("polish.fullscreenFailed", { error: error.message || String(error) }), "warn");
            }
        };
        const pip = el.querySelector(".vtile-pip");
        pip.disabled = !document.pictureInPictureEnabled;
        pip.onclick = async (e) => {
            e.stopPropagation();
            try {
                if (document.pictureInPictureElement === video) await document.exitPictureInPicture();
                else {
                    if (document.pictureInPictureElement) await document.exitPictureInPicture();
                    await video.requestPictureInPicture();
                }
            } catch (err) {
                V().sysMsg(tLabel("polish.floatingFailed", { error: err.message || err }));
            }
        };
        el.onclick = () => toggleFocus(key);
        el.oncontextmenu = (e) => {
            e.preventDefault();
            openTileMenu(e.clientX, e.clientY, t);
        };
        t = {
            el, video, nameEl, kindEl, badgeEl, track: null, stream: null,
            clientID: clid, slot: parsed.slot, frames: 0, stalls: 0, flowing: true,
        };
        tiles.set(key, t);
        gridEl().appendChild(el);
    }
    t.clientID = clid;
    t.watchControls = watchControls || null;
    if (watchControls) t.el.append(watchControls);
    t.el.dataset.clid = clid;
    // (61) select by track id and render from a private stream, so a tile can
    // never follow another slot or publisher even if the ontrack stream ever
    // carries more than one track.
    const vt = stream?.getVideoTracks().find((tr) => tr.id === key) || null;
    t.track = vt;
    if (t.frameCallback != null) t.video.cancelVideoFrameCallback(t.frameCallback);
    t.frameCallback = null;
    t.lastFrameAt = null;
    t.stream = vt ? new MediaStream([vt]) : null;
    t.video.srcObject = t.stream;
    if (vt && t.video.requestVideoFrameCallback) {
        const presented = () => {
            t.lastFrameAt = Date.now();
            if (t.video.paused) updatePreviewAge(t);
            t.frameCallback = t.video.requestVideoFrameCallback(presented);
        };
        t.frameCallback = t.video.requestVideoFrameCallback(presented);
    }
    t.frames = 0;
    t.stalls = 0;
    t.flowing = true;
    // Reserved receiver tracks are not evidence that a camera is publishing.
    if (vt) {
        vt.onmute = vt.onended = () => updateTileVideo(t);
        vt.onunmute = () => { t.flowing = true; updateTileVideo(t); };
    }
    updateTileVideo(t);
    applyTileIdentity(t);
    layoutGrid();
    startStatsPoll();
    return t.el;
}

// videoTrackRemoved drops one slot's tile (track ended / publisher left).
export function videoTrackRemoved(trackID) {
    const key = String(trackID);
    const t = tiles.get(key);
    if (!t) return;
    if (t.frameCallback != null) t.video.cancelVideoFrameCallback(t.frameCallback);
    t.video.srcObject = null;
    t.el.remove();
    tiles.delete(key);
    if (focusedID === key) focusedID = null;
    layoutGrid();
    if (tiles.size === 0) stopStatsPoll();
}

// videoSpeaking toggles the speaking ring on every tile of a publisher: with
// slot tiles (73) a speaker can own a camera tile and a screen tile, and a
// publisher with no video at all still gets the ring around their avatar (61).
export function videoSpeaking(clientID, speaking) {
    const clid = String(clientID);
    for (const t of tiles.values()) {
        if (t.clientID === clid) t.el.classList.toggle("speaking", speaking);
    }
}

// videoRefreshNames re-resolves nickname/avatar on all tiles (user joined,
// moved, avatar changed, started/stopped sharing).
export function videoRefreshNames() {
    for (const t of tiles.values()) { applyTileIdentity(t); updateTileVideo(t); updatePreviewAge(t); }
}

// clearVideoGrid removes all tiles (voice teardown).
export function clearVideoGrid() {
    qualityRequest = null;
    statsPollRequest = null;
    cpuPressure = false;
    receiveCpuPressure = false;
    sendCpuPressure = false;
    cpuRecoverySince = null;
    for (const key of [...tiles.keys()]) videoTrackRemoved(key);
    focusedID = null;
    // the next session starts from the server's default layer, so a stale
    // lastSentQuality would suppress the first push of this session.
    lastSentQuality = "";
    layoutGrid();
}

// applyTileIdentity fills the nickname label, the "what is this" marker and the
// avatar fallback (61: avatar-with-ring fallback behind the video; visible when
// no frames flow, e.g. camera off or black screen share).
function applyTileIdentity(t) {
    const { state, initials, fetchAvatar } = V();
    const clid = t.clientID;
    const c = state.clients.find((c) => String(c.client_id) === clid);
    const name = c ? (c.nickname || c.unique_id) : clid;
    t.nameEl.textContent = name;
    // (73) several tiles can carry the same nickname, so each one says what it
    // shows.
    const isScreen = tileIsScreen(t);
    t.kindEl.textContent = tLabel(isScreen ? "polish.screen" : "polish.camera");
    t.kindEl.classList.remove("hidden");
    t.el.classList.toggle("is-screen", isScreen);
    t.el.title = name + " — " + t.kindEl.textContent;
    const full = t.el.querySelector(".vtile-fullscreen");
    full.title = tLabel(document.fullscreenElement === t.el ? "polish.exitFullscreen" : "polish.fullscreen");
    full.setAttribute("aria-label", full.title + " · " + name);
    const pip = t.el.querySelector(".vtile-pip");
    pip.title = tLabel("polish.floating");
    pip.setAttribute("aria-label", pip.title + " · " + name);
    const av = t.el.querySelector(".vtile-fallback .avatar");
    const uid = c?.unique_id || "";
    av.dataset.uid = uid;
    const url = uid && state.avatars.get(uid);
    if (url) {
        setSafeImage(av, url);
    } else {
        av.textContent = initials(name || "?");
        if (uid) fetchAvatar(uid);
    }
    t.el.classList.toggle("speaking", !!(c && c.is_speaking));
}

function tileIsScreen(t) {
    return t.slot === SLOT_SCREEN;
}

// A paused preview describes the frame actually presented, not the latest
// packet received by the peer. Live video has no stale-preview label.
function updatePreviewAge(tile) {
    const label = tile.el.querySelector(".vtile-preview-age");
    const visible = tileIsScreen(tile) && tile.video.paused && tile.lastFrameAt !== null;
    label.classList.toggle("hidden", !visible);
    if (!visible) return;
    const minutes = Math.max(0, Math.floor((Date.now() - tile.lastFrameAt) / 60000));
    label.textContent = tLabel(minutes === 0 ? "polish.previewNow" : minutes === 1 ? "polish.previewMinute" : "polish.previewMinutes", { count: minutes });
    label.title = new Date(tile.lastFrameAt).toLocaleString(currentLanguage());
}

// updateTileVideo toggles has-video, which hides the avatar fallback. Frames
// have to be decoded (readyState >= HAVE_CURRENT_DATA), the track live, and
// the stream not stalled (61, see refreshBadges): a paused low-bandwidth tile
// still shows its last frame, a camera that was switched off does not.
function updateTileVideo(t) {
    const track = t.track;
    const live = !!track && track.readyState === "live" && track.enabled && !track.muted;
    const active = !!t.watchControls || live && t.flowing;
    t.el.classList.toggle("hidden", !active);
    t.el.classList.toggle("has-video", live && t.flowing && t.video.readyState >= 2);
    layoutGrid();
}

// layoutGrid recomputes the auto-layout class (1→full, 2→half, 3-4→2x2,
// more→scrollable) and hides the grid when empty so chat keeps the space.
function layoutGrid() {
    const grid = gridEl();
    const visible = [...tiles.entries()].filter(([, tile]) => !tile.el.classList.contains("hidden"));
    const n = visible.length;
    grid.classList.toggle("hidden", n === 0);
    grid.dataset.count = n <= 4 ? String(n) : "many";
    grid.classList.toggle("has-focus", !!focusedID && visible.some(([key]) => key === focusedID));
    for (const [key, t] of tiles) t.el.classList.toggle("focused", key === focusedID);
}

// ---------------------------------------------------------------------------
// Focus mode (73): click a tile for the large view + filmstrip; click again
// or Esc to return to the grid. Other tiles keep playing in the strip.
// ---------------------------------------------------------------------------

function toggleFocus(clid) {
    focusedID = focusedID === clid ? null : clid;
    layoutGrid();
    pushQuality(); // auto heuristic: focused = high, grid = mid
}

export function initVideo() {
    window.addEventListener("noxa-language-changed", () => { syncCameraButton(); syncShareButton(); syncLowBandwidthButton(); });
    syncCameraButton();
    syncShareButton();
    syncLowBandwidthButton();
    document.addEventListener("keydown", (e) => {
        if (e.key === "Escape" && focusedID) {
            focusedID = null;
            layoutGrid();
            pushQuality();
        }
    });
}

// ---------------------------------------------------------------------------
// Quality selector (63)
// ---------------------------------------------------------------------------

// effectiveQuality maps the preference to a concrete layer. Auto heuristic:
// focused view -> high, grid view -> mid, low-bandwidth mode -> low.
function effectiveQuality() {
    if (lowBandwidth || idleOverride || receiveCpuPressure) return "low";
    if (qualityPref !== "auto") return qualityPref;
    const viewQuality = focusedID ? "high" : "mid";
    const rank = { low: 0, mid: 1, high: 2 };
    return rank[autoNetworkQuality] < rank[viewQuality] ? autoNetworkQuality : viewQuality;
}

// setIdleQualityOverride is the idle-pause hook (342). It goes through the
// state machine so lastSentQuality stays in sync with the server and the
// user's preference comes back on resume.
export function setIdleQualityOverride(on) {
    if (idleOverride === on) return;
    idleOverride = on;
    pushQuality();
}

// pushQuality sends MsgVideoQuality when the effective layer changed.
async function pushQuality() {
    const q = effectiveQuality();
    // (88) the persisted restore and the voice-bar toggle both run while
    // disconnected, where there is no connection to send the preference on.
    // Clearing the last-sent value keeps the first push after joining honest.
    if (!V().state.pc) {
        lastSentQuality = "";
        return;
    }
    if (q === lastSentQuality) return;
    lastSentQuality = q;
    const scope = captureMediaScope();
    const request = { scope, pc: V().state.pc };
    qualityRequest = request;
    let err;
    try { err = await window.go.main.App.SetVideoQualityForTab(scope.tabID, q); }
    catch (error) { err = String(error); }
    if (qualityRequest !== request || !mediaScopeIsCurrent(scope) || V().state.pc !== request.pc) return;
    qualityRequest = null;
    if (err) {
        lastSentQuality = "";
        V().sysMsg("video quality failed: " + err);
    }
}

let qMenuEl = null;

// openTileMenu is the tile right-click menu: the shared receive-quality
// preference (63) plus, when the tile's publisher is sharing system audio,
// that share's own volume/mute (70).
function openTileMenu(x, y, t) {
    if (qMenuEl) qMenuEl.remove();
    qMenuEl = document.createElement("div");
    qMenuEl.className = "ctx-menu";
    const cur = qualityPref;
    const sa = V().shareAudioCtl?.get(t.clientID) || null;
    qMenuEl.innerHTML = `
        <a data-q="auto">${cur === "auto" ? "✓ " : ""}${tLabel("polish.videoAuto")}</a>
        <a data-q="high">${cur === "high" ? "✓ " : ""}${tLabel("polish.videoHigh")}</a>
        <a data-q="mid">${cur === "mid" ? "✓ " : ""}${tLabel("polish.videoMid")}</a>
        <a data-q="low">${cur === "low" ? "✓ " : ""}${tLabel("polish.videoLow")}</a>
        <div class="ctx-divider"></div>
        <a class="ctx-note">${tLabel("polish.videoQualityHelp")}</a>
        <a data-grid-pip="1">${tLabel("polish.floatGrid")}</a>` + (sa && tileIsScreen(t) ? `
        <div class="ctx-divider"></div>
        <a data-sa="mute">${tLabel(sa.muted ? "polish.sharedAudioUnmute" : "polish.sharedAudioMute")}</a>
        <div class="ctx-volume">
            <span>${tLabel("polish.sharedAudio")} <output class="mono ctx-vol-pct">${sa.volume}%</output></span>
            <input type="range" aria-label="${tLabel("polish.sharedAudio")}" aria-valuetext="${sa.volume}%" min="0" max="200" value="${sa.volume}" />
        </div>
        <a class="ctx-note">${tLabel("polish.sharedAudioHelp")}</a>` : "");
    qMenuEl.style.left = Math.min(x, window.innerWidth - 260) + "px";
    qMenuEl.style.top = Math.min(y, window.innerHeight - (sa ? 320 : 200)) + "px";
    qMenuEl.onclick = (e) => e.stopPropagation();
    for (const a of qMenuEl.querySelectorAll("a[data-q]")) {
        a.onclick = () => {
            qualityPref = a.dataset.q;
            pushQuality();
            qMenuEl.remove();
            qMenuEl = null;
        };
    }
    qMenuEl.querySelector("[data-grid-pip]").onclick = () => openGridOverlay();
    if (sa && tileIsScreen(t)) {
        qMenuEl.querySelector('a[data-sa="mute"]').onclick = () => {
            V().shareAudioCtl.setMuted(t.clientID, !sa.muted);
            qMenuEl.remove();
            qMenuEl = null;
        };
        const slider = qMenuEl.querySelector(".ctx-volume input");
        slider.oninput = () => {
            qMenuEl.querySelector(".ctx-vol-pct").textContent = slider.value + "%";
            slider.setAttribute("aria-valuetext", slider.value + "%");
            V().shareAudioCtl.setVolume(t.clientID, parseInt(slider.value, 10));
        };
    }
    document.body.appendChild(qMenuEl);
    const close = () => { if (qMenuEl) { qMenuEl.remove(); qMenuEl = null; } };
    setTimeout(() => {
        document.addEventListener("click", close, { once: true });
        document.addEventListener("keydown", (e) => { if (e.key === "Escape") close(); }, { once: true });
    });
}

async function openGridOverlay() {
    if (!document.pictureInPictureEnabled || tiles.size === 0) {
        V().sysMsg("floating grid is unavailable");
        return;
    }
    stopGridOverlay();
    try {
        const compositor = new GridCompositor();
        const canvas = document.createElement("canvas");
        canvas.width = 1280;
        canvas.height = 720;
        canvas.className = "grid-composite-canvas";
        document.body.appendChild(canvas);
        const output = document.createElement("video");
        output.muted = true;
        output.playsInline = true;
        output.srcObject = canvas.captureStream(15);
        output.className = "grid-composite-video";
        document.body.appendChild(output);
        await output.play();
        const target = canvas.getContext("bitmaprenderer");
        let drawing = false;
        const draw = async () => {
            if (drawing) return;
            drawing = true;
            try {
                target.transferFromImageBitmap(await compositor.compose([...tiles.values()].map((tile) => tile.video)));
            } finally { drawing = false; }
        };
        await draw();
        compositeTimer = setInterval(draw, 1000 / 15);
        compositeVideo = output;
        output.onleavepictureinpicture = stopGridOverlay;
        await output.requestPictureInPicture();
    } catch (err) {
        stopGridOverlay();
        V().sysMsg("floating grid failed: " + (err.message || err));
    }
}

function stopGridOverlay() {
    if (compositeTimer) clearInterval(compositeTimer);
    compositeTimer = null;
    if (compositeVideo) {
        compositeVideo.srcObject?.getTracks().forEach((track) => track.stop());
        compositeVideo.remove();
    }
    compositeVideo = null;
    document.querySelector(".grid-composite-canvas")?.remove();
}

// startStatsPoll refreshes the per-tile layer badge from getStats (63,
// best-effort): inbound-rtp frameWidth maps f/h/q to HD/MD/LD.
function startStatsPoll() {
    if (statsTimer) return;
    statsTimer = setInterval(refreshBadges, 3000);
}

function stopStatsPoll() {
    if (statsTimer) {
        clearInterval(statsTimer);
        statsTimer = null;
    }
}

// STALL_POLLS is how many consecutive polls without a newly decoded frame
// count as "the camera is off" (61). Two polls ≈ 6 s, long enough to survive a
// freeze/keyframe gap and short enough that a camera-off does not linger.
const STALL_POLLS = 2;

async function refreshBadges() {
    for (const tile of tiles.values()) updatePreviewAge(tile);
    const { state } = V();
    if (!state.pc || tiles.size === 0 || statsPollRequest) return;
    const request = { pc: state.pc, scope: captureMediaScope() };
    statsPollRequest = request;
    const current = () => statsPollRequest === request && state.pc === request.pc && mediaScopeIsCurrent(request.scope);
    try {
        const stats = await request.pc.getStats();
        if (!current()) return;
        const cpu = await window.go.main.App.SystemCPUPercent();
        if (!current()) return;
        const byTrack = new Map(); // trackIdentifier -> {w, frames}
        const decoders = [], encoders = [];
        let availableIncomingBitrate = 0;
        stats.forEach((r) => {
            if (r.type === "inbound-rtp" && (r.kind === "video" || r.mediaType === "video")) {
                byTrack.set(r.trackIdentifier, { w: r.frameWidth || 0, frames: r.framesDecoded || 0 });
                if (r.framesDecoded > 0) decoders.push(r);
            }
            if (r.type === "outbound-rtp" && r.kind === "video" && r.active !== false && r.framesEncoded > 0) encoders.push(r);
            if (r.type === "candidate-pair" && (r.nominated || r.selected) && r.state === "succeeded") {
                availableIncomingBitrate = Math.max(availableIncomingBitrate, r.availableIncomingBitrate || 0);
            }
        });
        if (availableIncomingBitrate > 0 && qualityPref === "auto") {
            const next = availableIncomingBitrate < 700000 ? "low" : availableIncomingBitrate < 2500000 ? "mid" : "high";
            if (next !== autoNetworkQuality) {
                autoNetworkQuality = next;
                pushQuality();
            }
        }
        // Restore only after sustained headroom. A single below-threshold
        // reading must not trigger another simulcast/keyframe switch.
        let nextPressure = cpuPressure;
        if (!Number.isFinite(cpu) || cpu < 0 || cpu > 100) {
            cpuRecoverySince = null;
        } else if (cpu >= 85) {
            nextPressure = true;
            cpuRecoverySince = null;
        } else if (cpuPressure && cpu < 70) {
            cpuRecoverySince ??= performance.now();
            if (performance.now() - cpuRecoverySince >= 15000) {
                nextPressure = false;
                cpuRecoverySince = null;
            }
        } else {
            cpuRecoverySince = null;
        }
        cpuPressure = nextPressure;
        // Honor the runtime's power-efficient path (normally hardware-backed)
        // instead of reacting to other applications' CPU use. Missing stats
        // retain the software fallback; this flag is not proof of a GPU codec.
        const nextReceive = cpuPressure && !(decoders.length && decoders.every(r => r.powerEfficientDecoder === true));
        const publishingVideo = encoders.length > 0 || request.pc.getSenders().some(sender => sender.track?.kind === "video" && sender.track.readyState !== "ended");
        const nextSend = cpuPressure && publishingVideo && !(encoders.length && encoders.every(r => r.powerEfficientEncoder === true));
        if (nextReceive !== receiveCpuPressure || nextSend !== sendCpuPressure) {
            const hadPressure = receiveCpuPressure || sendCpuPressure;
            if ((nextReceive || nextSend) !== hadPressure) {
                V().sysMsg(nextReceive || nextSend ? `CPU ${cpu.toFixed(0)}% — reducing software video resolution` : "Video processing recovered — restoring resolution");
            }
        }
        if (nextReceive !== receiveCpuPressure) {
            receiveCpuPressure = nextReceive;
            lastSentQuality = "";
            pushQuality();
        }
        if (nextSend !== sendCpuPressure) {
            sendCpuPressure = nextSend;
            applySendCaps();
        }
        for (const t of tiles.values()) {
            const s = (t.track && byTrack.get(t.track.id)) || null;
            // (61) a camera switched off mid-call keeps its receiver track
            // "live" and unmuted in WebView2 — onmute never fires — so without
            // this the tile would freeze on a stale frame for the rest of the
            // call. Screen shares are exempt: a still desktop legitimately
            // encodes nothing, and so does a tile paused by low-bandwidth
            // mode (88), which is meant to hold its last frame.
            const exempt = tileIsScreen(t) || t.video.paused;
            if (!s || exempt) {
                t.stalls = 0;
                t.flowing = true;
            } else if (s.frames > t.frames) {
                t.frames = s.frames;
                t.stalls = 0;
                t.flowing = true;
            } else if (++t.stalls >= STALL_POLLS) {
                t.flowing = false;
            }
            updateTileVideo(t); // WebView2 does not always fire track mute/unmute
            const w = s ? s.w : 0;
            if (!w || !t.flowing) {
                t.badgeEl.classList.add("hidden");
                continue;
            }
            t.badgeEl.classList.remove("hidden");
            t.badgeEl.textContent = w >= 1280 ? "HD" : w >= 640 ? "MD" : "LD";
        }
    } catch { if (current()) cpuRecoverySince = null; }
    finally { if (statsPollRequest === request) statsPollRequest = null; }
}

// ---------------------------------------------------------------------------
// Low-bandwidth mode (88): cap own video send to 150 kbps (single layer),
// request the low simulcast layer, pause tile rendering, persist the toggle.
// ---------------------------------------------------------------------------

let lowBandwidth = false;

// LOW_BW_BITRATE is the send ceiling of the mode; a screen share must not lift
// it (88), so the share preset caps are clamped to it while the mode is on.
const LOW_BW_BITRATE = 150000;
const LOW_BW_HOURLY_MB = Math.ceil(LOW_BW_BITRATE * 60 * 60 / 8 / 1000000);
const LOW_BW_ESTIMATE_ID = "voice-lowbw-estimate";

function syncLowBandwidthButton() {
    gridEl().dataset.lowbwLabel = tLabel("voice.lowBandwidth");
    const btn = V().$("voice-lowbw");
    if (!btn) return;
    const description = tLabel("voice.lowBandwidthHelp", { count: LOW_BW_HOURLY_MB });
    btn.title = tLabel("voice.lowBandwidth") + " — " + description;
    btn.setAttribute("aria-description", description);
    btn.classList.toggle("active", lowBandwidth);
    btn.setAttribute("aria-pressed", String(lowBandwidth));
    let badge = document.getElementById(LOW_BW_ESTIMATE_ID);
    if (!badge) {
        badge = document.createElement("span");
        badge.id = LOW_BW_ESTIMATE_ID;
        badge.className = "lowbw-estimate";
        btn.insertAdjacentElement("afterend", badge);
    }
    badge.textContent = tLabel("voice.lowBandwidthEstimate", { count: LOW_BW_HOURLY_MB });
    badge.title = description;
    badge.setAttribute("aria-label", description);
    badge.classList.toggle("active", lowBandwidth);
    btn.setAttribute("aria-describedby", LOW_BW_ESTIMATE_ID);
}

export function isLowBandwidth() { return lowBandwidth; }

// setLowBandwidth toggles the mode; persist=true saves it into settings.
export async function setLowBandwidth(on, persist) {
    lowBandwidth = on;
    syncLowBandwidthButton();
    gridEl().classList.toggle("lowbw", on);
    lastSentQuality = ""; // force re-push with the new effective quality
    pushQuality();
    for (const t of tiles.values()) {
        // (88) same rule as the decode handler: a tile with nothing decoded yet
        // keeps playing until its first frame lands and pauses on it there, so
        // every tile ends up frozen on a picture rather than on an avatar.
        if (on) { if (t.video.readyState >= 2) t.video.pause(); }
        else t.video.play().catch(() => {});
        updatePreviewAge(t);
    }
    applySendCaps();
    if (persist) {
        const s = Object.assign({}, V().state.settings, { low_bandwidth: on });
        const err = await window.go.main.App.SaveSettings(s);
        // (282) re-read rather than caching the copy we sent: the Go side owns
        // fields the frontend never has (recents, what's-new marker).
        if (!err) V().state.settings = await window.go.main.App.GetSettings();
    }
}

// videoSender returns the sender of a video transceiver this client can send
// on, or null. Matches by transceiver so it never confuses the share-audio
// sender, and skips recvonly ones: with no camera there is no send transceiver
// at all and replaceTrack on a server-side recvonly would publish nothing.
function videoSenderFor(pc) {
    if (!pc) return null;
    const t = pc.getTransceivers().find((t) =>
        t !== V().state.shareVideoTransceiver &&
        (t.receiver.track?.kind === "video" || t.sender.track?.kind === "video") &&
        (t.direction === "sendrecv" || t.direction === "sendonly"));
    return t ? t.sender : null;
}

function videoSender() {
    return videoSenderFor(V().state.pc);
}

const capUpdates = new WeakMap();
const pendingVideoSenders = new WeakMap();
const videoLimitUpdates = new WeakMap();
const capturePreferences = new WeakMap();

// Capture/encoder phase only. The caller owns native-cache fetching and must
// rebuild the server peer (restoring authorized controls) after dimension changes.
// A rejection stops this peer's video/display audio; the caller must reset its
// voice session. Null means a newer update or session owns the result.
export function applyVideoLimits(limits, stillRelevant = () => true) {
    const { state } = V();
    const pc = state.pc;
    const scope = captureMediaScope();
    if (!pc || !stillRelevant()) return Promise.resolve(null);
    const previous = videoLimitUpdates.get(pc);
    const fields = ["video_max_width", "video_max_height", "video_max_bitrate"];
    const changed = fields.some(key => (state.mediaLimits?.[key] || 0) !== (limits?.[key] || 0));
    if (!changed && !previous) return Promise.resolve({ changed: false, dimensionsChanged: false });
    const dimensionsChanged = !!previous?.dimensionsChanged || fields.slice(0, 2).some(key =>
        (state.mediaLimits?.[key] || 0) !== (limits?.[key] || 0));
    const update = { dimensionsChanged, paused: previous?.paused || new Map() };
    videoLimitUpdates.set(pc, update);
    state.mediaLimits = { ...limits };
    // Server limits survive channel movement on the same voice peer. Only its
    // server/session owner changes invalidate capture work, not the channel.
    const current = () => state.pc === pc && state.activeTabID === scope.tabID && state.serverGeneration === scope.generation &&
        state.sessionGeneration === scope.session && state.myClientID === scope.clientID &&
        stillRelevant() && videoLimitUpdates.get(pc) === update;
    const videoTracks = () => new Set([
        ...(state.localStream?.getVideoTracks() || []), ...(state.shareStream?.getVideoTracks() || []),
        ...pc.getSenders().map(sender => sender.track).filter(track => track?.kind === "video"),
    ].filter(track => track.readyState !== "ended"));
    const pause = track => {
        if (!update.paused.has(track)) update.paused.set(track, track.enabled);
        track.enabled = false;
    };
    // Pause immediately, including captures already waiting for the offer queue.
    if (dimensionsChanged) for (const track of videoTracks()) pause(track);
    return queuePeerNegotiation(pc, async () => {
        try {
            if (!current()) return null;
            if (dimensionsChanged) {
                for (const track of videoTracks()) {
                    pause(track);
                    const settings = track.getSettings();
                    const preferred = capturePreferences.get(track) || {
                        width: settings.width || 640, height: settings.height || 360, fps: settings.frameRate || 30,
                    };
                    await track.applyConstraints({ ...track.getConstraints(),
                        ...videoConstraints(preferred.width, preferred.height, preferred.fps, state.mediaLimits) });
                    if (!current()) return null;
                    // A user can end a source while constraints are pending.
                    if (track.readyState !== "ended" && !trackFitsVideoLimits(track, state.mediaLimits)) {
                        throw new Error(tLabel("voice.mediaDimensionsFailed"));
                    }
                }
            }
            await applySendCaps(current);
            if (!current()) return null;
            for (const [track, enabled] of update.paused) {
                if (track.readyState !== "ended") track.enabled = enabled;
            }
            syncCameraButton();
            return { changed: true, dimensionsChanged };
        } catch (error) {
            if (!current()) return null;
            for (const track of videoTracks()) track.stop();
            for (const track of state.shareStream?.getTracks() || []) track.stop();
            throw error;
        } finally {
            if (videoLimitUpdates.get(pc) === update) videoLimitUpdates.delete(pc);
        }
    });
}

// Serialize read/modify/write updates so a late mode change cannot restore an
// old budget after a camera or share starts. Only live tracks share the budget.
function applySendCaps(stillRelevant = () => true) {
    const { state } = V();
    const pc = state.pc;
    if (!pc) return Promise.resolve();
    const generation = state.serverGeneration;
    const current = () => state.pc === pc && state.serverGeneration === generation && stillRelevant();
    const pending = (capUpdates.get(pc) || Promise.resolve()).catch(() => {}).then(async () => {
        if (!current()) return;
        const screenSender = state.shareVideoTransceiver?.sender;
        const sources = [videoSenderFor(pc), screenSender].filter(sender => sender &&
            (sender === pendingVideoSenders.get(pc) || (sender.track && sender.track.readyState !== "ended"))).map(sender => {
            const parameters = sender.getParameters();
            return { sender, parameters, encodings: parameters.encodings || [],
                preset: sender === screenSender ? sharePresetBitrate || Infinity : Infinity };
        });
        capVideoEncodings(sources, state.mediaLimits, lowBandwidth ? LOW_BW_BITRATE : sendCpuPressure ? 500000 : 0);
        for (const { sender, parameters, encodings } of sources) {
            if (!current()) return;
            if (encodings.length) await sender.setParameters(parameters);
        }
    });
    capUpdates.set(pc, pending);
    pending.catch(() => { if (current()) V().sysMsg(tLabel("voice.mediaCapsFailed")); });
    return pending;
}

function mediaLimitHint() {
    const limits = V().state.mediaLimits;
    const parts = [];
    if (limits?.video_max_width) parts.push(tLabel("voice.mediaDimensions", { width: limits.video_max_width, height: limits.video_max_height }));
    if (limits?.video_max_bitrate) parts.push(tLabel("voice.mediaBitrate", { bitrate: limits.video_max_bitrate / 1000 }));
    return parts.join(" ");
}

// sharePresetBitrate is the active share's preset ceiling (72), 0 when idle.
// applySendCaps needs it so leaving low-bandwidth
// mode mid-share restores the preset instead of uncapping the share.
let sharePresetBitrate = 0;

// ---------------------------------------------------------------------------
// Screen share (69-72, 85)
// ---------------------------------------------------------------------------

// Share quality presets (72): applied via getDisplayMedia constraints plus a
// sender maxBitrate cap.
const SHARE_PRESETS = {
    text: { width: 1920, height: 1080, fps: 15, bitrate: 2500000 },
    balanced: { width: 1280, height: 720, fps: 30, bitrate: 1500000 },
    motion: { width: 1280, height: 720, fps: 60, bitrate: 2500000 },
};

// (71) Chromium puts cropTo on BrowserCaptureMediaStreamTrack, a SUBCLASS of
// MediaStreamTrack — probing the base prototype reports "unsupported" on every
// browser that actually has the API. The base check stays as a fallback for
// builds that shipped it there.
const regionSupported = typeof CropTarget !== "undefined" && (
    (typeof BrowserCaptureMediaStreamTrack !== "undefined" &&
        "cropTo" in BrowserCaptureMediaStreamTrack.prototype) ||
    (typeof MediaStreamTrack !== "undefined" && "cropTo" in MediaStreamTrack.prototype));

let shareDialogID = 0;

function syncShareButton() {
    const btn = V().$("voice-screen");
    if (!btn) return;
    const sharing = !!V().state.screenSharing;
    btn.disabled = !!V().state.shareStarting || !!V().state.shareStopping;
    const label = sharing ? tLabel("voice.stopShare") : tLabel("voice.startShare");
    btn.classList.toggle("active", sharing);
    btn.setAttribute("aria-pressed", String(sharing));
    btn.setAttribute("aria-label", label);
    btn.title = label;
    labelButton(btn, "screen", sharing ? tLabel("voice.stopShare") : tLabel("voice.share"));
}

// shareToggle is the voice-screen button handler: stop when sharing
// (confirming first, 85), otherwise open the share dialog.
export async function shareToggle() {
    const { state } = V();
    if (state.shareStarting || state.shareStopping) return;
    if (state.screenSharing) {
        await confirmStopShare();
        return;
    }
    openShareDialog();
}

function openShareDialog() {
    const dialogID = ++shareDialogID;
    const sourceName = `shsrc-${dialogID}`;
    const qualityID = `share-quality-${dialogID}`;
    const audioID = `share-audio-${dialogID}`;
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `
        <div class="dlg share-dlg">
            <h3>${tLabel("polish.shareTitle")}</h3>
            <fieldset class="share-sources">
                <legend class="dlg-label">${tLabel("polish.source")}</legend>
                <label><input type="radio" name="${sourceName}" value="monitor" checked /> ${tLabel("polish.monitor")}</label>
                <label><input type="radio" name="${sourceName}" value="window" /> ${tLabel("polish.window")}</label>
                <label title="${tLabel(regionSupported ? "polish.cropHelp" : "polish.cropUnavailable")}">
                    <input type="radio" name="${sourceName}" value="region" ${regionSupported ? "" : "disabled"} /> ${tLabel("polish.region")}
                </label>
            </fieldset>
            <label class="dlg-label" for="${qualityID}">${tLabel("polish.preset")}</label>
            <select class="dlg-input sh-preset" id="${qualityID}">
                <option value="balanced">${tLabel("polish.presetBalanced")}</option>
                <option value="text">${tLabel("polish.presetText")}</option>
                <option value="motion">${tLabel("polish.presetMotion")}</option>
            </select>
            <label class="share-audio" for="${audioID}"><input type="checkbox" class="sh-audio" id="${audioID}" /> ${tLabel("polish.systemAudio")}</label>
            <div class="dlg-buttons">
                <button class="dlg-ok">${tLabel("voice.startShare")}</button>
                <button class="dlg-cancel">${tLabel("polish.cancel")}</button>
            </div>
        </div>`;
    overlay.querySelector(".dlg-cancel").onclick = () => overlay.remove();
    overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
    overlay.querySelector(".dlg-ok").onclick = async () => {
        if (!isCurrentServerDialog(overlay)) return;
        const surface = overlay.querySelector(`input[name="${sourceName}"]:checked`).value;
        const preset = overlay.querySelector(".sh-preset").value;
        const withAudio = overlay.querySelector(".sh-audio").checked;
        overlay.remove();
        const { state } = V();
        if (state.shareStarting || state.shareStopping || state.screenSharing) return;
        const request = {};
        state.shareStarting = request;
        syncShareButton();
        try { await startShare({ surface, preset, withAudio }); }
        finally {
            if (state.shareStarting === request) { state.shareStarting = null; syncShareButton(); }
        }
    };
    mountServerDialog(overlay);
    const hint = mediaLimitHint();
    if (hint) {
        const note = document.createElement("p");
        note.className = "set-hint media-limit-hint";
        note.textContent = hint;
        overlay.querySelector(".sh-preset").after(note);
    }
    if (!regionSupported) {
        // (71) the radio is disabled, so the dialog has to say why and what to
        // do instead — a disabled control with no explanation reads as a bug.
        const note = document.createElement("div");
        note.className = "set-hint warn";
        note.textContent = tLabel("polish.cropNeedsUpdate");
        overlay.querySelector(".share-sources").appendChild(note);
    }
}

// startShare captures the display (69: displaySurface preference — Chromium's
// picker ultimately decides what is shareable), publishes a dedicated screen
// track independently of the camera, optionally merges display
// audio (70), and applies the quality preset (72).
async function startShare({ surface, preset, withAudio }) {
    const { state } = V();
    const generation = state.serverGeneration;
    const tabID = state.activeTabID;
    const peerConnection = state.pc;
    if (!peerConnection) return;
    const shareScope = captureMediaScope();
    const current = () => mediaScopeIsCurrent(shareScope) && state.serverGeneration === generation && state.activeTabID === tabID && state.pc === peerConnection;
    let shareTransceiver = null;
    const discardDisplay = (stream) => {
        stream?.getTracks().forEach((track) => track.stop());
        shareTransceiver?.stop();
        if (state.shareVideoTransceiver === shareTransceiver) state.shareVideoTransceiver = null;
        if (stream?.getTracks().includes(state.shareAudioSender?.track)) {
            state.shareAudioTransceiver?.stop();
            state.shareAudioTransceiver = null;
            state.shareAudioSender = null;
        }
    };
    const p = SHARE_PRESETS[preset] || SHARE_PRESETS.balanced;
    const limits = state.mediaLimits;
    const video = videoConstraints(p.width, p.height, p.fps, limits);
    if (surface !== "region") video.displaySurface = surface; // 69: "monitor" | "window"
    const gdm = { video, audio: !!withAudio };
    if (surface === "region") {
        // (71) Region Capture only applies to a self-capture of this app's own
        // surface. Without these the picker hands back a monitor/window track
        // and cropTo() rejects it, so the region path could never succeed.
        gdm.preferCurrentTab = true;
        gdm.selfBrowserSurface = "include";
    }
    let display;
    try {
        display = await navigator.mediaDevices.getDisplayMedia(gdm);
    } catch (e) {
        if (!current()) return;
        if (withAudio) {
            // (70) WebView2 may refuse display audio (works on Windows for
            // screen/tab shares) — retry video-only and say so.
            V().sysMsg("system audio not available for this share (" + (e.message || e.name) + "); sharing video only");
            try {
                display = await navigator.mediaDevices.getDisplayMedia(Object.assign({}, gdm, { audio: false }));
                withAudio = false;
            } catch (e2) {
                if (!current()) return;
                V().sysMsg("screen capture failed: " + (e2.message || e2.name));
                return;
            }
        } else {
            V().sysMsg("screen capture failed: " + (e.message || e.name));
            return;
        }
    }
    if (!current()) {
        discardDisplay(display);
        return;
    }
    const screenTrack = display.getVideoTracks()[0];
    if (!screenTrack) {
        discardDisplay(display);
        V().sysMsg("screen capture produced no video track");
        return;
    }
    capturePreferences.set(screenTrack, p);
    screenTrack.contentHint = preset === "text" ? "detail" : "motion";
    let shareEnded = false;
    screenTrack.onended = () => {
        shareEnded = true;
        if (!current() || (state.shareStream && state.shareStream !== display)) { discardDisplay(display); return; }
        if (state.screenSharing) {
            doStopShare();
            return;
        }
        if (state.shareStream === display) state.shareStream = null;
        discardDisplay(display);
        clearRegionBox();
    };

    if (surface === "region") {
        const ok = await pickRegionAndCrop(screenTrack, current);
        if (!ok || !current()) {
            discardDisplay(display);
            return; // cancelled
        }
    }

    if (!trackFitsVideoLimits(screenTrack, state.mediaLimits)) {
        discardDisplay(display);
        clearRegionBox();
        V().sysMsg(tLabel("voice.mediaDimensionsFailed"));
        return;
    }
    state.shareStream = display;
    sharePresetBitrate = p.bitrate;
    if (peerConnection) {
        // The dedicated screen transceiver survives publication stops.
        try {
            await queuePeerNegotiation(peerConnection, async () => {
                if (!current() || shareEnded) throw new DOMException("capture ended", "AbortError");
                try {
                    if (!trackFitsVideoLimits(screenTrack, state.mediaLimits)) throw new Error(tLabel("voice.mediaDimensionsFailed"));
                    shareTransceiver = state.shareVideoTransceiver;
                    if (shareTransceiver) await shareTransceiver.sender.replaceTrack(screenTrack);
                    else shareTransceiver = peerConnection.addTransceiver(screenTrack, { direction: "sendonly", streams: [display] });
                    state.shareVideoTransceiver = shareTransceiver;
                    await applySendCaps();
                    if (!current() || shareEnded) throw new DOMException("capture ended", "AbortError");
                    if (!trackFitsVideoLimits(screenTrack, state.mediaLimits)) throw new Error(tLabel("voice.mediaDimensionsFailed"));
                    await negotiateOffer(peerConnection, generation, undefined, () => !shareEnded, tabID);
                } catch (error) {
                    // Remove the candidate before the next queued offer can see it.
                    discardDisplay(display);
                    throw error;
                }
            });
            if (!current()) {
                discardDisplay(display);
                return;
            }
        } catch (e) {
            if (!current()) {
                discardDisplay(display);
                return;
            }
            V().sysMsg("publishing screen share failed: " + (e.message || e.name));
            // a rollback only drops transceivers created by applying a remote
            // description, so this one survives with its direction flip and
            // would be re-offered — stop it to keep the dead m-line out of
            // videoSender()'s reach.
            discardDisplay(display);
            state.shareStream = null;
            sharePresetBitrate = 0;
            applySendCaps();
            clearRegionBox(); // (71) nothing is being cropped after this
            return;
        }
    }
    // (70) merge display audio as a second published audio track (the server
    // fans out every publisher audio track) + renegotiate.
    const displayAudio = withAudio ? display.getAudioTracks()[0] : null;
    if (displayAudio && peerConnection) {
        if (!state.shareAudioTransceiver) {
            let tr = null;
            try {
                // addTrack would recycle one of the server's recvonly audio
                // m-lines and publish the share on a subscriber slot, so take a
                // dedicated sendonly transceiver.
                tr = peerConnection.addTransceiver(displayAudio, { direction: "sendonly", streams: [display] });
                state.shareAudioTransceiver = tr;
                state.shareAudioSender = tr.sender;
                await renegotiate(peerConnection, generation);
                if (!current()) {
                    discardDisplay(display);
                    return;
                }
            } catch (e) {
                if (!current()) {
                    discardDisplay(display);
                    return;
                }
                V().sysMsg("publishing share audio failed: " + (e.message || e.name));
                // a rollback keeps this transceiver, so without the stop() the
                // dead track is re-offered on the next renegotiation.
                try { tr?.stop(); } catch { /* nothing left to stop */ }
                displayAudio.stop();
                state.shareAudioTransceiver = null;
                state.shareAudioSender = null;
            }
        } else {
            try {
                state.shareAudioSender = state.shareAudioTransceiver.sender;
                await state.shareAudioSender.replaceTrack(displayAudio);
                await renegotiate(peerConnection, generation);
                if (!current()) {
                    discardDisplay(display);
                    return;
                }
            } catch (e) {
                if (!current()) {
                    discardDisplay(display);
                    return;
                }
                V().sysMsg("publishing share audio failed: " + (e.message || e.name));
                displayAudio.stop();
            }
        }
    }

    if (!current() || shareEnded || screenTrack.readyState === "ended") {
        if (state.shareStream === display) state.shareStream = null;
        discardDisplay(display);
        clearRegionBox();
        return;
    }
    state.screenSharing = true;
    let shareErr;
    try { if (!await startPublication("screen", screenTrack, () => {
        if (state.shareStream?.getVideoTracks()[0] === screenTrack) void doStopShare();
    })) shareErr = "capture ended"; }
    catch (error) { shareErr = String(error); }
    if (!current()) {
        discardDisplay(display);
        return;
    }
    if (shareErr) {
        V().sysMsg("screen share permission failed: " + shareErr);
        await doStopShare();
        return;
    }
    syncShareButton();
}

// pickRegionAndCrop shows a draggable/resizable box over the app; on confirm
// the track is cropped to it via the Region Capture API (71). The box then
// STAYS in the document: a crop target with no layout box produces no frames,
// so removing it would blank the share. It is torn down in doStopShare.
// The crop itself lives on the MediaStreamTrack, so it survives the
// renegotiation that publishes the share (and any later one).
let cancelPendingRegion = null;

function pickRegionAndCrop(track, isCurrent = () => true) {
    return new Promise((resolve) => {
        cancelPendingRegion?.();
        const box = document.createElement("div");
        box.className = "region-box";
        box.innerHTML = `
            <div class="region-title">drag to move · drag corner to resize</div>
            <button class="region-ok">Crop &amp; share</button>
            <button class="region-cancel">Cancel</button>`;
        document.body.appendChild(box);
        let settled = false;
        const finish = (value) => {
            if (settled) return;
            settled = true;
            if (cancelPendingRegion === cancel) cancelPendingRegion = null;
            resolve(value);
        };
        const cancel = () => {
            box.remove();
            finish(false);
        };
        cancelPendingRegion = cancel;

        // Drag by the title bar; resize via CSS resize handle.
        const title = box.querySelector(".region-title");
        title.onpointerdown = (e) => {
            const startX = e.clientX - box.offsetLeft;
            const startY = e.clientY - box.offsetTop;
            const move = (ev) => {
                box.style.left = Math.max(0, ev.clientX - startX) + "px";
                box.style.top = Math.max(0, ev.clientY - startY) + "px";
            };
            const up = () => {
                document.removeEventListener("pointermove", move);
                document.removeEventListener("pointerup", up);
            };
            document.addEventListener("pointermove", move);
            document.addEventListener("pointerup", up);
        };

        box.querySelector(".region-cancel").onclick = cancel;
        box.querySelector(".region-ok").onclick = async () => {
            if (settled || !isCurrent()) {
                cancel();
                return;
            }
            try {
                const target = await CropTarget.fromElement(box);
                await track.cropTo(target);
                if (settled || !isCurrent()) {
                    cancel();
                    return;
                }
                // the box is inside its own crop rect from now on, so it has to
                // stop painting anything: .cropping leaves a transparent hole
                // and marks the region with an outline, which is drawn outside
                // the border box and therefore outside the captured rect.
                box.innerHTML = "";
                box.classList.add("cropping");
                V().state.regionBox = box;
                finish(true);
            } catch (e) {
                if (!settled && isCurrent()) V().sysMsg("region capture failed: " + (e.message || e.name));
                cancel();
            }
        };
    });
}

// clearRegionBox drops the persistent crop target (71).
export function clearRegionBox() {
    const cancel = cancelPendingRegion;
    cancelPendingRegion = null;
    cancel?.();
    const { state } = V();
    if (state.regionBox) {
        state.regionBox.remove();
        state.regionBox = null;
    }
}

// receiverCount is the set of users the SFU could be forwarding my video to:
// it only fans a publisher out to peers in the publisher's own channel.
function receiverCount() {
    const { state } = V();
    if (!state.myChannelID) return 0;
    return state.clients.filter((c) =>
        c.channel_id === state.myChannelID && c.client_id !== state.myClientID).length;
}

// videoIsBeingDecoded turns "may be watching" into evidence (85). The router
// relays subscriber PLI/FIR/NACK back to the publisher, so a non-zero count on
// my video sender means a subscriber's decoder really is consuming this
// stream; zero only means nobody had to ask, hence the softer wording.
async function videoIsBeingDecoded(what) {
    const sender = what === "screen" ? V().state.shareVideoTransceiver?.sender : videoSender();
    if (!sender || !V().state.pc) return false;
    try {
        const stats = await sender.getStats();
        let n = 0;
        stats.forEach((r) => {
            if (r.type === "outbound-rtp" && (r.kind === "video" || r.mediaType === "video")) {
                n += (r.pliCount || 0) + (r.firCount || 0) + (r.nackCount || 0);
            }
        });
        return n > 0;
    } catch {
        return false;
    }
}

// confirmStopPublish (85) asks before cutting a stream others receive; it
// stops straight away when nobody in the channel can be receiving it.
async function confirmStopPublish({ heading, what, stopLabel, keepLabel, onStop }) {
    const { state } = V();
    const generation = state.serverGeneration;
    const peerConnection = state.pc;
    const n = receiverCount();
    if (n === 0) {
        onStop();
        return;
    }
    const decoded = await videoIsBeingDecoded(what);
    if (state.serverGeneration !== generation || state.pc !== peerConnection) return;
    const text = tLabel(decoded ? "polish.decoders" : "polish.receivers", {
        count: n, kind: tLabel(what === "screen" ? "polish.screen" : "polish.camera"),
    });
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `
        <div class="dlg share-dlg">
            <h3>${heading}</h3>
            <p class="dlg-text">${text}</p>
            <div class="dlg-buttons">
                <button class="dlg-ok">${stopLabel}</button>
                <button class="dlg-cancel">${keepLabel}</button>
            </div>
        </div>`;
    overlay.querySelector(".dlg-ok").onclick = () => {
        overlay.remove();
        onStop();
    };
    overlay.querySelector(".dlg-cancel").onclick = () => overlay.remove();
    overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
    mountServerDialog(overlay);
}

function confirmStopShare() {
    return confirmStopPublish({
        heading: tLabel("polish.stopShare"),
        what: "screen",
        stopLabel: tLabel("voice.stopShare"),
        keepLabel: tLabel("polish.keepShare"),
        onStop: () => doStopShare(),
    });
}

// doStopShare stops only display tracks; camera publication is independent.
async function doStopShare() {
    const { state } = V();
    if (!state.screenSharing) return;
    const pc = state.pc;
    const generation = state.serverGeneration;
    const tabID = state.activeTabID;
    const current = () => state.pc === pc && state.serverGeneration === generation && state.activeTabID === tabID;
    state.shareStopping = true;
    state.screenSharing = false;
    syncShareButton();
    if (state.shareAudioSender && state.pc) {
        // The transceiver stays (stopping the track silences it); removing it
        // would need another renegotiation.
        state.shareAudioSender.track?.stop();
        state.shareAudioSender.replaceTrack(null).catch(() => {});
        state.shareAudioSender = null;
    }
    const screenSender = state.shareVideoTransceiver?.sender;
    if (screenSender) void screenSender.replaceTrack(null).catch(() => {});
    if (state.shareStream) {
        state.shareStream.getTracks().forEach((t) => t.stop());
        state.shareStream = null;
    }
    clearRegionBox(); // (71)
    sharePresetBitrate = 0;
    applySendCaps();
    syncCameraButton();
    try {
        await stopPublication("screen");
        if (pc && current()) await renegotiate(pc, generation);
    } catch (error) {
        if (current()) V().sysMsg("stopping screen share failed: " + (error.message || error.name));
    } finally {
        if (current()) { state.shareStopping = false; syncShareButton(); }
    }
}

// ---------------------------------------------------------------------------
// Camera capture is session-local and starts only from an explicit action.
// ---------------------------------------------------------------------------

let cameraOff = true;
let cameraRequest = null;

export function cameraConstraints(settings, limits) {
    if (limits?.video_max_width) return videoConstraints(640, 360, settings?.camera_fps || 30, limits);
    return { width: 640, height: 360, frameRate: { ideal: settings?.camera_fps || 30 } };
}

// syncCameraButton reflects camera availability and state in the voice bar.
function syncCameraButton() {
    const { state, $ } = V();
    const cam = state.localStream?.getVideoTracks()[0] || null;
    const btn = $("voice-video");
    if (!btn) return;
    btn.disabled = !state.pc || !!cameraRequest;
    btn.classList.toggle("active", !!cam && !cameraOff);
    btn.setAttribute("aria-pressed", String(!!cam && !cameraOff));
    btn.title = !cam || cameraOff ? tLabel("voice.cameraEnable") : tLabel("voice.cameraDisable");
    const hint = mediaLimitHint();
    if (hint) btn.title += ". " + hint;
    const enabled = !!cam && !cameraOff;
    labelButton(btn, enabled ? "camera" : "cameraOff", enabled ? tLabel("voice.cameraOn") : tLabel("voice.cameraOff"));
    btn.setAttribute("aria-label", btn.title);
    $("local-video").classList.toggle("hidden", !cam || cameraOff);
    const preview = $("local-video");
    const stream = enabled ? state.localStream : null;
    if (preview.srcObject !== stream) {
        preview.srcObject = stream;
        renderMicStatus($("mic-status"), state.micState, V().retryMicrophoneAccess, enabled, $("ptt-btn"));
    }
}

// resetCameraState clears the toggle for a fresh (or ended) voice session.
export function resetCameraState() {
    cameraOff = true;
    cameraRequest = null;
    sharePresetBitrate = 0;
    syncCameraButton();
    syncShareButton();
}

// cameraToggle is the voice-video button handler.
export async function cameraToggle() {
    const { state } = V();
    if (!state.pc || !state.localStream || cameraRequest) return;
    const cam = state.localStream?.getVideoTracks()[0] || null;
    if (!cam || cameraOff) {
        await startCamera();
        return;
    }
    await confirmStopPublish({
        heading: tLabel("polish.stopCamera"),
        what: "camera",
        stopLabel: tLabel("polish.turnOff"),
        keepLabel: tLabel("polish.keepOn"),
        onStop: stopCamera,
    });
}

async function stopCamera() {
    const { state } = V();
    const scope = captureMediaScope();
    const cam = state.localStream?.getVideoTracks()[0] || null;
    cameraOff = true;
    if (cam) {
        cam.stop();
        state.localStream.removeTrack(cam);
    }
    const vs = videoSender();
    if (vs) void vs.replaceTrack(null).catch(() => {});
    applySendCaps();
    syncCameraButton();
    try { await stopPublication("cam", cam); }
    catch (error) { if (mediaScopeIsCurrent(scope)) V().sysMsg(tLabel("streams.failed", { error: String(error) })); }
}

async function startCamera() {
    const { state } = V();
    const pc = state.pc;
    const localStream = state.localStream;
    const generation = state.serverGeneration;
    const tabID = state.activeTabID;
    const request = {};
    const cameraScope = captureMediaScope();
    cameraRequest = request;
    const current = () => mediaScopeIsCurrent(cameraScope) && cameraRequest === request && state.pc === pc &&
        state.localStream === localStream && state.serverGeneration === generation && state.activeTabID === tabID;
    syncCameraButton();
    let stream, sender, transceiver;
    try {
        stream = await navigator.mediaDevices.getUserMedia({ audio: false, video: cameraConstraints(state.settings, state.mediaLimits) });
        if (!current()) return;
        const cam = stream.getVideoTracks()[0];
        if (!cam) throw new Error("no camera track available");
        capturePreferences.set(cam, { width: 640, height: 360, fps: state.settings?.camera_fps || 30 });
        if (!trackFitsVideoLimits(cam, state.mediaLimits)) throw new Error(tLabel("voice.mediaDimensionsFailed"));
        cam.contentHint = "motion";
        // Teardown owns capture immediately, even while another offer is pending.
        localStream.addTrack(cam);
        await queuePeerNegotiation(pc, async () => {
            if (!current()) return;
            try {
                if (!trackFitsVideoLimits(cam, state.mediaLimits)) throw new Error(tLabel("voice.mediaDimensionsFailed"));
                sender = videoSenderFor(pc);
                if (!sender) {
                    try {
                        transceiver = pc.addTransceiver(cam, {
                            direction: "sendrecv", streams: [localStream],
                            sendEncodings: [{ rid: "q", scaleResolutionDownBy: 4 }, { rid: "h", scaleResolutionDownBy: 2 }, { rid: "f" }],
                        });
                    } catch {
                        transceiver = pc.addTransceiver(cam, { direction: "sendrecv", streams: [localStream] });
                    }
                    sender = transceiver.sender;
                }
                pendingVideoSenders.set(pc, sender);
                await applySendCaps();
                if (!current()) throw new DOMException("capture ended", "AbortError");
                if (!trackFitsVideoLimits(cam, state.mediaLimits)) throw new Error(tLabel("voice.mediaDimensionsFailed"));
                await sender.replaceTrack(cam);
                await negotiateOffer(pc, generation, undefined, current, tabID);
            } catch (error) {
                // A failed candidate must disappear before the queue advances.
                cam.stop();
                localStream.removeTrack(cam);
                if (sender?.track === cam) await sender.replaceTrack(null).catch(() => {});
                transceiver?.stop?.();
                throw error;
            } finally {
                pendingVideoSenders.delete(pc);
            }
        });
        if (!current()) return;
        if (!await startPublication("cam", cam, () => {
            if (state.localStream?.getVideoTracks()[0] === cam) void stopCamera();
        }) || !current()) return;
        cameraOff = false;
        cam.onended = () => { if (state.localStream === localStream) void stopCamera(); };
    } catch (error) {
        if (current()) V().sysMsg("camera unavailable: " + (error.message || error.name));
    } finally {
        if (!current() || cameraOff) {
            for (const track of stream?.getTracks() || []) {
                track.stop();
                localStream.removeTrack(track);
            }
            if (sender && stream?.getTracks().includes(sender.track)) await sender.replaceTrack(null).catch(() => {});
            transceiver?.stop?.();
            if (current()) applySendCaps();
        }
        if (cameraRequest === request) {
            cameraRequest = null;
            syncCameraButton();
        }
    }
}

// trackSlots declares which slot each outbound track occupies (70). The router
// carries one source per slot, so the share's system audio has to be named or
// it contends with the microphone for the default audio slot and one of the
// two is dropped for the rest of the session. The declaration REPLACES the
// previous one, so it must be complete on EVERY offer — including the ICE
// restart, which would otherwise tear the share's output track off every
// subscriber exactly when the network is already unstable.
export function trackSlots(sdp) {
    const { state } = V();
    if (!state.pc) return [];
    const shareAudio = state.shareAudioSender?.track || null;
    const shareVideo = state.shareVideoTransceiver?.sender.track || null;
    const negotiatedIDs = new Map();
    for (const section of String(sdp || "").split(/(?=^m=)/m)) {
        const mid = section.match(/^a=mid:([^\r\n]+)/m)?.[1];
        const trackID = section.match(/^a=msid:[^\s]+ ([^\r\n\s]+)/m)?.[1];
        if (mid && trackID) negotiatedIDs.set(mid, trackID);
    }
    const out = [];
    for (const s of state.pc.getSenders()) {
        const t = s.track;
        if (!t) continue;
        const transceiver = state.pc.getTransceivers().find(item => item.sender === s);
        const trackID = negotiatedIDs.get(transceiver?.mid) || (negotiatedIDs.size === 0 ? t.id : "");
        if (trackID) out.push({ track_id: trackID, slot: t === shareAudio ? SLOT_SCREEN_AUDIO : t === shareVideo ? SLOT_SCREEN : t.kind === "audio" ? "mic" : "cam" });
    }
    return out;
}

// renegotiate runs a client-initiated offer/answer round (used after adding a
// share track); the server treats re-offers idempotently. A failed round is
// rolled back to "stable": without that the pc stays in have-local-offer and
// every later renegotiation dies with InvalidStateError.
const peerNegotiations = new WeakMap();

// All local offers and remote answers share one queue per peer. A failed round
// cannot roll back a different camera, screen or ICE negotiation.
export function queuePeerNegotiation(peerConnection, operation) {
    const previous = peerNegotiations.get(peerConnection) || Promise.resolve();
    const pending = previous.catch(() => {}).then(operation);
    peerNegotiations.set(peerConnection, pending);
    const finished = () => { if (peerNegotiations.get(peerConnection) === pending) peerNegotiations.delete(peerConnection); };
    pending.then(finished, finished);
    return pending;
}

function remoteICEIdentity(sdp) {
    const usernames = new Set();
    const passwords = new Set();
    for (const line of String(sdp || "").split(/\r?\n/)) {
        if (line.startsWith("a=ice-ufrag:")) usernames.add(line.slice(12));
        if (line.startsWith("a=ice-pwd:")) passwords.add(line.slice(10));
    }
    if (!usernames.size || !passwords.size) return null;
    return JSON.stringify([[...usernames].sort(), [...passwords].sort()]);
}

// Ignore offers from a transport superseded by a reconnect or ICE restart.
// Compare ICE credentials after any earlier local round finishes.
const pendingRemoteOffers = new WeakMap();
export function answerRemoteOffer(peerConnection, generation, offerSDP) {
    const tabID = V().state.activeTabID;
    const pending = { generation, tabID, offerSDP };
    pendingRemoteOffers.set(peerConnection, pending);
    return queuePeerNegotiation(peerConnection, () => applyPendingRemoteOffer(peerConnection, pending));
}

async function applyPendingRemoteOffer(peerConnection, pending) {
        if (pendingRemoteOffers.get(peerConnection) !== pending) return false;
        pendingRemoteOffers.delete(peerConnection);
        const { generation, tabID, offerSDP } = pending;
        const current = () => V().state.pc === peerConnection && V().state.serverGeneration === generation && V().state.activeTabID === tabID;
        const identity = remoteICEIdentity(peerConnection.remoteDescription?.sdp);
        if (!current() || !identity || identity !== remoteICEIdentity(offerSDP)) return false;
        await peerConnection.setRemoteDescription({ type: "offer", sdp: offerSDP });
        if (!current()) return;
        const answer = await peerConnection.createAnswer();
        if (!current()) return;
        await peerConnection.setLocalDescription(answer);
        if (!current()) return;
        await window.go.main.App.WebRTCAnswerForTab(tabID, answer.sdp);
        return current();
}

export function renegotiate(peerConnection = V().state.pc, generation = V().state.serverGeneration, offerOptions, stillRelevant = () => true) {
    if (!peerConnection) return Promise.reject(new DOMException("voice session ended", "AbortError"));
    const tabID = V().state.activeTabID;
    return queuePeerNegotiation(peerConnection, () => negotiateOffer(peerConnection, generation, offerOptions, stillRelevant, tabID));
}

async function negotiateOffer(peerConnection, generation, offerOptions, stillRelevant, tabID) {
    const current = () => V().state.pc === peerConnection && V().state.serverGeneration === generation && V().state.activeTabID === tabID && stillRelevant();
    const ensureCurrent = () => {
        if (!current()) throw new DOMException("server session changed", "AbortError");
    };
    for (let attempt = 0; ; attempt++) {
      try {
        ensureCurrent();
        const offer = await peerConnection.createOffer(offerOptions);
        ensureCurrent();
        await peerConnection.setLocalDescription(offer);
        ensureCurrent();
        const answerSDP = await window.go.main.App.WebRTCOfferForTab(tabID, offer.sdp, trackSlots(offer.sdp));
        ensureCurrent();
        await peerConnection.setRemoteDescription({ type: "answer", sdp: answerSDP });
        ensureCurrent();
        return;
      } catch (e) {
        await peerConnection?.setLocalDescription({ type: "rollback" }).catch(() => {});
        if (!current() || attempt >= 2 || !String(e).includes("webrtc negotiation collision")) throw e;
        // The server's offer may still be crossing the native event bridge.
        // Consume it under our queue ownership so its queued callback cannot
        // deadlock this retry or apply the same offer twice.
        for (let wait = 0; wait < 20 && current() && !pendingRemoteOffers.has(peerConnection); wait++) {
            await new Promise(resolve => setTimeout(resolve, 25));
        }
        ensureCurrent();
        const pending = pendingRemoteOffers.get(peerConnection);
        if (!pending || !await applyPendingRemoteOffer(peerConnection, pending)) throw e;
      }
    }
}
