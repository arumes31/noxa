import { escapeHTML as escapeTranslation } from "./markdown.js";
// video-grid.js — received stream tiles, quality selection and diagnostics.
import { copyToClipboard } from "./clipboard.js";
import { formatBitrate, summarizeStream } from "./connection-stats.js";
import { GridCompositor } from "./grid-compositor.js";
import { captureMediaScope, mediaScopeIsCurrent } from "./media-controls.js";
import { closeContextMenu, mountContextMenu, contextMenuKey } from "./context-menu.js";
import { setSafeImage } from "./safe-media.js";
import { t as tLabel, currentLanguage } from "./i18n.js";
import { SLOT_SCREEN, parseTrackID } from "./media-track-id.js";
const V = () => window.__noxa;

export function createVideoGrid({ applySendCaps, syncCameraButton, syncShareButton }) {
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
    function videoTrackAdded(trackID, stream, publisher, watchControls) {
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
                <details class="vtile-diagnostics"><summary></summary><div class="vtile-diagnostics-body"><p class="vtile-codec"></p><p class="vtile-rate"></p><p class="vtile-traffic-note"></p><button class="vtile-copy-diagnostics" type="button"></button></div></details>
                <button class="vtile-pip icon-btn" title="${escapeTranslation(tLabel("desktop.floating.always.on.top.video"))}">▣</button>
                <button class="vtile-fullscreen icon-btn" title="${escapeTranslation(tLabel("desktop.fullscreen.esc.exits"))}">⛶</button>`;
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
            el.tabIndex = 0;
            el.setAttribute("aria-haspopup", "menu");
            el.onkeydown = e => {
                if (!contextMenuKey(e)) return;
                e.preventDefault(); e.stopPropagation();
                openTileMenu(undefined, undefined, t);
            };
            t = {
                el, video, nameEl, kindEl, badgeEl, track: null, stream: null,
                clientID: clid, slot: parsed.slot, frames: 0, stalls: 0, flowing: true,
            };
            el.querySelector(".vtile-diagnostics").onclick = event => event.stopPropagation();
            el.querySelector(".vtile-copy-diagnostics").onclick = () => {
                const sample = t.diagnostics;
                const payload = {
                    sampled_at: t.diagnosticsAt || null, direction: "received", slot: t.slot,
                    codec: sample?.codec ?? null, bitrate_bits_per_second: sample?.bitrate ?? null,
                    bandwidth: formatBitrate(sample?.bitrate), payload_bytes_received: sample?.bytes ?? null,
                    width: sample?.width ?? null, height: sample?.height ?? null,
                    frames_decoded: sample?.frames ?? null, packets_lost: sample?.packetsLost ?? null,
                    note: "RTP media payload only; excludes network headers. Null means unavailable.",
                };
                void copyToClipboard(JSON.stringify(payload, null, 2), { success: tLabel("wins.streamCopied"), isCurrent: () => el.isConnected });
            };
            tiles.set(key, t);
            gridEl().appendChild(el);
        }
        t.clientID = clid;
        t.watchControls = watchControls || null;
        if (watchControls) t.el.append(watchControls);
        t.el.dataset.clid = clid;
        // The receiver supplies a private, single-track stream. Its browser ID
        // can differ from the negotiated publisher/slot key after MID reuse.
        const tracks = stream?.getVideoTracks() || [];
        const vt = tracks.length === 1 ? tracks[0] : tracks.find(track => track.id === key) || null;
        t.track = vt;
        t.diagnostics = null;
        t.diagnosticsPC = null;
        t.diagnosticsAt = null;
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
    function videoTrackRemoved(trackID) {
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
    function videoSpeaking(clientID, speaking) {
        const clid = String(clientID);
        for (const t of tiles.values()) {
            if (t.clientID === clid) t.el.classList.toggle("speaking", speaking);
        }
    }

    // videoRefreshNames re-resolves nickname/avatar on all tiles (user joined,
    // moved, avatar changed, started/stopped sharing).
    function videoRefreshNames() {
        for (const t of tiles.values()) { applyTileIdentity(t); updateTileVideo(t); updatePreviewAge(t); }
    }

    // clearVideoGrid removes all tiles (voice teardown).
    function clearVideoGrid() {
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
        renderStreamDiagnostics(t);
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

    function renderStreamDiagnostics(tile) {
        const panel = tile.el.querySelector(".vtile-diagnostics");
        panel.querySelector("summary").textContent = tLabel("wins.streamDetails");
        panel.querySelector(".vtile-codec").textContent = tLabel("wins.streamCodec", { codec: tile.diagnostics?.codec || "—" });
        panel.querySelector(".vtile-rate").textContent = tLabel("wins.streamRate", { rate: formatBitrate(tile.diagnostics?.bitrate) });
        panel.querySelector(".vtile-traffic-note").textContent = tLabel("wins.payloadNote");
        panel.querySelector("button").textContent = tLabel("wins.copyStream");
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

    function initVideo() {
        window.addEventListener("noxa-camera-preferences-changed", syncCameraButton);
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
    function setIdleQualityOverride(on) {
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
            V().sysMsg(tLabel("desktop.video.quality.failed") + err);
        }
    }

    // openTileMenu is the tile right-click menu: the shared receive-quality
    // preference (63) plus, when the tile's publisher is sharing system audio,
    // that share's own volume/mute (70).
    function openTileMenu(x, y, t) {
        const qMenuEl = document.createElement("div");
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
        for (const a of qMenuEl.querySelectorAll("a[data-q]")) {
            a.onclick = () => {
                qualityPref = a.dataset.q;
                pushQuality();
                closeContextMenu(qMenuEl, true);
            };
        }
        qMenuEl.querySelector("[data-grid-pip]").onclick = () => { closeContextMenu(qMenuEl, true); void openGridOverlay(); };
        if (sa && tileIsScreen(t)) {
            qMenuEl.querySelector('a[data-sa="mute"]').onclick = () => {
                V().shareAudioCtl.setMuted(t.clientID, !sa.muted);
                closeContextMenu(qMenuEl, true);
            };
            const slider = qMenuEl.querySelector(".ctx-volume input");
            slider.oninput = () => {
                qMenuEl.querySelector(".ctx-vol-pct").textContent = slider.value + "%";
                slider.setAttribute("aria-valuetext", slider.value + "%");
                V().shareAudioCtl.setVolume(t.clientID, parseInt(slider.value, 10));
            };
        }
        mountContextMenu(qMenuEl, { x, y, trigger: t.el });
    }

    async function openGridOverlay() {
        if (!document.pictureInPictureEnabled || tiles.size === 0) {
            V().sysMsg(tLabel("desktop.floating.grid.is.unavailable"));
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
            V().sysMsg(tLabel("desktop.floating.grid.failed") + (err.message || err));
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
            for (const tile of tiles.values()) {
                tile.diagnostics = summarizeStream(stats, tile.track?.id, tile.diagnosticsPC === request.pc ? tile.diagnostics : null);
                tile.diagnosticsPC = request.pc;
                tile.diagnosticsAt = new Date().toISOString();
                renderStreamDiagnostics(tile);
            }
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
                    V().sysMsg(nextReceive || nextSend ? tLabel("runtime.cpuReduction", { cpu: cpu.toFixed(0) }) : tLabel("desktop.video.processing.recovered.restoring.resolution"));
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

    function isLowBandwidth() { return lowBandwidth; }

    // setLowBandwidth toggles the mode; persist=true saves it into settings.
    async function setLowBandwidth(on, persist) {
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

    return { videoTrackAdded, videoTrackRemoved, videoSpeaking, videoRefreshNames, clearVideoGrid, initVideo, setIdleQualityOverride, isLowBandwidth, setLowBandwidth, get lowBandwidth() { return lowBandwidth; }, get sendCpuPressure() { return sendCpuPressure; }, lowBandwidthBitrate: LOW_BW_BITRATE };
}
