import { hardwareVideoEvidence } from "./hardware-video.js";
import { updateStartupBlocked } from "./update-transition.js";
import { retainDisplayCapture, stopDisplayCapture, onDisplayCaptureStop } from "./display-capture-lease.js";
// video-publication.js — camera/screen capture and publication lifecycle.
import { captureCamera, applyCameraPreview } from "./camera-capture.js";
import { captureMediaScope, mediaScopeIsCurrent } from "./media-controls.js";
import { startPublication, stopPublication, publicationSnapshot, preparePublicationUpload, publicationUploadActive, publicationCodec, publicationHEVCFormat, withdrawPublicationHEVC } from "./stream-publication.js";
import { hardwareRecoveryCodec, knownSoftwareVideoEncoder } from "./video-hevc.js";
import { preferPublicationCodec } from "./video-codecs.js";
import { startShareStatus, stopShareStatus, refreshShareStatus } from "./share-status.js";
import { applyVideoNetworkPriority } from "./voice-priority.js";
import { shareQuality, DEFAULT_SHARE_PRESET } from "./screen-share-quality.js";
import { createSharePicker } from "./share-picker.js";
import { closeDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { labelButton } from "./icons.js";
import { renderMicStatus } from "./audio.js";
import { videoConstraints, trackFitsVideoLimits, capVideoEncodings } from "./media-limits.js";
import { t as tLabel } from "./i18n.js";
import { rememberVideoSenderProfile } from "./video-sender-stats.js";
import { hardwareVideoEncodeAvailable, videoSenderCaptureSample, hardwareStartupGeometry } from "./video-sender-hardware-recovery.js";
import { sampleSenderStats } from "./peer-stats.js";
import { nativeShareStatus, nativeSource, nativeSourceOptions, nativeStandInStream, nativeEventIsCurrent, nativeUnavailableKey, nativeFailureKey,
    nativeRegionSource, nativeScreenPreview, nativeTile,
    nativeThumbnails, rememberNativeSource, rememberedNativeSource } from "./native-share.js";
import { negotiateOffer, queuePeerNegotiation } from "./video-negotiation.js";
const V = () => window.__noxa;

export function createVideoPublication({ policy }) {
    // videoSender returns the sender of a video transceiver this client can send
    // on, or null. Matches by transceiver and skips recvonly ones: with no camera there is no send transceiver
    // at all and replaceTrack on a server-side recvonly would publish nothing.
    function videoSenderFor(pc) {
        if (!pc) return null;
        const t = pc.getTransceivers().find((t) =>
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
    const codecSelections = new WeakMap();
    function selectCodec(slot, track, transceiver) {
        const codec = publicationCodec(slot, track);
        const format = codec === "h265" ? publicationHEVCFormat(slot, track) : undefined;
        const previous = codecSelections.get(transceiver);
        if (!codec || previous?.codec === codec && previous?.format?.sdpFmtpLine === format?.sdpFmtpLine) return false;
        if (!preferPublicationCodec(transceiver, codec, format)) {
            if (codec === "h265") throw new Error("HEVC format unavailable");
            return false;
        }
        codecSelections.set(transceiver, { codec, format });
        return true;
    }

    async function negotiateInitialCodec(slot, track, transceiver, pc, generation, current, tabID) {
        try {
            selectCodec(slot, track, transceiver);
            await negotiateOffer(pc, generation, undefined, current, tabID);
        } catch (error) {
            if (!current() || publicationCodec(slot, track) !== "h265" ||
                !await withdrawPublicationHEVC(slot, track, current, false)) throw error;
            selectCodec(slot, track, transceiver);
            await negotiateOffer(pc, generation, undefined, current, tabID);
        }
    }

    function applyPublicationSettings(slot, track, { demandOnly = false } = {}) {
        const { state } = V(), pc = state.pc, scope = captureMediaScope();
        if (!pc) return Promise.resolve();
        const current = () => state.pc === pc && mediaScopeIsCurrent(scope) && track.readyState !== "ended" &&
            state.localStream?.getVideoTracks().includes(track);
        return queuePeerNegotiation(pc, async () => {
            if (!current()) return;
            const transceiver = pc.getTransceivers().find(item => item.sender.track === track);
            const previous = codecSelections.get(transceiver);
            try {
                if (!demandOnly && selectCodec(slot, track, transceiver)) await negotiateOffer(pc, scope.generation, undefined, current, scope.tabID);
            } catch (error) {
                if (!current()) throw error;
                if (publicationCodec(slot, track) === "h265" && await withdrawPublicationHEVC(slot, track, current, false)) {
                    selectCodec(slot, track, transceiver);
                    await negotiateOffer(pc, scope.generation, undefined, current, scope.tabID);
                } else {
                    if (previous) { preferPublicationCodec(transceiver, previous.codec, previous.format); codecSelections.set(transceiver, previous); }
                    else codecSelections.delete(transceiver);
                    throw error;
                }
            }
            if (current()) await applySendCaps(current);
        });
    }

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
            ...(state.localStream?.getVideoTracks() || []),
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
                        await track.applyConstraints({ ...track.getConstraints(), ...videoConstraints(preferred.width, preferred.height, preferred.fps, state.mediaLimits) });
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
                stopDisplayCapture(state.shareStream);
                for (const track of videoTracks()) track.stop();
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
            // The camera is the only browser video sender: a native screen share sends from Go.
            const sources = [videoSenderFor(pc)].filter(sender => sender &&
                (sender === pendingVideoSenders.get(pc)?.sender || (sender.track && sender.track.readyState !== "ended"))).map(sender => {
                const parameters = sender.getParameters();
                for (const encoding of parameters.encodings || []) {
                    encoding.priority = "medium";
                    applyVideoNetworkPriority(encoding); // never in the voice class
                }
                return { sender, parameters, encodings: parameters.encodings || [], singleEncoding: true,
                    uploadActive: publicationUploadActive("cam", pendingVideoSenders.get(pc)?.sender === sender ? pendingVideoSenders.get(pc).track : sender.track),
                    bitrateHeadroom: Infinity };
            });
            capVideoEncodings(sources, state.mediaLimits, policy.lowBandwidth ? policy.lowBandwidthBitrate : 0);
            for (const { sender, encodings } of sources) {
                if (!current()) return;
                if (!encodings.length) continue;
                // Chrome accepts only a getParameters result from the same task;
                // an earlier sender's await would invalidate a stale read.
                const fresh = sender.getParameters();
                fresh.encodings = encodings;
                await sender.setParameters(fresh);
            }
        });
        capUpdates.set(pc, pending);
        pending.catch(() => { if (current()) V().sysMsg(tLabel("voice.mediaCapsFailed")); });
        return pending;
    }

    // WebRTC reinitializes its encoder on a size change, retrying hardware first.
    // A bounded scale-only pulse keeps capture, codec, adaptation and one upload.
    // Explicit scale applies to already-adapted input; capture/output ratios do
    // not reveal that native input and must never become a replacement scale.
    // Capability queries alone do not guarantee hardware on every runtime; the
    // recovery scheduler must confirm the result using actual encoder stats.
    // Resolves null when it declines before the pulse, false when the pulse failed.
    function retryVideoHardware(slot, track, stillRelevant = () => true, captureRecovery = null) {
        const { state } = V(), pc = state.pc, scope = captureMediaScope();
        // The camera only: a native screen share encodes in Go.
        const transceiver = slot === "cam" ? pc?.getTransceivers().find(item => item.sender.track === track) : null;
        const sender = transceiver?.sender;
        if (!pc || !sender) return Promise.resolve(null);
        const owns = () => state.pc === pc && mediaScopeIsCurrent(scope) && !transceiver.stopped && sender.track === track &&
            track?.readyState === "live" && state.localStream?.getVideoTracks().includes(track);
        const current = () => owns() && track.enabled && stillRelevant() && publicationUploadActive(slot, track) !== false;
        return queuePeerNegotiation(pc, () => {
            const pending = (capUpdates.get(pc) || Promise.resolve()).catch(() => {}).then(async () => {
                if (!current() || !navigator.mediaCapabilities?.encodingInfo) return null;
                const sample = async () => {
                    const reports = await sampleSenderStats(sender, { fresh: true });
                    const video = [...reports.values()].filter(row => row.type === "outbound-rtp" && !row.isRemote &&
                        (row.kind === "video" || row.mediaType === "video"));
                    return video.length === 1 ? { ...video[0], codec: reports.get(video[0].codecId),
                        startupGeometry: hardwareStartupGeometry(reports, video[0], track, sender.getParameters().encodings?.[0]),
                        hardwareEvidence: hardwareVideoEvidence(reports, track.id),
                        capture: videoSenderCaptureSample(reports, video[0], track) } : null;
                };
                const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
                const first = await sample();
                const parameters = sender.getParameters(), encoding = parameters.encodings?.[0];
                const scale = encoding?.scaleResolutionDownBy ?? 1;
                const size = track.getSettings();
                const startup = captureRecovery?.hardwareStartup === true;
                const dimensions = row => startup && row?.startupGeometry ? row.startupGeometry : { width: row?.frameWidth, height: row?.frameHeight };
                const eligible = row => {
                    const geometry = dimensions(row);
                    const implementation = startup ? row?.startupGeometry && row.powerEfficientEncoder === true && row.framesEncoded === (captureRecovery.framesEncoded ?? 0) :
                        row?.powerEfficientEncoder === false && hardwareRecoveryCodec(row.codec) && (row.codec.mimeType.toLowerCase() === "video/h264" || knownSoftwareVideoEncoder(row.codec, row.encoderImplementation));
                    return row && implementation && hardwareRecoveryCodec(row.codec) &&
                        ["none", "cpu", "bandwidth"].includes(row.qualityLimitationReason) && geometry.width >= 640 && geometry.height >= 360 &&
                        geometry.width % 4 === 0 && geometry.height % 4 === 0 &&
                        Number.isFinite(size.width) && Number.isFinite(size.height) &&
                        geometry.width * scale <= size.width && geometry.height * scale <= size.height &&
                        geometry.width * size.height === geometry.height * size.width;
                };
                // Revalidate the exact source and either previously working
                // software-fallback geometry or the bounded first-frame stall.
                const capturedProgress = (before, after) => captureRecovery &&
                    [before, after].every(row => row?.capture?.id === captureRecovery.sourceId &&
                        row.codec?.mimeType === captureRecovery.codec && row.codec?.sdpFmtpLine === captureRecovery.fmtp &&
                        dimensions(row).width === captureRecovery.width && dimensions(row).height === captureRecovery.height &&
                        (startup ? row.startupGeometry && row.framesEncoded === (captureRecovery.framesEncoded ?? 0) : knownSoftwareVideoEncoder(row.codec, row.encoderImplementation)) &&
                        Number.isSafeInteger(row.framesEncoded) && row.framesEncoded >= 0) &&
                    after.framesEncoded >= before.framesEncoded && after.capture.frames > before.capture.frames &&
                    after.capture.timestamp > before.capture.timestamp && after.capture.timestamp - before.capture.timestamp <= 15000;
                if (!current() || parameters.encodings?.length !== 1 || encoding.active === false || !Number.isFinite(scale) || scale < 1 || !eligible(first)) return null;
                const recoverySize = dimensions(first);
                // The shared bounded cache retains hung native queries; a later
                // retry must not launch additional work for the same geometry.
                const sourceRate = size.frameRate > 0 ? size.frameRate : 30;
                const encodedRate = encoding.maxFramerate > 0 ? Math.min(sourceRate, encoding.maxFramerate) : sourceRate;
                const supported = await Promise.all([1, 2].map(divisor => hardwareVideoEncodeAvailable(first.codec, {
                    frameWidth: recoverySize.width / divisor, frameHeight: recoverySize.height / divisor,
                    framesPerSecond: encodedRate,
                })));
                if (supported.some(value => !value)) return null;
                await pause(150);
                const baseline = await sample();
                if (!current() || !eligible(baseline) || baseline.id !== first.id || baseline.codecId !== first.codecId || baseline.ssrc !== first.ssrc ||
                    baseline.timestamp <= first.timestamp ||
                    !(baseline.framesEncoded > first.framesEncoded || capturedProgress(first, baseline)) ||
                    dimensions(baseline).width !== recoverySize.width || dimensions(baseline).height !== recoverySize.height ||
                    track.getSettings().width !== size.width || track.getSettings().height !== size.height) return null;
                const originalScale = encoding.scaleResolutionDownBy;
                const pulseScale = scale * 2;
                let pulsed = false;
                try {
                    await pause(150);
                    const ready = await sample();
                    if (!current() || !eligible(ready) || ready.id !== first.id || ready.codecId !== first.codecId || ready.ssrc !== first.ssrc ||
                        ready.timestamp <= baseline.timestamp || ready.framesEncoded < baseline.framesEncoded ||
                        dimensions(ready).width !== recoverySize.width || dimensions(ready).height !== recoverySize.height ||
                        (!(baseline.framesEncoded > first.framesEncoded) &&
                            (ready.timestamp <= baseline.timestamp || !capturedProgress(baseline, ready))) ||
                        track.getSettings().width !== size.width || track.getSettings().height !== size.height) return null;
                    const pulse = sender.getParameters();
                    if (pulse.encodings?.length !== 1 || (pulse.encodings[0].scaleResolutionDownBy ?? 1) !== scale || pulse.encodings[0].active === false) return null;
                    pulse.encodings[0].scaleResolutionDownBy = pulseScale;
                    pulsed = true;
                    await sender.setParameters(pulse);
                    const deadline = performance.now() + 2000;
                    while (current() && performance.now() < deadline) {
                        await pause(100);
                        const remaining = deadline - performance.now();
                        if (remaining <= 0) break;
                        let timer, row;
                        try {
                            row = await Promise.race([sample(), new Promise(resolve => { timer = setTimeout(() => resolve(null), remaining); })]);
                        } finally { clearTimeout(timer); }
                        if (!row) break;
                        if (current() && row?.timestamp > ready.timestamp && row.framesEncoded > ready.framesEncoded &&
                            row.id === first.id && row.codecId === first.codecId && row.ssrc === first.ssrc &&
                            row.frameWidth === recoverySize.width / 2 && row.frameHeight === recoverySize.height / 2 &&
                            (!startup || row.hardwareEvidence === "hardware") &&
                            track.getSettings().width === size.width && track.getSettings().height === size.height) return true;
                    }
                    return false;
                } finally {
                    // Restore only fields still owned by this operation. Teardown or
                    // source replacement must never be undone by a late completion.
                    if (pulsed && owns()) {
                        const restore = sender.getParameters();
                        if (restore.encodings?.length === 1 && restore.encodings[0].scaleResolutionDownBy === pulseScale) {
                            if (originalScale === undefined) delete restore.encodings[0].scaleResolutionDownBy;
                            else restore.encodings[0].scaleResolutionDownBy = originalScale;
                            // A runtime that never settles setParameters cannot safely
                            // accept later writes: end the publication instead of
                            // holding negotiation and cap updates behind it.
                            let timer;
                            const restored = await Promise.race([sender.setParameters(restore).then(() => true),
                                new Promise(resolve => { timer = setTimeout(() => resolve(false), 5000); })]).finally(() => clearTimeout(timer));
                            if (!restored && owns()) void stopCamera();
                        }
                    }
                }
            });
            capUpdates.set(pc, pending);
            return pending;
        });
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

    let shareDialogID = 0;
    // The Studio program currently published on the screen slot, if any.
    let studioShare = null;
    // A native share has no source to resume after a reconnect: it ends with the connection.
    function endShareForReconnect() {
        if (nativePending) nativePending.ended = true; // a Start still in flight is discarded too
        if (V().state.nativeShare) void doStopShare();
    }
    // The settings of the running share for the post-update resume prompt, or null.
    function updateShareSettings() {
        const native = V().state.screenSharing && V().state.nativeShare;
        if (!native) return null;
        return { surface: native.studio ? "studio" : native.surface, ...native.selection, audioMode: native.audioMode };
    }
    function openResumedShare(settings) {
        if (settings.surface === "studio") { document.getElementById("voice-studio")?.click(); return; }
        openShareDialog(false, settings.preset, settings.audioMode, settings.surface, settings.custom, settings.bitrateCeiling);
    }

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
    // (confirming first, 85), otherwise open the share dialog (which explains
    // when this device cannot share).
    async function shareToggle() {
        if (updateStartupBlocked()) return;
        const { state } = V();
        if (state.shareStarting || state.shareStopping) return;
        if (state.screenSharing) {
            await confirmStopShare();
            return;
        }
        openShareDialog();
    }

    // openCallShareDialog is noXa's picker for a private call's screen share:
    // startNative({source, max_width, max_height, fps, bitrate, audioMode})
    // streams it natively.
    function openCallShareDialog({ startNative, audio = "none", onClose = () => {} }) {
        openShareDialog(false, DEFAULT_SHARE_PRESET, audio, null, {}, 0, { startNative, onClose });
    }

    // openShareDialog is noXa's share picker: every screen and window as a row
    // with a live thumbnail (a running game first among the windows), and a
    // preview of the picked one. The source preselected is the one of
    // initialSurface (a reopen), else the last shared one's. A Region is drawn
    // on a still of the picked screen and cropped on the GPU. When this device
    // cannot share natively the dialog says why and Start stays disabled.
    function openShareDialog(replacing = false, initialPreset = DEFAULT_SHARE_PRESET, initialAudio = "none", initialSurface = null, custom = {}, bitrateCeiling = 0, call = null) {
        const dialogID = ++shareDialogID;
        const nativeAvailable = status => !!status && !nativeUnavailableKey(status, !!call);
        const overlay = document.createElement("div");
        overlay.className = "dlg-overlay";
        // The visible title is just "Share"; the dialog keeps its full name.
        overlay.setAttribute("aria-label", tLabel("polish.shareTitle"));
        const view = createSharePicker({ id: `share-${dialogID}`, preset: initialPreset, custom, bitrateCeiling,
            surface: initialSurface || "monitor", audio: initialAudio });
        overlay.appendChild(view.element);
        const { quality, $ } = view;
        const initialKind = (initialSurface || rememberedNativeSource()?.surface) === "window" ? "window" : "monitor";
        const sources = () => overlay.querySelectorAll(`input[name="${view.sourceName}"]`);
        // The given audio belongs to the surface opened with; a fresh open's "none" is no choice (a window still defaults to its application).
        const audioChoices = new Map(initialSurface || initialAudio !== "none" ? [[initialSurface || initialKind, initialAudio]] : []);
        // Keep explicit choices per source so toggling sources cannot undo an override.
        $(".sh-audio").addEventListener("change", () => { audioChoices.set(view.surface(), view.audio()); refreshNative(); });
        const syncAudioSource = () => {
            const surface = view.surface();
            const application = surface === "window";
            view.setApplicationAudio(application);
            let audio = audioChoices.get(surface) ?? (application ? "application" : "none");
            if (!application && audio === "application") audio = "none";
            view.setAudio(audio);
        };
        let publishing = false;
        const error = $(".sh-error");
        const start = $(".dlg-ok"), cancel = $(".dlg-cancel"), close = $(".sp-close"), area = $(".sp-area");
        let nativeChoice = () => null; // the native source when Start can run
        let nativeStatus = null, nativeOptions = [], nativeValue = "", thumbs = {};
        const update = () => {
            cancel.disabled = close.disabled = publishing;
            start.disabled = publishing || !nativeChoice();
            for (const input of sources()) input.disabled = publishing;
            area.disabled = publishing || !nativeValue || nativeValue.startsWith("w:");
            $(".sh-audio").disabled = publishing; quality.element.inert = publishing;
        };
        view.onRegionChange(update);
        view.onAreaChange(() => { syncAudioSource(); update(); });
        const nativeNote = $(".sp-native-note");
        const lists = { monitor: $(".sp-screens"), window: $(".sp-windows") };
        const nativeUsable = () => nativeAvailable(nativeStatus);
        const pick = value => {
            nativeValue = value;
            if (value.startsWith("w:")) view.setArea(false);
            dropNativePreview(); view.clearPreview(); refreshNative();
        };
        // A row's name and detail: "Screen 2" / "1920 × 1080 · Primary"; a window's title / "Game · running" or its executable.
        const rowText = o => o.value.startsWith("m:")
            ? [tLabel("share.screenName", { n: Number(o.value.slice(2)) + 1 }), [o.size.replace("x", " × "), o.primary ? tLabel("share.primary") : ""].filter(Boolean).join(" · ")]
            : [o.label, o.game ? tLabel("share.nativeGameRunning") : o.exe];
        // Rows are reused by source value: a refresh never replaces the row
        // (or the radio) that has keyboard focus, and it keeps thumbnails.
        const fillList = (list, options) => {
            const rows = new Map([...list.querySelectorAll(".sp-native-tile")].map(row => [row.querySelector("input").value, row]));
            const wanted = options.map(o => {
                const row = rows.get(o.value) || nativeTile(o, view.sourceName, () => pick(o.value));
                const [name, info] = rowText(o);
                const title = row.querySelector(".sp-native-title"), detail = row.querySelector(".sp-native-detail");
                if (title.textContent !== name) title.textContent = name;
                if (detail.textContent !== info) detail.textContent = info;
                detail.hidden = !info;
                detail.classList.toggle("sp-game", !!o.game);
                return row;
            });
            const current = [...list.children];
            if (wanted.length !== current.length || wanted.some((row, i) => row !== current[i])) {
                const focused = list.contains(document.activeElement) ? document.activeElement : null;
                list.replaceChildren(...wanted);
                // Reordering detaches nodes: put focus back on the same source.
                if (focused?.isConnected && document.activeElement !== focused) focused.focus({ preventScroll: true });
            }
            list.parentElement.hidden = !options.length;
        };
        const refreshNative = () => {
            const usable = nativeUsable();
            const options = { monitor: usable ? nativeSourceOptions(nativeStatus, "monitor") : [], window: usable ? nativeSourceOptions(nativeStatus, "window") : [] };
            nativeOptions = [...options.monitor, ...options.window];
            // A refresh keeps the user's pick while that source still exists; else
            // the preselected one of the same kind (first: the initial kind), else of the other.
            if (!nativeOptions.some(o => o.value === nativeValue)) {
                const kind = nativeValue ? (nativeValue.startsWith("w:") ? "window" : "monitor") : initialKind;
                nativeValue = (options[kind].find(o => o.selected) || options[kind === "window" ? "monitor" : "window"].find(o => o.selected))?.value || "";
                if (nativeValue.startsWith("w:")) view.setArea(false);
                // The old source is gone: never show its picture over the new pick.
                if (preview) { dropNativePreview(); view.clearPreview(); }
            }
            fillList(lists.monitor, options.monitor);
            fillList(lists.window, options.window);
            for (const input of sources()) input.checked = input.value === nativeValue;
            const option = nativeOption();
            view.setSourceLabel(option ? rowText(option)[0] : "");
            nativeNote.textContent = !nativeStatus ? "" : !usable ? tLabel(nativeUnavailableKey(nativeStatus, !!call))
                : !nativeOptions.length ? tLabel("share.nativeNoSources") : "";
            nativeNote.hidden = !nativeNote.textContent;
            syncAudioSource();
            update();
            void showNativePreview();
        };
        // The preview: a large still of the picked screen (painted at its pixel
        // size, so a Region's size hint and minimum size stay right), or the
        // picked window's thumbnail enlarged; it refreshes with the thumbnails.
        let preview = null, previewToken = 0;
        const dropNativePreview = () => {
            previewToken++;
            preview?.stream.getTracks().forEach(track => track.stop());
            preview = null;
        };
        const showNativePreview = async (refresh = false) => {
            if (!nativeUsable() || !nativeValue || publishing) {
                if (preview) { dropNativePreview(); view.clearPreview(); update(); }
                return;
            }
            if (!refresh && preview?.value === nativeValue) return;
            const value = nativeValue, token = ++previewToken, screen = value.startsWith("m:");
            const url = screen ? await nativeScreenPreview(value) : thumbs[value] || "";
            if (!url || token !== previewToken || !isCurrentServerDialog(overlay)) return;
            const image = new Image();
            image.src = url;
            try { await image.decode(); } catch { return; }
            if (token !== previewToken || !isCurrentServerDialog(overlay) || value !== nativeValue) return;
            if (preview?.value !== value) {
                dropNativePreview();
                const [width, height] = screen ? String(nativeOption()?.size || "").split("x").map(Number) : [];
                const canvas = document.createElement("canvas");
                canvas.width = width || image.naturalWidth; canvas.height = height || image.naturalHeight;
                preview = { value, canvas, stream: canvas.captureStream(), shown: false };
                previewToken = token;
            }
            preview.canvas.getContext("2d")?.drawImage(image, 0, 0, preview.canvas.width, preview.canvas.height);
            if (!preview.shown) { preview.shown = true; view.showPreview(preview.stream); update(); }
        };
        // Live thumbnails (and the window list) refresh about every 1.5 s while the picker is open.
        let nativeTimer = 0, nativeBusy = false;
        const pollNative = async () => {
            if (nativeBusy || !isCurrentServerDialog(overlay) || publishing || !nativeUsable()) return;
            nativeBusy = true;
            try {
                const status = await nativeShareStatus(V().state.activeTabID);
                if (!isCurrentServerDialog(overlay)) return;
                if (nativeAvailable(status)) { nativeStatus = status; refreshNative(); }
                thumbs = await nativeThumbnails(nativeOptions.map(o => o.value));
                if (!isCurrentServerDialog(overlay)) return;
                for (const img of $(".sp-sources").querySelectorAll("img[data-value]")) if (thumbs[img.dataset.value]) img.src = thumbs[img.dataset.value];
                await showNativePreview(true);
            } finally { nativeBusy = false; }
        };
        const nativeOption = () => nativeOptions.find(o => o.value === nativeValue) || null;
        nativeChoice = () => {
            if (!nativeUsable() || !nativeValue) return null;
            // The drawn area belongs to the screen whose still is shown, never to one still loading.
            if (view.surface() === "region") return preview?.shown && preview.value === nativeValue ? nativeRegionSource(nativeValue, view.region()) : null;
            return nativeSource(view.surface(), nativeValue);
        };
        void nativeShareStatus(V().state.activeTabID).then(status => {
            if (!isCurrentServerDialog(overlay)) return;
            nativeStatus = status; refreshNative();
            if (!nativeAvailable(status)) return;
            void pollNative();
            nativeTimer = setInterval(() => { void pollNative(); }, 1500);
        });
        syncAudioSource(); update();
        cancel.onclick = close.onclick = () => closeDialog(overlay, "cancel");
        overlay.onclick = (event) => { if (event.target === overlay && !publishing) closeDialog(overlay, "cancel"); };
        start.onclick = async () => {
            const native = nativeChoice();
            if (!isCurrentServerDialog(overlay) || publishing || !native) return;
            const selected = quality.read();
            if (!selected) { view.closePanels(); $(".sp-quality-toggle").click(); quality.read(); return; }
            const surface = view.surface();
            if (call) { void startCallShare(native, selected, surface); return; }
            const { state } = V();
            if (!state.pc || state.pc.connectionState === "closed" || state.pc.connectionState === "failed") { error.textContent = tLabel("share.connectionRequired"); return; }
            if (state.shareStarting || state.shareStopping || (!!state.screenSharing !== replacing)) { error.textContent = tLabel("share.stateChanged"); return; }
            view.closePanels();
            const request = {}; state.shareStarting = request;
            publishing = true; error.textContent = ""; update(); syncShareButton();
            try {
                rememberNativeSource(surface === "region" ? "monitor" : surface, nativeOption());
                if (await startNativeShare({ surface, source: native, ...selected, audioMode: view.audio(), replacing })) closeDialog(overlay, "started");
                else if (isCurrentServerDialog(overlay)) error.textContent = tLabel("share.startFailed");
            } catch (failure) {
                if (isCurrentServerDialog(overlay)) error.textContent = tLabel("desktop.screen.capture.failed") + (failure.message || failure.name);
            } finally {
                publishing = false;
                if (state.shareStarting === request) { state.shareStarting = null; syncShareButton(); }
                if (isCurrentServerDialog(overlay)) update();
            }
        };
        // The call path of Start: native capture to the call's viewers.
        async function startCallShare(native, selected, surface) {
            view.closePanels();
            publishing = true; error.textContent = ""; update();
            try {
                const p = shareQuality(selected.preset, selected.custom, selected.bitrateCeiling);
                if (!p) return;
                rememberNativeSource(surface === "region" ? "monitor" : surface, nativeOption());
                // Native output is at most 1080p; a call has no server media limits.
                const width = Math.min(p.width || 1920, 1920), height = Math.min(p.height || 1080, 1080);
                if (await call.startNative({ source: native, max_width: width, max_height: height, fps: p.fps,
                    bitrate: p.bitrateCeiling || Math.min(8000000, Math.round(width * height * p.fps * 0.07)), audioMode: view.audio() })) closeDialog(overlay, "started");
                else if (isCurrentServerDialog(overlay)) error.textContent = tLabel("share.startFailed");
            } catch (failure) {
                if (isCurrentServerDialog(overlay)) error.textContent = tLabel("desktop.screen.capture.failed") + (failure.message || failure.name);
            } finally {
                publishing = false;
                if (isCurrentServerDialog(overlay)) update();
            }
        }
        mountServerDialog(overlay, { onCancel: () => !publishing, onClose: () => { clearInterval(nativeTimer); view.dispose(); dropNativePreview(); call?.onClose(); } });
        const hint = call ? "" : mediaLimitHint();
        if (hint) {
            const note = document.createElement("p");
            note.className = "set-hint media-limit-hint";
            note.textContent = hint;
            $(".sp-notes").appendChild(note);
        }
    }

    let nativePending = null; // the native share whose Start is in flight
    // startNativeShare captures, encodes, encrypts and sends in Go on the
    // publish-only native peer. The webview owns the publication (a still
    // stand-in track is its identity, never sent) and forwards generation,
    // upload demand and codec; stop and replace go through doStopShare.
    async function startNativeShare({ surface, source, preset, custom, bitrateCeiling = 0, audioMode = "none", replacing = false, studio = null }) {
        const { state } = V();
        const tabID = state.activeTabID, pc = state.pc, serverGeneration = state.serverGeneration;
        const p = shareQuality(preset, custom, bitrateCeiling);
        if (!p || !pc || !source) return false;
        const scope = captureMediaScope();
        const current = () => mediaScopeIsCurrent(scope) && state.pc === pc && state.serverGeneration === serverGeneration && state.activeTabID === tabID;
        if (replacing) { await doStopShare(); if (!current()) return false; }
        const limits = state.mediaLimits;
        const bound = (value, limit) => limit > 0 ? Math.min(value, limit) : value;
        // Native output is at most 1080p ("original" and larger presets are bounded).
        const width = Math.min(p.width || 1920, 1920), height = Math.min(p.height || 1080, 1080);
        // Go bounds it by the server limit too, also after later limit changes.
        const bitrate = bound(p.bitrateCeiling || Math.min(8000000, Math.round(width * height * p.fps * 0.07)), Math.floor((limits?.video_max_bitrate || 0) * 0.85));
        // The share identity: Go ignores stops and updates of other shares and tags its events with it.
        const id = crypto.randomUUID();
        const stopNative = () => void Promise.resolve().then(() => window.go.main.App.StopNativeScreenShareForTab(tabID, id)).catch(() => {});
        const stream = nativeStandInStream(tLabel("share.nativeActive"));
        const track = stream.getVideoTracks()[0];
        // The capture lease keeps the display awake and raises this process's GPU/CPU priority.
        retainDisplayCapture(stream);
        // Paths that drop the share by stopping its stream (channel move, voice end) end Go capture too.
        onDisplayCaptureStop(track, () => {
            if (state.nativeShare?.track !== track) return;
            state.nativeShare = null;
            stopNative();
        });
        // Go may end the share before Start resolves: the event handler marks it here.
        const pending = { id, tabID, ended: false };
        nativePending = pending;
        try {
            await window.go.main.App.StartNativeScreenShareForTab(tabID, { id, source, max_width: bound(width, limits?.video_max_width),
                max_height: bound(height, limits?.video_max_height), fps: p.fps, bitrate, audio: audioMode, ...(studio ? { studio: studio.scene } : {}) });
        } catch (error) {
            stopDisplayCapture(stream);
            if (current()) V().sysMsg(tLabel("share.nativeFailed") + (error?.message || error));
            return false;
        } finally { if (nativePending === pending) nativePending = null; }
        // The session, peer or share state changed while Go started (or Go already ended it): discard this capture.
        if (pending.ended || !current() || state.nativeShare || state.screenSharing || track.readyState !== "live") {
            stopDisplayCapture(stream);
            stopNative();
            if (pending.failure && current()) V().toast(tLabel(nativeFailureKey(pending.failure)), "error");
            return false;
        }
        const native = { id, tabID, track, generation: "0", surface, audioMode, selection: { preset, custom, bitrateCeiling },
            studio, scenes: Promise.resolve() };
        // Go reports an audio fallback while Start is still running.
        if (pending.audio) applyNativeAudioNotice(native, pending.audio);
        // Bound calls run concurrently in Go: keep updates in order (a pause and
        // its resume share a generation).
        let updates = Promise.resolve();
        const sync = () => {
            if (state.nativeShare !== native) return;
            native.generation = publicationSnapshot().find(entry => entry.publication.slot === "screen" && entry.publication.track === track)?.generation || native.generation;
            const update = { id, generation: native.generation, upload_active: publicationUploadActive("screen", track) !== false, codec: publicationCodec("screen", track) || "" };
            updates = updates.then(() => window.go.main.App.UpdateNativeScreenShareForTab(tabID, update)).catch(() => {});
        };
        state.nativeShare = native;
        state.shareStream = stream;
        state.screenSharing = true;
        // doStopShare ends the Studio session through onEnded.
        if (studio) studioShare = { stream, onEnded: studio.onEnded };
        syncShareButton();
        let failure = "";
        try {
            if (!await preparePublicationUpload("screen", track, () => sync(), ["h264", "vp8"])) failure = "capture ended";
            else if (!await startPublication("screen", track, () => { if (state.nativeShare === native) void doStopShare(); })) failure = "capture ended";
        } catch (error) { failure = String(error?.message || error); }
        if (failure || !current() || state.nativeShare !== native) {
            if (studioShare?.stream === stream) studioShare = null;
            if (state.nativeShare === native) await doStopShare();
            if (failure && current()) V().sysMsg(tLabel("desktop.screen.share.permission.failed") + failure);
            return false;
        }
        sync();
        startShareStatus({ stream, pc, scope: captureMediaScope(), preset: p, surface, audioMode: native.audioMode,
            generation: native.generation,
            stop: () => { V().$("voice-screen").focus(); void doStopShare(); },
            change: () => studio ? studio.change() : openShareDialog(true, preset, native.audioMode, surface, custom, bitrateCeiling),
            changeQuality: () => studio ? studio.change() : openShareDialog(true, preset, native.audioMode, surface, custom, bitrateCeiling),
            reduction: () => "" });
        return true;
    }

    // startNativeStudioShare publishes a Studio program composed in Go (the
    // native D3D11 compositor) through the native share path. scene is the program as studio-native.js
    // builds it; later Takes go through updateNativeStudio.
    async function startNativeStudioShare({ width, height, fps, scene, onEnded = () => {}, change = () => {} }) {
        const { state } = V();
        if (updateStartupBlocked()) return false;
        if (!state.pc || state.pc.connectionState === "closed" || state.pc.connectionState === "failed") throw new Error(tLabel("share.connectionRequired"));
        if (state.shareStarting || state.shareStopping || state.screenSharing) throw new Error(tLabel("studio.shareBusy"));
        const request = {};
        state.shareStarting = request;
        syncShareButton();
        try {
            return !!await startNativeShare({ surface: "studio", source: { monitor: 0, window: "0", studio: true }, preset: "custom",
                custom: { width, height, fps }, studio: { scene, onEnded, change } });
        } finally {
            if (state.shareStarting === request) { state.shareStarting = null; syncShareButton(); }
        }
    }

    // updateNativeStudio sends a Take (or new overlays of the live Take) to
    // the native Studio share, in order, to the share's own tab.
    function updateNativeStudio(scene) {
        const native = V().state.nativeShare;
        if (!native?.studio) return false;
        native.scenes = native.scenes.then(() => window.go.main.App.UpdateNativeStudioForTab(native.tabID, native.id, scene)).catch(() => {});
        return true;
    }

    // Go captured other audio than requested: application audio fell back to
    // system audio (minus noXa), or no audio could be captured at all.
    function applyNativeAudioNotice(native, reason) {
        native.audioMode = reason === "system" ? "system" : "none";
        V().toast(tLabel(reason === "system" ? "share.nativeAudioSystem" : "share.nativeAudioUnavailable"), "warn");
    }

    // Go reports the native pipeline's own lifecycle: window closed, a failure
    // it could not recover from (with the reason), minimized source (the last
    // frame is held).
    window.runtime?.EventsOn?.("native_share", payload => {
        let event = payload;
        if (typeof payload === "string") { try { event = JSON.parse(payload); } catch { event = null; } }
        const { state } = V();
        if ((event?.state === "ended" || event?.state === "failed") && nativeEventIsCurrent(event, nativePending)) {
            nativePending.ended = true;
            if (event.state === "failed") nativePending.failure = event.reason || "unknown";
            return;
        }
        if (event?.state === "audio" && nativeEventIsCurrent(event, nativePending)) { nativePending.audio = event.reason || "none"; return; }
        // A delayed event of an older share or another tab never touches this one.
        if (!event || !nativeEventIsCurrent(event, state.nativeShare)) return;
        if (event.state === "minimized") V().toast(tLabel("share.nativeMinimized"), "warn");
        else if (event.state === "camera") V().toast(tLabel(event.reason === "busy" ? "studio.nativeCameraBusy" : "studio.nativeCameraUnavailable"), "warn");
        else if (event.state === "audio") applyNativeAudioNotice(state.nativeShare, event.reason);
        else if (event.state === "ended") void doStopShare();
        else if (event.state === "failed") {
            // No other capture path: the share (and a Studio with it) ends with the reason.
            void doStopShare();
            V().toast(tLabel(nativeFailureKey(event.reason)), "error");
        }
    });

    function studioShareActive() {
        return !!studioShare && V().state.screenSharing;
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
        // A native screen share's RTCP reaches Go: only the camera has local evidence.
        const sender = what === "screen" ? null : videoSender();
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
        // A native share ends in Go even when another path already reset the share state.
        const native = state.nativeShare;
        state.nativeShare = null;
        if (native) void Promise.resolve().then(() => window.go.main.App.StopNativeScreenShareForTab(native.tabID, native.id)).catch(() => {});
        if (!state.screenSharing) return;
        const pc = state.pc;
        const generation = state.serverGeneration;
        const tabID = state.activeTabID;
        const current = () => state.pc === pc && state.serverGeneration === generation && state.activeTabID === tabID;
        const studio = studioShare;
        studioShare = null;
        state.shareStopping = true;
        state.screenSharing = false;
        syncShareButton();
        if (state.shareStream) {
            stopDisplayCapture(state.shareStream);
            state.shareStream = null;
        }
        syncCameraButton();
        // The Studio owns its captures: a stopped program ends the Studio.
        try { studio?.onEnded(); } catch { /* the share stop proceeds */ }
        try {
            await stopPublication("screen");
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
        studioShare = null;
        stopShareStatus();
        cameraOff = true;
        cameraRequest?.controller?.abort();
        cameraRequest = null;
        syncCameraButton();
        syncShareButton();
    }

    // cameraToggle is the voice-video button handler.
    async function cameraToggle() {
        if (updateStartupBlocked()) return;
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
            rememberVideoSenderProfile(cam, capturePreferences.get(cam));
            if (!trackFitsVideoLimits(cam, state.mediaLimits)) throw new Error(tLabel("voice.mediaDimensionsFailed"));
            cam.contentHint = "motion";
            if (!await preparePublicationUpload("cam", cam, options => applyPublicationSettings("cam", cam, options))) return;
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
                                sendEncodings: [{ active: publicationUploadActive("cam", cam) !== false }],
                            });
                        } catch {
                            transceiver = pc.addTransceiver(cam, { direction: "sendrecv", streams: [localStream] });
                        }
                        sender = transceiver.sender;
                    }
                    pendingVideoSenders.set(pc, { sender, track: cam });
                    await applySendCaps();
                    if (!current()) throw new DOMException("capture ended", "AbortError");
                    if (!trackFitsVideoLimits(cam, state.mediaLimits)) throw new Error(tLabel("voice.mediaDimensionsFailed"));
                    await sender.replaceTrack(cam);
                    await negotiateInitialCodec("cam", cam, pc.getTransceivers().find(item => item.sender === sender), pc, generation, current, tabID);
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

    return { applyVideoLimits, shareToggle, resetCameraState, cameraToggle, applySendCaps, retryVideoHardware, syncCameraButton, syncShareButton, startNativeStudioShare, updateNativeStudio, studioShareActive, stopShare: doStopShare,
        endShareForReconnect, updateShareSettings, openResumedShare, openCallShareDialog };
}
