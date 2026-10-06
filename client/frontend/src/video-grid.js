import { escapeHTML as escapeTranslation } from "./markdown.js";
// video-grid.js — received stream tiles, quality selection and diagnostics.
import { copyToClipboard } from "./clipboard.js";
import { formatBitrate, summarizeStream } from "./connection-stats.js";
import { renderStreamPath } from "./stream-path-diagnostics.js";
import { GridCompositor } from "./grid-compositor.js";
import { captureMediaScope, mediaScopeIsCurrent } from "./media-controls.js";
import { closeContextMenu, mountContextMenu, contextMenuKey } from "./context-menu.js";
import { setSafeImage } from "./safe-media.js";
import { t as tLabel, currentLanguage } from "./i18n.js";
import { SLOT_SCREEN, parseTrackID } from "./media-track-id.js";
import { streamQualityTarget, streamUsesSourceQuality } from "./stream-controls.js";
import { allocateReceiveQuality, settleAutoQuality } from "./receive-quality.js";
import { samplePeerStats } from "./peer-stats.js";
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

    // Legacy servers retain their explicitly labelled connection-wide control.
    // New servers apply each tile's preference to its current watched publication.
    let qualityPref = "auto"; // auto | high | mid | low
    let lastSentQuality = "";
    let qualityRequest = null;
    let cpuPressure = false;
    let receiveCpuPressure = false;
    let cpuRecoverySince = null;
    let statsPollRequest = null;
    let autoNetworkQuality = "high";
    let statsTimer = null;
    let compositeTimer = null;
    let compositeVideo = null;
    let qualityCapability = null;
    let incomingBudget = null;

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
                    <span class="vtile-preview-age hidden"></span>
                </div>
                <span class="vtile-badge hidden"></span>
                <details class="vtile-diagnostics"><summary></summary><div class="vtile-diagnostics-body"><p class="vtile-codec"></p><p class="vtile-rate"></p><p class="vtile-received"></p><p class="vtile-quality"></p><p class="vtile-quality-error" role="status"></p><div class="vtile-stream-path"></div><p class="vtile-traffic-note"></p><button class="vtile-copy-diagnostics" type="button"></button></div></details>
                <button class="vtile-quality-button" type="button"></button>
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
                key, qualityPreference: "auto",
            };
            el.querySelector(".vtile-quality-button").onclick = event => { event.stopPropagation(); void openTileMenu(undefined, undefined, t); };
            el.querySelector(".vtile-diagnostics").onclick = event => event.stopPropagation();
            el.querySelector(".vtile-diagnostics").ontoggle = () => { void refreshRemoteDiagnostics(t); };
            el.querySelector(".vtile-copy-diagnostics").onclick = () => {
                const sample = currentReceiverDiagnostics(t);
                const sourceQuality = streamUsesSourceQuality(t.clientID, t.slot);
                const payload = {
                    sampled_at: t.diagnosticsAt || null, direction: "received", slot: t.slot,
                    codec: sample?.codec ?? null, bitrate_bits_per_second: sample?.bitrate ?? null,
                    bandwidth: formatBitrate(sample?.bitrate), payload_bytes_received: sample?.bytes ?? null,
                    width: sample?.width ?? null, height: sample?.height ?? null,
                    frames_decoded: sample?.frames ?? null, frames_per_second: sample?.fps ?? null, packets_lost: sample?.packetsLost ?? null,
                    requested_quality: sourceQuality ? "source" : qualityCapability?.supported === false ? qualityPref : t.qualityPreference,
                    confirmed_preference: sourceQuality ? "source" : (qualityCapability?.supported === false ? lastSentQuality : t.sentQuality) || null,
                    receiver: sample ? { sample_ms: sample.sampleMS, received_fps: sample.receivedFPS, decoded_fps: sample.fps,
                        dropped_fps: sample.droppedFPS, decode_ms: sample.decodeMS, jitter_buffer_ms: sample.bufferMS,
                        recent_loss_percent: sample.lossPercent, nacks: sample.nacks, plis: sample.plis, freezes: sample.freezes,
                        key_frames: sample.keyFrames, decoder: sample.decoder, power_efficient_decoder: sample.powerEfficientDecoder } : null,
                    stream_path: t.remoteDiagnostics || null,
                    stream_path_age_ms: remoteDiagnosticsAge(t),
                    note: "Browser bitrate is RTP payload, including retransmissions. Server byte counts include RTP headers. Stage sample windows are independent; server FPS counts observed RTP timestamps, not complete or decoded frames. Null means unavailable.",
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
        t.diagnosticsMeasuredAt = null;
        t.remoteDiagnostics = null;
        t.remoteDiagnosticsAt = null;
        t.remoteDiagnosticsRequest = null;
        t.remoteDiagnosticsStatus = "";
        t.remoteDiagnosticsError = "";
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
        void pushQuality();
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
        else void pushQuality();
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
        cpuRecoverySince = null;
        qualityCapability = null;
        incomingBudget = null;
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

    function currentReceiverDiagnostics(tile) {
        if (tile.diagnostics && tile.diagnosticsMeasuredAt != null && performance.now() - tile.diagnosticsMeasuredAt > 15000) {
            tile.diagnostics = null;
            tile.diagnosticsPC = null;
            tile.diagnosticsAt = null;
            tile.diagnosticsMeasuredAt = null;
            tile.badgeEl.classList.add("hidden");
        }
        return tile.diagnostics;
    }

    function renderStreamDiagnostics(tile) {
        currentReceiverDiagnostics(tile);
        const panel = tile.el.querySelector(".vtile-diagnostics");
        panel.querySelector("summary").textContent = tLabel("wins.streamDetails");
        panel.querySelector(".vtile-codec").textContent = tLabel("wins.streamCodec", { codec: tile.diagnostics?.codec || "—" });
        panel.querySelector(".vtile-rate").textContent = tLabel("wins.streamRate", { rate: formatBitrate(tile.diagnostics?.bitrate) });
        const sample = tile.diagnostics;
        const dimensions = sample?.width > 0 && sample?.height > 0 ? `${sample.width} × ${sample.height}` : "—";
        const fps = Number.isFinite(sample?.fps) ? sample.fps.toFixed(1) : "—";
        panel.querySelector(".vtile-received").textContent = tLabel("streams.received", { dimensions, fps });
        const legacy = qualityCapability?.supported === false;
        const sourceQuality = streamUsesSourceQuality(tile.clientID, tile.slot);
        const preference = legacy ? qualityPref : tile.qualityPreference;
        const label = tLabel(`polish.video${preference[0].toUpperCase()}${preference.slice(1)}`);
        panel.querySelector(".vtile-quality").textContent = sourceQuality ? tLabel("wins.sourceQuality") : tLabel("streams.qualityState", { preference: label, applied: (legacy ? lastSentQuality : tile.sentQuality) || "—" });
        panel.querySelector(".vtile-quality-error").textContent = sourceQuality ? "" : tile.qualityError || "";
        const qualityButton = tile.el.querySelector(".vtile-quality-button");
        qualityButton.hidden = sourceQuality;
        qualityButton.textContent = preference === "auto" ? tLabel("streams.auto") : label;
        qualityButton.title = tLabel(legacy ? "polish.videoQualityHelp" : "streams.qualityHelp");
        qualityButton.setAttribute("aria-label", tLabel(legacy ? "streams.legacyQualityControl" : "streams.qualityControl"));
        panel.querySelector(".vtile-traffic-note").textContent = tLabel("wins.payloadNote");
        panel.querySelector("button").textContent = tLabel("wins.copyStream");
        renderStreamPath(panel.querySelector(".vtile-stream-path"), tile.remoteDiagnostics, sample, {
            extraAgeMS: remoteDiagnosticsAge(tile) || 0, quality: tile.sentQuality || "high",
            status: tile.remoteDiagnosticsStatus, error: tile.remoteDiagnosticsError,
        });
    }

    function remoteDiagnosticsAge(tile) {
        return tile.remoteDiagnosticsAt == null ? null : Math.max(0, performance.now() - tile.remoteDiagnosticsAt);
    }

    async function refreshRemoteDiagnostics(tile) {
        if (!tile.el.querySelector(".vtile-diagnostics").open || tile.remoteDiagnosticsRequest) return;
        const scope = captureMediaScope(), pc = V().state.pc;
        const target = streamQualityTarget(tile.clientID, tile.slot);
        if (!pc) return;
        const identity = JSON.stringify(target), track = tile.track;
        if (tile.remoteDiagnosticsIdentity !== identity) {
            tile.remoteDiagnostics = null;
            tile.remoteDiagnosticsAt = null;
            tile.remoteDiagnosticsIdentity = identity;
        }
        const request = {};
        tile.remoteDiagnosticsRequest = request;
        const current = () => tile.remoteDiagnosticsRequest === request && V().state.pc === pc && mediaScopeIsCurrent(scope) &&
            tiles.get(tile.key) === tile && tile.track === track && JSON.stringify(streamQualityTarget(tile.clientID, tile.slot)) === identity;
        tile.remoteDiagnosticsStatus = tile.remoteDiagnostics ? "" : "loading";
        tile.remoteDiagnosticsError = "";
        renderStreamDiagnostics(tile);
        try {
            const api = window.go.main.App;
            const supported = await api.SupportsStreamDiagnosticsForTab?.(scope.tabID);
            if (!current()) return;
            if (!supported || !target || !api.StreamDiagnosticsForTab) {
                tile.remoteDiagnostics = null;
                tile.remoteDiagnosticsAt = null;
                tile.remoteDiagnosticsStatus = "unsupported";
                return;
            }
            const reply = await api.StreamDiagnosticsForTab(scope.tabID, target.publisherID, target.slot, target.generation, target.session);
            if (!current()) return;
            if (!reply || reply.publisher_id !== target.publisherID || reply.slot !== target.slot ||
                reply.generation !== target.generation || reply.session !== target.session) throw new Error(tLabel("wins.path.scopeMismatch"));
            tile.remoteDiagnostics = reply;
            tile.remoteDiagnosticsAt = performance.now();
            tile.remoteDiagnosticsStatus = "";
        } catch (error) {
            if (!current()) return;
            tile.remoteDiagnostics = null;
            tile.remoteDiagnosticsAt = null;
            tile.remoteDiagnosticsStatus = "";
            tile.remoteDiagnosticsError = error?.message || String(error);
        } finally {
            const render = current();
            if (tile.remoteDiagnosticsRequest === request) tile.remoteDiagnosticsRequest = null;
            if (render) renderStreamDiagnostics(tile);
        }
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
        t.el.dataset.streamState = track?.readyState === "ended" ? "ended"
            : !live || t.video.readyState < 2 ? "waiting"
            : lowBandwidth && t.video.paused ? "paused"
            : !t.flowing ? "stalled" : "live";
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
        if (lowBandwidth || receiveCpuPressure) return "low";
        if (qualityPref !== "auto") return qualityPref;
        const viewQuality = focusedID ? "high" : "mid";
        const rank = { low: 0, mid: 1, high: 2 };
        return rank[autoNetworkQuality] < rank[viewQuality] ? autoNetworkQuality : viewQuality;
    }

    // pushQuality sends MsgVideoQuality when the effective layer changed.
    async function pushLegacyQuality() {
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

    async function supportsIndependentQuality() {
        const pc = V().state.pc, scope = captureMediaScope();
        if (!pc) return false;
        if (qualityCapability?.pc === pc && mediaScopeIsCurrent(qualityCapability.scope)) return qualityCapability.promise;
        const capability = { pc, scope };
        qualityCapability = capability;
        capability.promise = Promise.resolve().then(() => window.go.main.App.SupportsStreamVideoQualityForTab?.(scope.tabID) ?? false)
            .then(value => { capability.supported = value === true; return capability.supported; })
            .catch(() => { if (qualityCapability === capability) qualityCapability = null; return false; });
        return capability.promise;
    }

    async function pushQuality(immediate = true) {
        const pc = V().state.pc, scope = captureMediaScope();
        let adaptable = [...tiles.values()].filter(tile => !streamUsesSourceQuality(tile.clientID, tile.slot));
        if (tiles.size && !adaptable.length) {
            for (const tile of tiles.values()) renderStreamDiagnostics(tile);
            return;
        }
        const independent = await supportsIndependentQuality();
        if (V().state.pc !== pc || !mediaScopeIsCurrent(scope)) return;
        adaptable = [...tiles.values()].filter(tile => !streamUsesSourceQuality(tile.clientID, tile.slot));
        if (!adaptable.length) return;
        if (!independent) {
            await pushLegacyQuality();
            if (V().state.pc === pc && mediaScopeIsCurrent(scope)) for (const tile of tiles.values()) renderStreamDiagnostics(tile);
            return;
        }
        const streams = adaptable.map(tile => ({ key: tile.key, preference: tile.qualityPreference, slot: tile.slot,
            highBitrate: tile.estimatedHighBitrate, bitrate: tile.diagnostics?.bitrate }));
        const qualities = allocateReceiveQuality(streams, { availableBitrate: incomingBudget, focusedID,
            cpuPressure: receiveCpuPressure, lowBandwidth });
        await Promise.all(adaptable.map(async tile => {
            const target = streamQualityTarget(tile.clientID, tile.slot);
            if (!target) return;
            const identity = JSON.stringify(target);
            const desired = qualities.get(tile.key);
            tile.autoQuality = settleAutoQuality(tile.autoQuality, desired, immediate || tile.qualityPreference !== "auto" || lowBandwidth);
            const quality = tile.autoQuality.applied;
            tile.desiredQuality = quality;
            if (tile.qualityRequest || (tile.sentQuality === quality && tile.qualityIdentity === identity)) return;
            const request = { pc, scope, identity, quality };
            tile.qualityRequest = request;
            const current = () => V().state.pc === pc && mediaScopeIsCurrent(scope) && tiles.get(tile.key) === tile &&
                JSON.stringify(streamQualityTarget(tile.clientID, tile.slot)) === identity;
            let error;
            try { error = await window.go.main.App.SetStreamVideoQualityForTab(scope.tabID, target.publisherID, target.slot, target.generation, target.session, quality); }
            catch (failure) { error = String(failure); }
            if (tile.qualityRequest === request) tile.qualityRequest = null;
            if (!current()) return;
            tile.qualityError = error ? tLabel("streams.qualityFailed", { error }) : "";
            if (!error) { tile.sentQuality = quality; tile.qualityIdentity = identity; }
            renderStreamDiagnostics(tile);
            if (tile.desiredQuality !== quality) void pushQuality(false);
        }));
    }

    // openTileMenu is the tile right-click menu: the shared receive-quality
    // preference (63) plus, when the tile's publisher is sharing system audio,
    // that share's own volume/mute (70).
    async function openTileMenu(x, y, t) {
        const scope = captureMediaScope();
        const independent = await supportsIndependentQuality();
        if (!mediaScopeIsCurrent(scope) || tiles.get(t.key) !== t) return;
        const qMenuEl = document.createElement("div");
        qMenuEl.className = "ctx-menu";
        const cur = independent ? t.qualityPreference : qualityPref;
        const sourceQuality = streamUsesSourceQuality(t.clientID, t.slot);
        const sa = V().shareAudioCtl?.get(t.clientID) || null;
        qMenuEl.innerHTML = (sourceQuality ? "" : `
            <a data-q="auto">${cur === "auto" ? "✓ " : ""}${tLabel("polish.videoAuto")}</a>
            <a data-q="high">${cur === "high" ? "✓ " : ""}${tLabel("polish.videoHigh")}</a>
            <a data-q="mid">${cur === "mid" ? "✓ " : ""}${tLabel("polish.videoMid")}</a>
            <a data-q="low">${cur === "low" ? "✓ " : ""}${tLabel("polish.videoLow")}</a>
            <div class="ctx-divider"></div>`) + `
            <a data-stream-details>${tLabel("wins.streamDetails")}</a>
            <a class="ctx-note">${tLabel(sourceQuality ? "wins.sourceQuality" : independent ? "streams.qualityHelp" : "polish.videoQualityHelp")}</a>
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
                if (independent) t.qualityPreference = a.dataset.q;
                else qualityPref = a.dataset.q;
                renderStreamDiagnostics(t);
                void pushQuality();
                closeContextMenu(qMenuEl, true);
            };
        }
        qMenuEl.querySelector("[data-grid-pip]").onclick = () => { closeContextMenu(qMenuEl, true); void openGridOverlay(); };
        qMenuEl.querySelector("[data-stream-details]").onclick = () => {
            closeContextMenu(qMenuEl, true);
            const panel = t.el.querySelector(".vtile-diagnostics");
            panel.open = true;
            panel.querySelector("summary").focus();
            void refreshRemoteDiagnostics(t);
        };
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
        for (const tile of tiles.values()) {
            updatePreviewAge(tile);
            const hadSample = !!tile.diagnostics;
            if (hadSample && !currentReceiverDiagnostics(tile)) renderStreamDiagnostics(tile);
            void refreshRemoteDiagnostics(tile);
        }
        const { state } = V();
        if (!state.pc || tiles.size === 0 || statsPollRequest) return;
        const request = { pc: state.pc, scope: captureMediaScope() };
        statsPollRequest = request;
        const current = () => statsPollRequest === request && state.pc === request.pc && mediaScopeIsCurrent(request.scope);
        try {
            const stats = await samplePeerStats(request.pc);
            if (!current()) return;
            for (const tile of tiles.values()) {
                tile.diagnostics = summarizeStream(stats, tile.track?.id, tile.diagnosticsPC === request.pc ? tile.diagnostics : null);
                tile.diagnosticsPC = request.pc;
                tile.diagnosticsAt = new Date().toISOString();
                tile.diagnosticsMeasuredAt = performance.now();
                if (tile.sentQuality === "high" && Number.isFinite(tile.diagnostics.bitrate) && tile.diagnostics.bitrate > 0) {
                    const estimate = Math.max(tile.slot === "screen" ? 1000000 : 250000, Math.min(50000000, tile.diagnostics.bitrate));
                    tile.estimatedHighBitrate = tile.estimatedHighBitrate ? tile.estimatedHighBitrate * 0.7 + estimate * 0.3 : estimate;
                }
                renderStreamDiagnostics(tile);
            }
            const cpu = await window.go.main.App.SystemCPUPercent();
            if (!current()) return;
            const byTrack = new Map(); // trackIdentifier -> {w, frames}
            const decoders = [];
            let availableIncomingBitrate = 0;
            stats.forEach((r) => {
                if (r.type === "inbound-rtp" && (r.kind === "video" || r.mediaType === "video")) {
                    byTrack.set(r.trackIdentifier, { w: r.frameWidth || 0, frames: r.framesDecoded || 0 });
                    if (r.framesDecoded > 0) decoders.push(r);
                }
                if (r.type === "candidate-pair" && (r.nominated || r.selected) && r.state === "succeeded") {
                    availableIncomingBitrate = Math.max(availableIncomingBitrate, r.availableIncomingBitrate || 0);
                }
            });
            incomingBudget = availableIncomingBitrate > 0 ? availableIncomingBitrate : null;
            if (availableIncomingBitrate > 0 && qualityPref === "auto") {
                const next = availableIncomingBitrate < 700000 ? "low" : availableIncomingBitrate < 2500000 ? "mid" : "high";
                if (next !== autoNetworkQuality) {
                    autoNetworkQuality = next;
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
            const canAdapt = [...tiles.values()].some(tile => !streamUsesSourceQuality(tile.clientID, tile.slot));
            const nextReceive = canAdapt && cpuPressure && !(decoders.length && decoders.every(r => r.powerEfficientDecoder === true));
            // Encoding already adapts to its own CPU budget in WebRTC. System
            // load from a game must not impose a second bitrate/resolution cut.
            if (nextReceive !== receiveCpuPressure) {
                V().sysMsg(tLabel(nextReceive ? "streams.autoCPU" : "streams.autoRecovered"));
                receiveCpuPressure = nextReceive;
                lastSentQuality = "";
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
                const sample = t.diagnostics;
                t.badgeEl.textContent = `${w} × ${sample?.height || "—"} · ${Number.isFinite(sample?.fps) ? Math.round(sample.fps) : "—"} fps`;
            }
            void pushQuality(false);
        } catch { if (current()) cpuRecoverySince = null; }
        finally { if (statsPollRequest === request) statsPollRequest = null; }
    }

    // ---------------------------------------------------------------------------
    // Low-bandwidth mode (88): cap own video send to 150 kbps (single layer),
    // request the low simulcast layer, pause tile rendering, persist the toggle.
    // ---------------------------------------------------------------------------

    let lowBandwidth = false;

    // Camera and screen share split this user-selected send ceiling.
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
            updateTileVideo(t);
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

    return { videoTrackAdded, videoTrackRemoved, videoSpeaking, videoRefreshNames, clearVideoGrid, initVideo, isLowBandwidth, setLowBandwidth, get lowBandwidth() { return lowBandwidth; }, lowBandwidthBitrate: LOW_BW_BITRATE };
}
