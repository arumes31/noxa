import { escapeHTML as escapeTranslation } from "./markdown.js";
// video-publication.js — camera/screen capture and publication lifecycle.
import { captureCamera, applyCameraPreview } from "./camera-capture.js";
import { captureMediaScope, mediaScopeIsCurrent } from "./media-controls.js";
import { startPublication, stopPublication, publicationSnapshot } from "./stream-publication.js";
import { startShareStatus, stopShareStatus, refreshShareStatus } from "./share-status.js";
import { displayAudioOptions, validateDisplayAudio } from "./display-audio.js";
import { shareQuality, screenShareConstraints, createShareQualityControls } from "./screen-share-quality.js";
import { isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { labelButton } from "./icons.js";
import { renderMicStatus } from "./audio.js";
import { videoConstraints, trackFitsVideoLimits, capVideoEncodings } from "./media-limits.js";
import { t as tLabel } from "./i18n.js";
import { renegotiate, negotiateOffer, queuePeerNegotiation } from "./video-negotiation.js";
const V = () => window.__noxa;
// An unset maxBitrate invokes Chromium's ~2.5 Mbps default for larger frames.
// Match the relay's 50 Mbps congestion-controller ceiling; this is headroom,
// not a target or minimum. Actual throughput still follows network feedback.
const SCREEN_BITRATE_HEADROOM = 50000000;

export function createVideoPublication({ policy }) {
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
    function applyVideoLimits(limits, stillRelevant = () => true) {
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
                        await track.applyConstraints({ ...track.getConstraints(), ...(preferred.screen
                            ? screenShareConstraints(preferred, state.mediaLimits)
                            : videoConstraints(preferred.width, preferred.height, preferred.fps, state.mediaLimits)) });
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
                if (sender === screenSender) parameters.degradationPreference =
                    sender.track?.contentHint === "motion" ? "balanced" : "maintain-resolution";
                return { sender, parameters, encodings: parameters.encodings || [],
                    bitrateHeadroom: sender === screenSender ? SCREEN_BITRATE_HEADROOM : Infinity };
            });
            capVideoEncodings(sources, state.mediaLimits, policy.lowBandwidth ? policy.lowBandwidthBitrate : 0);
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

    // ---------------------------------------------------------------------------
    // Screen share (69-72, 85)
    // ---------------------------------------------------------------------------

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
        if (!sharing) stopShareStatus();
        btn.disabled = !!V().state.shareStarting || !!V().state.shareStopping;
        const label = sharing ? tLabel("voice.stopShare") : tLabel("voice.startShare");
        btn.classList.toggle("active", sharing);
        btn.setAttribute("aria-pressed", String(sharing));
        btn.setAttribute("aria-label", label);
        btn.title = label;
        labelButton(btn, "screen", sharing ? tLabel("voice.stopShare") : tLabel("voice.share"));
        refreshShareStatus();
    }

    // shareToggle is the voice-screen button handler: stop when sharing
    // (confirming first, 85), otherwise open the share dialog.
    async function shareToggle() {
        const { state } = V();
        if (state.shareStarting || state.shareStopping) return;
        if (state.screenSharing) {
            await confirmStopShare();
            return;
        }
        openShareDialog();
    }

    function openShareDialog(replacing = false, initialPreset = "balanced", initialAudio = "none", initialSurface = "monitor", custom = {}) {
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
                <div class="sh-quality"></div>
                <label class="dlg-label" for="${audioID}">${tLabel("share.audio")}</label>
                <select class="dlg-input sh-audio" id="${audioID}" aria-describedby="${audioID}-help">
                    <option value="none">${tLabel("share.audioNone")}</option>
                    <option value="application">${tLabel("share.audioApplication")}</option>
                    <option value="system">${tLabel("share.audioSystem")}</option>
                </select>
                <p class="set-hint" id="${audioID}-help">${tLabel("share.applicationHelp")}</p>
                <div class="dlg-buttons">
                    <button class="dlg-ok">${tLabel("voice.startShare")}</button>
                    <button class="dlg-cancel">${tLabel("polish.cancel")}</button>
                </div>
            </div>`;
        overlay.querySelector(".dlg-cancel").onclick = () => overlay.remove();
        const quality = createShareQualityControls(qualityID, { preset: initialPreset, custom });
        overlay.querySelector(".sh-quality").appendChild(quality.element);
        const audioSelect = overlay.querySelector(".sh-audio");
        const audioChoices = new Map();
        if (replacing) audioChoices.set(initialSurface,
            V().state.shareStream?.getAudioTracks().some(track => track.readyState === "live") ? initialAudio : "none");
        overlay.querySelector(`input[value="${initialSurface}"]`).checked = true;
        const selectedSurface = () => overlay.querySelector(`input[name="${sourceName}"]:checked`).value;
        // Keep explicit choices per source so toggling sources cannot undo an override.
        audioSelect.onchange = () => audioChoices.set(selectedSurface(), audioSelect.value);
        const syncAudioSource = () => {
            const surface = selectedSurface();
            const application = surface === "window";
            audioSelect.querySelector('[value="application"]').disabled = !application;
            audioSelect.value = audioChoices.get(surface) ?? (application ? "application" : "none");
            if (!application && audioSelect.value === "application") audioSelect.value = "none";
        };
        for (const source of overlay.querySelectorAll(`input[name="${sourceName}"]`)) source.onchange = syncAudioSource;
        syncAudioSource();
        overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
        overlay.querySelector(".dlg-ok").onclick = async () => {
            if (!isCurrentServerDialog(overlay)) return;
            const selected = quality.read();
            if (!selected) return;
            const surface = overlay.querySelector(`input[name="${sourceName}"]:checked`).value;
            const audioMode = audioSelect.value;
            overlay.remove();
            const { state } = V();
            if (state.shareStarting || state.shareStopping || (!!state.screenSharing !== replacing)) return;
            const request = {};
            state.shareStarting = request;
            syncShareButton();
            try { await startShare({ surface, ...selected, audioMode, replacing }); }
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
    async function startShare({ surface, preset, custom, audioMode, replacing = false }) {
        const requestedAudioMode = audioMode;
        const { state } = V();
        const generation = state.serverGeneration;
        const tabID = state.activeTabID;
        const peerConnection = state.pc;
        if (!peerConnection) return;
        const shareScope = captureMediaScope();
        const current = () => mediaScopeIsCurrent(shareScope) && state.serverGeneration === generation && state.activeTabID === tabID && state.pc === peerConnection;
        let shareTransceiver = null;
        let nextRegion = null;
        const discardDisplay = (stream) => {
            nextRegion?.remove();
            stream?.getTracks().forEach((track) => track.stop());
            shareTransceiver?.stop();
            if (state.shareVideoTransceiver === shareTransceiver) state.shareVideoTransceiver = null;
            if (stream?.getTracks().includes(state.shareAudioSender?.track)) {
                state.shareAudioTransceiver?.stop();
                state.shareAudioTransceiver = null;
                state.shareAudioSender = null;
            }
        };
        const previousDisplay = state.shareStream;
        const p = shareQuality(preset, custom);
        if (!p) return;
        const limits = state.mediaLimits;
        const video = screenShareConstraints(p, limits);
        if (surface !== "region") video.displaySurface = surface; // 69: "monitor" | "window"
        const gdm = { video, ...displayAudioOptions(audioMode) };
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
            if (audioMode !== "none" && e.name !== "NotAllowedError" && e.name !== "AbortError") {
                // A runtime can refuse display audio. Retry without audio, but
                // never reopen the picker after cancellation or denied access.
                try {
                    display = await navigator.mediaDevices.getDisplayMedia({ ...gdm, ...displayAudioOptions("none") });
                    audioMode = "none";
                } catch (e2) {
                    if (!current()) return;
                    V().sysMsg(tLabel("desktop.screen.capture.failed") + (e2.message || e2.name));
                    return;
                }
            } else {
                V().sysMsg(tLabel("desktop.screen.capture.failed") + (e.message || e.name));
                return;
            }
        }
        if (!current()) {
            discardDisplay(display);
            return;
        }
        audioMode = validateDisplayAudio(display, audioMode);
        const screenTrack = display.getVideoTracks()[0];
        if (!screenTrack) {
            discardDisplay(display);
            V().sysMsg(tLabel("desktop.screen.capture.produced.no.video.track"));
            return;
        }
        capturePreferences.set(screenTrack, { ...p, screen: true });
        screenTrack.contentHint = preset === "text" ? "text" : p.fps > 30 ? "motion" : "detail";
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
            nextRegion = await pickRegionAndCrop(screenTrack, current);
            if (!nextRegion || !current()) {
                discardDisplay(display);
                return; // cancelled
            }
        }

        if (!trackFitsVideoLimits(screenTrack, state.mediaLimits)) {
            discardDisplay(display);
            V().sysMsg(tLabel("voice.mediaDimensionsFailed"));
            return;
        }
        // Keep the current source alive while the picker is open or cancelled.
        if (replacing) {
            if (state.shareStream !== previousDisplay || !state.screenSharing) { discardDisplay(display); return; }
            await doStopShare();
            if (!current() || shareEnded || screenTrack.readyState === "ended") { nextRegion?.remove(); discardDisplay(display); return; }
        }
        state.shareStream = display;
        state.regionBox = nextRegion;
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
                V().sysMsg(tLabel("desktop.publishing.screen.share.failed") + (e.message || e.name));
                // a rollback only drops transceivers created by applying a remote
                // description, so this one survives with its direction flip and
                // would be re-offered — stop it to keep the dead m-line out of
                // videoSender()'s reach.
                discardDisplay(display);
                state.shareStream = null;
                applySendCaps();
                clearRegionBox(); // (71) nothing is being cropped after this
                return;
            }
        }
        // (70) merge display audio as a second published audio track (the server
        // fans out every publisher audio track) + renegotiate.
        const displayAudio = display.getAudioTracks()[0];
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
                    V().sysMsg(tLabel("desktop.publishing.share.audio.failed") + (e.message || e.name));
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
                    V().sysMsg(tLabel("desktop.publishing.share.audio.failed") + (e.message || e.name));
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
            V().sysMsg(tLabel("desktop.screen.share.permission.failed") + shareErr);
            await doStopShare();
            return;
        }
        syncShareButton();
        if (requestedAudioMode !== "none" && audioMode === "none") {
            V().toast(tLabel(requestedAudioMode === "application" ? "share.applicationUnavailable" : "share.audioNotSelected"), "warn");
        }
        startShareStatus({ stream: display, pc: peerConnection, scope: shareScope, preset: p, surface, audioMode,
            generation: publicationSnapshot().find(p => p.publication.slot === "screen")?.generation,
            stop: () => { V().$("voice-screen").focus(); void doStopShare(); }, change: () => openShareDialog(true, preset, audioMode, surface, custom),
            reduction: () => policy.lowBandwidth ? "share.lowBandwidth" : "" });
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
                <div class="region-title">${escapeTranslation(tLabel("desktop.drag.to.move.drag.corner.to.resize"))}</div>
                <button class="region-ok">${escapeTranslation(tLabel("desktop.crop.share"))}</button>
                <button class="region-cancel">${escapeTranslation(tLabel("desktop.cancel"))}</button>`;
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
                finish(null);
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
                    finish(box);
                } catch (e) {
                    if (!settled && isCurrent()) V().sysMsg(tLabel("desktop.region.capture.failed") + (e.message || e.name));
                    cancel();
                }
            };
        });
    }

    // clearRegionBox drops the persistent crop target (71).
    function clearRegionBox() {
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
        applySendCaps();
        syncCameraButton();
        try {
            await stopPublication("screen");
            if (pc && current()) await renegotiate(pc, generation);
        } catch (error) {
            if (current()) V().sysMsg(tLabel("desktop.stopping.screen.share.failed") + (error.message || error.name));
        } finally {
            if (current()) { state.shareStopping = false; syncShareButton(); }
        }
    }

    // ---------------------------------------------------------------------------
    // Camera capture is session-local and starts only from an explicit action.
    // ---------------------------------------------------------------------------

    let cameraOff = true;
    let cameraRequest = null;

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
        applyCameraPreview(preview, state.settings);
        const stream = enabled ? state.localStream : null;
        if (preview.srcObject !== stream) {
            preview.srcObject = stream;
            renderMicStatus($("mic-status"), state.micState, V().retryMicrophoneAccess, enabled, $("ptt-btn"));
        }
    }

    // resetCameraState clears the toggle for a fresh (or ended) voice session.
    function resetCameraState() {
        stopShareStatus();
        cameraOff = true;
        cameraRequest?.controller?.abort();
        cameraRequest = null;
        syncCameraButton();
        syncShareButton();
    }

    // cameraToggle is the voice-video button handler.
    async function cameraToggle() {
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
        const request = { controller: new AbortController() };
        const cameraScope = captureMediaScope();
        cameraRequest = request;
        const current = () => mediaScopeIsCurrent(cameraScope) && cameraRequest === request && state.pc === pc &&
            state.localStream === localStream && state.serverGeneration === generation && state.activeTabID === tabID;
        syncCameraButton();
        let stream, sender, transceiver;
        try {
            const capture = await captureCamera(state.settings, state.mediaLimits, { signal: request.controller.signal });
            stream = capture.stream;
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
            if (current()) V().sysMsg(tLabel("desktop.camera.unavailable") + (error.message || error.name));
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

    return { applyVideoLimits, shareToggle, clearRegionBox, resetCameraState, cameraToggle, applySendCaps, syncCameraButton, syncShareButton };
}
