import { updateStartupBlocked } from "./update-transition.js";
import { retainDisplayCapture, releaseDisplayCapture, stopDisplayCapture } from "./display-capture-lease.js";
// video-publication.js — camera/screen capture and publication lifecycle.
import { captureCamera, applyCameraPreview } from "./camera-capture.js";
import { captureMediaScope, mediaScopeIsCurrent } from "./media-controls.js";
import { startPublication, stopPublication, publicationSnapshot, preparePublicationUpload, publicationUploadActive, publicationCodec, publicationHEVCFormat, withdrawPublicationHEVC } from "./stream-publication.js";
import { hardwareRecoveryCodec, knownSoftwareVideoEncoder } from "./video-hevc.js";
import { preferPublicationCodec } from "./video-codecs.js";
import { startShareStatus, stopShareStatus, refreshShareStatus } from "./share-status.js";
import { displayAudioOptions, validateDisplayAudio } from "./display-audio.js";
import { applyVideoNetworkPriority } from "./voice-priority.js";
import { shareQuality, screenShareConstraints, createShareQualityControls, DEFAULT_SHARE_PRESET } from "./screen-share-quality.js";
import { createSharePicker } from "./share-picker.js";
import { cropDisplayStream, regionCropSupported } from "./share-region.js";
import { closeDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { labelButton } from "./icons.js";
import { renderMicStatus } from "./audio.js";
import { videoConstraints, trackFitsVideoLimits, capVideoEncodings, screenBitrateHeadroom } from "./media-limits.js";
import { t as tLabel } from "./i18n.js";
import { rememberVideoSenderProfile } from "./video-sender-stats.js";
import { hardwareVideoEncodeAvailable, videoSenderCaptureSample } from "./video-sender-hardware-recovery.js";
import { sampleSenderStats } from "./peer-stats.js";
import { createScreenStartupAttempt } from "./video-startup-plan.js";
import { renegotiate, negotiateOffer, queuePeerNegotiation } from "./video-negotiation.js";
const V = () => window.__noxa;

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
    const codecSelections = new WeakMap();
    const startupAttempt = createScreenStartupAttempt();

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
            (slot === "screen" ? state.shareStream?.getVideoTracks()[0] === track : state.localStream?.getVideoTracks().includes(track));
        return queuePeerNegotiation(pc, async () => {
            if (!current()) return;
            const transceiver = slot === "screen" ? state.shareVideoTransceiver : pc.getTransceivers().find(item => item.sender.track === track);
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
            if (slot === "screen" && current() && !policy.lowBandwidth && publicationUploadActive(slot, track) === true) {
                const seedProfile = capturePreferences.get(track), seedLimits = state.mediaLimits;
                const seedBudget = seedLimits?.video_max_bitrate ?? 0;
                const otherVideoActive = () => pc.getSenders().some(sender => sender !== transceiver.sender &&
                    sender.track?.kind === "video" && sender.track.readyState === "live" &&
                    sender.getParameters().encodings?.some(encoding => encoding.active !== false));
                const seedCurrent = () => current() && publicationUploadActive(slot, track) === true &&
                    track.enabled && !transceiver.stopped && transceiver.sender.track === track &&
                    !policy.lowBandwidth && !state.shareQualityUpdating && capturePreferences.get(track) === seedProfile &&
                    state.mediaLimits === seedLimits && (state.mediaLimits?.video_max_bitrate ?? 0) === seedBudget &&
                    !otherVideoActive() && transceiver.sender.getParameters().encodings?.[0]?.active === false;
                const plan = await startupAttempt({ pc, transceiver, track, profile: seedProfile,
                    settings: track.getSettings(), limits: seedLimits, current: seedCurrent, otherVideoActive });
                if (plan && seedCurrent()) {
                    const profileKey = value => JSON.stringify([value?.width, value?.height, value?.fps, value?.bitrateCeiling ?? 0]);
                    const limitKey = value => JSON.stringify([value?.video_max_width ?? 0, value?.video_max_height ?? 0, value?.video_max_bitrate ?? 0]);
                    const ownedProfile = profileKey(seedProfile), ownedLimits = limitKey(seedLimits), ownedHint = track.contentHint;
                    // Demand pauses and additional viewers retain the identical
                    // codec parameter. Actual source, codec or cap changes end it.
                    const stillOwned = () => current() && state.shareVideoTransceiver === transceiver &&
                        !transceiver.stopped && transceiver.sender.track === track && track.contentHint === ownedHint && !policy.lowBandwidth &&
                        profileKey(capturePreferences.get(track)) === ownedProfile && limitKey(state.mediaLimits) === ownedLimits;
                    // Start-rate hints are consumed by the existing transport even
                    // while this encoding is paused. Activate only after the answer.
                    // A rejected optional hint must not prevent ordinary streaming.
                    try { await negotiateOffer(pc, scope.generation, undefined, current, scope.tabID,
                        { ...plan, stillEligible: seedCurrent, stillOwned }); }
                    catch { /* negotiateOffer rolls back; ordinary caps remain authoritative. */ }
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
            const screenSender = state.shareVideoTransceiver?.sender;
            const screenTrack = state.shareStream?.getVideoTracks()[0];
            const sources = [videoSenderFor(pc), screenSender].filter(sender => sender &&
                (sender === screenSender && screenTrack?.readyState === "live" ||
                    sender === pendingVideoSenders.get(pc)?.sender || (sender.track && sender.track.readyState !== "ended"))).map(sender => {
                const parameters = sender.getParameters();
                if (sender === screenSender) {
                    // Motion must be able to trade pixels for frames under CPU or
                    // network pressure. Only explicit text/detail shares keep pixels.
                    parameters.degradationPreference = ["text", "detail"].includes(screenTrack?.contentHint) ? "maintain-resolution" : "maintain-framerate";
                }
                for (const encoding of parameters.encodings || []) {
                    encoding.priority = sender === screenSender ? "high" : "medium";
                    applyVideoNetworkPriority(encoding); // never in the voice class
                    if (sender === screenSender) {
                        const fps = capturePreferences.get(screenTrack)?.fps;
                        if (fps > 0) encoding.maxFramerate = fps;
                    }
                }
                return { sender, parameters, encodings: parameters.encodings || [],
                    singleEncoding: true,
                    uploadActive: publicationUploadActive(sender === screenSender ? "screen" : "cam",
                        sender === screenSender ? screenTrack : pendingVideoSenders.get(pc)?.sender === sender ? pendingVideoSenders.get(pc).track : sender.track),
                    bitrateHeadroom: sender === screenSender ? screenBitrateHeadroom(capturePreferences.get(screenTrack), screenTrack?.getSettings()) : Infinity };
            });
            capVideoEncodings(sources, state.mediaLimits, policy.lowBandwidth ? policy.lowBandwidthBitrate : 0);
            for (const { sender, parameters, encodings } of sources) {
                if (!current()) return;
                if (encodings.length) await sender.setParameters(parameters);
            }
            const audioSender = state.shareAudioTransceiver?.sender;
            if (audioSender && (audioSender.track || state.shareStream?.getAudioTracks().some(track => track.readyState === "live")) && current()) {
                const parameters = audioSender.getParameters();
                const active = publicationUploadActive("screen", state.shareStream?.getVideoTracks()[0]) !== false;
                for (const encoding of parameters.encodings || []) { encoding.active = active; encoding.priority = "high"; }
                if (parameters.encodings?.length) await audioSender.setParameters(parameters);
            }
        });
        capUpdates.set(pc, pending);
        pending.catch(() => { if (current()) V().sysMsg(tLabel("voice.mediaCapsFailed")); });
        return pending;
    }

    // A ceiling-only change owns the same cap queue, but never rewrites camera,
    // audio, capture or adaptation parameters. Compute shared limits on copies.
    function applyScreenBitrate(track, profile, stillRelevant) {
        const { state } = V(), pc = state.pc, scope = captureMediaScope();
        const stream = state.shareStream, transceiver = state.shareVideoTransceiver, sender = transceiver?.sender;
        const current = () => stillRelevant() && mediaScopeIsCurrent(scope) && state.pc === pc &&
            state.shareStream === stream && state.shareVideoTransceiver === transceiver && sender?.track === track &&
            stream?.getVideoTracks()[0] === track && track.readyState === "live";
        const pending = (capUpdates.get(pc) || Promise.resolve()).catch(() => {}).then(async () => {
            if (!current()) return false;
            if (publicationUploadActive("screen", track) === false) {
                capturePreferences.set(track, { ...profile, screen: true });
                return true;
            }
            const parameters = sender.getParameters(), original = parameters.encodings?.map(encoding => ({ ...encoding }));
            if (!original?.length) throw new Error(tLabel("voice.mediaCapsFailed"));
            const limited = budget => {
                const encodings = original.map(encoding => ({ ...encoding }));
                const sources = [{ encodings, singleEncoding: true,
                    bitrateHeadroom: screenBitrateHeadroom(budget, track.getSettings()) }];
                const camera = videoSenderFor(pc), waiting = pendingVideoSenders.get(pc);
                const cameraTrack = camera === waiting?.sender ? waiting.track : camera?.track;
                if (cameraTrack?.readyState === "live") sources.push({ encodings: [],
                    uploadActive: publicationUploadActive("cam", cameraTrack) });
                capVideoEncodings(sources, state.mediaLimits, policy.lowBandwidth ? policy.lowBandwidthBitrate : 0);
                return encodings;
            };
            const encodings = limited(profile);
            if (encodings.some((encoding, i) => (encoding.active !== false) !== (original[i].active !== false))) throw new Error(tLabel("voice.mediaCapsFailed"));
            parameters.encodings.forEach((encoding, i) => { encoding.maxBitrate = encodings[i].maxBitrate; });
            try {
                if (original.some((encoding, i) => encoding.maxBitrate !== encodings[i].maxBitrate)) await sender.setParameters(parameters);
            } catch (error) {
                if (!current()) return false;
                let restored = true;
                try {
                    const rollback = sender.getParameters();
                    if (rollback.encodings?.length !== original.length || rollback.encodings.some((encoding, i) => encoding.rid !== original[i].rid)) throw error;
                    // A server limit may change while setParameters is pending.
                    // Restore the previous preference without exceeding fresh caps.
                    const bounded = limited(capturePreferences.get(track));
                    if (bounded.some((encoding, i) => (encoding.active !== false) !== (original[i].active !== false))) throw error;
                    rollback.encodings.forEach((encoding, i) => {
                        encoding.maxBitrate = Math.min(original[i].maxBitrate ?? Infinity, bounded[i].maxBitrate);
                    });
                    await sender.setParameters(rollback);
                } catch { restored = false; }
                throw new Error(tLabel(restored ? "share.qualityFailed" : "share.qualityRestoreFailed", { error: error.message || String(error) }));
            }
            if (!current()) return false;
            capturePreferences.set(track, { ...profile, screen: true });
            return true;
        });
        capUpdates.set(pc, pending);
        return pending;
    }

    // Apply the same screen ceiling before the first offer, including when the
    // source starts paused. A live camera still shares any stricter server cap.
    function initialScreenEncodings(track) {
        const { state } = V(), pc = state.pc, profile = capturePreferences.get(track);
        const encodings = [{}], sources = [{ encodings, singleEncoding: true,
            bitrateHeadroom: screenBitrateHeadroom(profile, track.getSettings()) }];
        const camera = videoSenderFor(pc), pending = pendingVideoSenders.get(pc);
        const cameraTrack = camera === pending?.sender ? pending.track : camera?.track;
        if (cameraTrack?.readyState === "live") sources.push({ encodings: [],
            uploadActive: publicationUploadActive("cam", cameraTrack) });
        capVideoEncodings(sources, state.mediaLimits, policy.lowBandwidth ? policy.lowBandwidthBitrate : 0);
        encodings[0].active = encodings[0].active && publicationUploadActive("screen", track) !== false;
        encodings[0].priority = "high";
        applyVideoNetworkPriority(encodings[0]);
        if (profile?.fps > 0) encodings[0].maxFramerate = profile.fps;
        return encodings;
    }

    // WebRTC reinitializes its encoder on a size change, retrying hardware first.
    // A bounded scale-only pulse keeps capture, codec, adaptation and one upload.
    // Explicit scale applies to already-adapted input; capture/output ratios do
    // not reveal that native input and must never become a replacement scale.
    // Capability queries alone do not guarantee hardware on every runtime; the
    // recovery scheduler must confirm the result using actual encoder stats.
    function retryVideoHardware(slot, track, stillRelevant = () => true, captureRecovery = null) {
        const { state } = V(), pc = state.pc, scope = captureMediaScope();
        const transceiver = slot === "screen" ? state.shareVideoTransceiver : pc?.getTransceivers().find(item => item.sender.track === track);
        const sender = transceiver?.sender;
        if (!pc || !sender || !["screen", "cam"].includes(slot)) return Promise.resolve(false);
        const owns = () => state.pc === pc && mediaScopeIsCurrent(scope) && !transceiver.stopped && sender.track === track &&
            track?.readyState === "live" && (slot === "screen" ? state.shareVideoTransceiver === transceiver &&
                state.shareStream?.getVideoTracks()[0] === track : state.localStream?.getVideoTracks().includes(track));
        const current = () => owns() && track.enabled && !state.shareQualityUpdating && stillRelevant() &&
            publicationUploadActive(slot, track) !== false;
        return queuePeerNegotiation(pc, () => {
            const pending = (capUpdates.get(pc) || Promise.resolve()).catch(() => {}).then(async () => {
                if (!current() || !navigator.mediaCapabilities?.encodingInfo) return false;
                const sample = async () => {
                    const reports = await sampleSenderStats(sender, { fresh: true });
                    const video = [...reports.values()].filter(row => row.type === "outbound-rtp" && !row.isRemote &&
                        (row.kind === "video" || row.mediaType === "video"));
                    return video.length === 1 ? { ...video[0], codec: reports.get(video[0].codecId),
                        capture: videoSenderCaptureSample(reports, video[0], track) } : null;
                };
                const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
                const first = await sample();
                const parameters = sender.getParameters(), encoding = parameters.encodings?.[0];
                const scale = encoding?.scaleResolutionDownBy ?? 1;
                const size = track.getSettings();
                const eligible = row => row && row.powerEfficientEncoder === false && hardwareRecoveryCodec(row.codec) && (row.codec.mimeType.toLowerCase() === "video/h264" || knownSoftwareVideoEncoder(row.codec, row.encoderImplementation)) &&
                    ["none", "cpu", "bandwidth"].includes(row.qualityLimitationReason) && row.frameWidth >= 640 && row.frameHeight >= 360 &&
                    row.frameWidth % 4 === 0 && row.frameHeight % 4 === 0 &&
                    Number.isFinite(size.width) && Number.isFinite(size.height) &&
                    row.frameWidth * scale <= size.width && row.frameHeight * scale <= size.height &&
                    row.frameWidth * size.height === row.frameHeight * size.width;
                // Only the publication scheduler's previously measured hardware
                // configuration authorizes capture progress to replace output.
                // Revalidate the bound source on fresh reads inside this queue.
                const capturedProgress = (before, after) => captureRecovery &&
                    [before, after].every(row => row?.capture?.id === captureRecovery.sourceId &&
                        row.codec?.mimeType === captureRecovery.codec && row.codec?.sdpFmtpLine === captureRecovery.fmtp &&
                        row.frameWidth === captureRecovery.width && row.frameHeight === captureRecovery.height &&
                        knownSoftwareVideoEncoder(row.codec, row.encoderImplementation) &&
                        Number.isSafeInteger(row.framesEncoded) && row.framesEncoded >= 0) &&
                    after.framesEncoded >= before.framesEncoded && after.capture.frames > before.capture.frames &&
                    after.capture.timestamp > before.capture.timestamp && after.capture.timestamp - before.capture.timestamp <= 15000;
                if (!current() || parameters.encodings?.length !== 1 || encoding.active === false || !Number.isFinite(scale) || scale < 1 || !eligible(first)) return false;
                // The shared bounded cache retains hung native queries; a later
                // retry must not launch additional work for the same geometry.
                const supported = await Promise.all([1, 2].map(divisor => hardwareVideoEncodeAvailable(first.codec, {
                    frameWidth: first.frameWidth / divisor, frameHeight: first.frameHeight / divisor,
                    framesPerSecond: size.frameRate || 30,
                })));
                if (supported.some(value => !value)) return false;
                await pause(150);
                const baseline = await sample();
                if (!current() || !eligible(baseline) || baseline.id !== first.id || baseline.codecId !== first.codecId || baseline.ssrc !== first.ssrc ||
                    baseline.timestamp <= first.timestamp ||
                    !(baseline.framesEncoded > first.framesEncoded || capturedProgress(first, baseline)) ||
                    baseline.frameWidth !== first.frameWidth || baseline.frameHeight !== first.frameHeight ||
                    track.getSettings().width !== size.width || track.getSettings().height !== size.height) return false;
                const originalScale = encoding.scaleResolutionDownBy;
                const pulseScale = scale * 2;
                let pulsed = false;
                try {
                    await pause(150);
                    const ready = await sample();
                    if (!current() || !eligible(ready) || ready.id !== first.id || ready.codecId !== first.codecId || ready.ssrc !== first.ssrc ||
                        ready.timestamp <= baseline.timestamp || ready.framesEncoded < baseline.framesEncoded ||
                        ready.frameWidth !== first.frameWidth || ready.frameHeight !== first.frameHeight ||
                        (!(baseline.framesEncoded > first.framesEncoded) &&
                            (ready.timestamp <= baseline.timestamp || !capturedProgress(baseline, ready))) ||
                        track.getSettings().width !== size.width || track.getSettings().height !== size.height) return false;
                    const pulse = sender.getParameters();
                    if (pulse.encodings?.length !== 1 || (pulse.encodings[0].scaleResolutionDownBy ?? 1) !== scale || pulse.encodings[0].active === false) return false;
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
                            row.frameWidth === first.frameWidth / 2 && row.frameHeight === first.frameHeight / 2 &&
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
                            // Do not race mutation promises: a runtime that never
                            // settles setParameters cannot safely accept later writes.
                            await sender.setParameters(restore);
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

    // Region crops a picked screen or window frame by frame (share-region.js).
    const regionSupported = regionCropSupported();

    let shareDialogID = 0;
    // The Studio program currently published on the screen slot, if any.
    let studioShare = null;

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
        if (updateStartupBlocked()) return;
        const { state } = V();
        if (state.shareStarting || state.shareStopping) return;
        if (state.screenSharing) {
            await confirmStopShare();
            return;
        }
        openShareDialog();
    }

    function openShareDialog(replacing = false, initialPreset = DEFAULT_SHARE_PRESET, initialAudio = "none", initialSurface = "monitor", custom = {}, bitrateCeiling = 0) {
        const dialogID = ++shareDialogID;
        const overlay = document.createElement("div");
        overlay.className = "dlg-overlay";
        const view = createSharePicker({ id: `share-${dialogID}`, preset: initialPreset, custom, bitrateCeiling,
            surface: initialSurface, audio: initialAudio, regionSupported });
        overlay.appendChild(view.element);
        const { quality, $ } = view;
        const sources = () => overlay.querySelectorAll(`input[name="${view.sourceName}"]`);
        const audioChoices = new Map();
        if (replacing) audioChoices.set(initialSurface,
            V().state.shareStream?.getAudioTracks().some(track => track.readyState === "live") ? initialAudio : "none");
        // Keep explicit choices per source so toggling sources cannot undo an override.
        $(".sh-audio").addEventListener("change", () => { audioChoices.set(view.surface(), view.audio()); discardSelection(); });
        const syncAudioSource = () => {
            const surface = view.surface();
            const application = surface === "window";
            view.setApplicationAudio(application);
            let audio = audioChoices.get(surface) ?? (application ? "application" : "none");
            if (!application && audio === "application") audio = "none";
            view.setAudio(audio);
        };
        let selectedDisplay = null, picking = false, publishing = false, pickToken = 0;
        const error = $(".sh-error");
        const choosers = [$(".sh-choose"), $(".sp-change")];
        const start = $(".dlg-ok"), cancel = $(".dlg-cancel"), close = $(".sp-close");
        const update = () => {
            for (const button of choosers) button.disabled = picking || publishing;
            cancel.disabled = close.disabled = publishing;
            start.disabled = picking || publishing || !selectedDisplay || (view.surface() === "region" && !view.region());
            for (const input of sources()) input.disabled = publishing || (input.value === "region" && !regionSupported);
            $(".sh-audio").disabled = publishing; quality.element.inert = publishing;
        };
        view.onRegionChange(update);
        const discardSelection = () => {
            ++pickToken;
            stopDisplayCapture(selectedDisplay); selectedDisplay = null;
            view.clearPreview();
            update();
        };
        const choose = async () => {
            if (picking || publishing || !isCurrentServerDialog(overlay)) return;
            const selection = quality.read();
            if (!selection) { view.closePanels(); $(".sp-quality-toggle").click(); quality.read(); return; }
            discardSelection(); const token = pickToken;
            error.textContent = ""; picking = true; update();
            const surface = view.surface();
            const profile = shareQuality(selection.preset, selection.custom, selection.bitrateCeiling);
            // Region crops afterwards, so it captures at the source's own size.
            const video = surface === "region"
                ? screenShareConstraints({ width: 0, height: 0, fps: profile.fps, original: true }, null)
                : screenShareConstraints(profile, V().state.mediaLimits);
            video.displaySurface = surface === "region" ? "monitor" : surface;
            const options = { video, ...displayAudioOptions(view.audio()) };
            try {
                // Must run directly from the click to retain user activation.
                let display;
                try { display = await navigator.mediaDevices.getDisplayMedia(options); }
                catch (failure) {
                    if (!options.audio || ["NotAllowedError", "AbortError"].includes(failure.name) || !isCurrentServerDialog(overlay) || token !== pickToken) throw failure;
                    display = await navigator.mediaDevices.getDisplayMedia({ ...options, ...displayAudioOptions("none") });
                }
                if (!isCurrentServerDialog(overlay) || token !== pickToken) { stopDisplayCapture(display); return; }
                const track = display.getVideoTracks()[0];
                if (!track || track.readyState === "ended") { stopDisplayCapture(display); throw new Error(tLabel("desktop.screen.capture.produced.no.video.track")); }
                selectedDisplay = display; retainDisplayCapture(display);
                view.showPreview(display, track.label || tLabel(surface === "window" ? "polish.window" : "polish.monitor"));
                track.addEventListener("ended", () => { if (selectedDisplay === display) { discardSelection(); error.textContent = tLabel("share.sourceEnded"); } }, { once: true });
            } catch (failure) {
                if (isCurrentServerDialog(overlay) && token === pickToken) error.textContent = tLabel("desktop.screen.capture.failed") + (failure.message || failure.name);
            } finally { picking = false; if (isCurrentServerDialog(overlay)) update(); }
        };
        for (const button of choosers) button.onclick = choose;
        for (const source of sources()) source.addEventListener("change", () => { syncAudioSource(); discardSelection(); });
        syncAudioSource(); update();
        cancel.onclick = close.onclick = () => closeDialog(overlay, "cancel");
        overlay.onclick = (event) => { if (event.target === overlay && !publishing) closeDialog(overlay, "cancel"); };
        start.onclick = async () => {
            if (!isCurrentServerDialog(overlay) || publishing || !selectedDisplay) return;
            const selected = quality.read();
            if (!selected) { view.closePanels(); $(".sp-quality-toggle").click(); quality.read(); return; }
            const surface = view.surface(), region = view.region();
            if (surface === "region" && !region) return;
            const { state } = V();
            if (!state.pc || state.pc.connectionState === "closed" || state.pc.connectionState === "failed") { error.textContent = tLabel("share.connectionRequired"); return; }
            if (state.shareStarting || state.shareStopping || (!!state.screenSharing !== replacing)) { error.textContent = tLabel("share.stateChanged"); return; }
            let display = selectedDisplay; selectedDisplay = null;
            view.closePanels();
            const request = {}; state.shareStarting = request;
            publishing = true; error.textContent = ""; update(); syncShareButton();
            try {
                if (surface === "region") {
                    // The cropped track owns the capture from here; its own lease
                    // replaces the picker's one in startShare.
                    const cropped = cropDisplayStream(display, region);
                    releaseDisplayCapture(display);
                    display = cropped.stream;
                }
                const started = await startShare({ surface, ...selected, audioMode: view.audio(), replacing, selectedDisplay: display });
                if (started) closeDialog(overlay, "started");
                else if (isCurrentServerDialog(overlay)) { view.clearPreview(); error.textContent = tLabel("share.startFailed"); }
            } catch (failure) {
                stopDisplayCapture(display);
                if (isCurrentServerDialog(overlay)) { view.clearPreview(); error.textContent = tLabel("desktop.screen.capture.failed") + (failure.message || failure.name); }
            } finally {
                publishing = false;
                if (state.shareStarting === request) { state.shareStarting = null; syncShareButton(); }
                if (isCurrentServerDialog(overlay)) update();
            }
        };
        mountServerDialog(overlay, { onCancel: () => !publishing, onClose: () => { view.dispose(); discardSelection(); } });
        const hint = mediaLimitHint();
        if (hint) {
            const note = document.createElement("p");
            note.className = "set-hint media-limit-hint";
            note.textContent = hint;
            $(".sp-notes").appendChild(note);
        }
        if (!regionSupported) {
            // The tab is disabled, so the dialog has to say why.
            const note = document.createElement("p");
            note.className = "set-hint warn";
            note.textContent = tLabel("polish.cropNeedsUpdate");
            $(".sp-notes").appendChild(note);
        }
    }

    // startShare captures the display (69: displaySurface preference — Chromium's
    // picker ultimately decides what is shareable), publishes a dedicated screen
    // track independently of the camera, optionally merges display
    // audio (70), and applies the quality preset (72).
    async function startShare({ surface, preset, custom, bitrateCeiling = 0, audioMode, replacing = false, selectedDisplay = null, studio = null }) {
        const requestedAudioMode = audioMode;
        const { state } = V();
        const generation = state.serverGeneration;
        const tabID = state.activeTabID;
        const peerConnection = state.pc;
        if (!peerConnection) { stopDisplayCapture(selectedDisplay); V().toast(tLabel("share.connectionRequired"), "warn"); return; }
        const shareScope = captureMediaScope();
        const current = () => mediaScopeIsCurrent(shareScope) && state.serverGeneration === generation && state.activeTabID === tabID && state.pc === peerConnection;
        let shareTransceiver = null;
        const discardDisplay = (stream) => {
            stopDisplayCapture(stream);
            shareTransceiver?.stop();
            if (state.shareVideoTransceiver === shareTransceiver) state.shareVideoTransceiver = null;
            if (stream?.getTracks().includes(state.shareAudioSender?.track)) {
                state.shareAudioTransceiver?.stop();
                state.shareAudioTransceiver = null;
                state.shareAudioSender = null;
            }
        };
        const previousDisplay = state.shareStream;
        const p = shareQuality(preset, custom, bitrateCeiling);
        if (!p) { stopDisplayCapture(selectedDisplay); return; }
        const selection = { preset, custom, bitrateCeiling };
        const limits = state.mediaLimits;
        const video = screenShareConstraints(p, limits);
        video.displaySurface = surface === "region" ? "monitor" : surface; // 69: "monitor" | "window"
        const gdm = { video, ...displayAudioOptions(audioMode) };
        let display;
        try {
            display = selectedDisplay || await navigator.mediaDevices.getDisplayMedia(gdm);
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
        retainDisplayCapture(display);
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
        if (selectedDisplay) {
            try { await screenTrack.applyConstraints(screenShareConstraints(p, limits)); }
            catch (error) { discardDisplay(display); V().sysMsg(tLabel("desktop.screen.capture.failed") + error.message); return; }
            if (!current() || screenTrack.readyState === "ended") { discardDisplay(display); return; }
        }
        capturePreferences.set(screenTrack, { ...p, screen: true });
        rememberVideoSenderProfile(screenTrack, p);
        screenTrack.contentHint = preset === "text" ? "text" : "motion";
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
        };

        if (!trackFitsVideoLimits(screenTrack, state.mediaLimits)) {
            discardDisplay(display);
            V().sysMsg(tLabel("voice.mediaDimensionsFailed"));
            return;
        }
        // Keep the current source alive while the picker is open or cancelled.
        if (replacing) {
            if (state.shareStream !== previousDisplay || !state.screenSharing) { discardDisplay(display); return; }
            await doStopShare();
            if (!current() || shareEnded || screenTrack.readyState === "ended") { discardDisplay(display); return; }
        }
        state.shareStream = display;
        if (peerConnection) {
            // The dedicated screen transceiver survives publication stops.
            try {
                if (!await preparePublicationUpload("screen", screenTrack, options => applyPublicationSettings("screen", screenTrack, options))) throw new DOMException("capture ended", "AbortError");
                await queuePeerNegotiation(peerConnection, async () => {
                    if (!current() || shareEnded) throw new DOMException("capture ended", "AbortError");
                    try {
                        if (!trackFitsVideoLimits(screenTrack, state.mediaLimits)) throw new Error(tLabel("voice.mediaDimensionsFailed"));
                        shareTransceiver = state.shareVideoTransceiver;
                        if (shareTransceiver) {
                            // A reused sender can still be active from the previous
                            // publication. Pause it before attaching the new source.
                            await applySendCaps(current);
                            if (!current() || shareEnded) throw new DOMException("capture ended", "AbortError");
                            await shareTransceiver.sender.replaceTrack(screenTrack);
                        }
                        else {
                            try {
                                shareTransceiver = peerConnection.addTransceiver(screenTrack, { direction: "sendonly", streams: [display],
                                    sendEncodings: initialScreenEncodings(screenTrack) });
                            } catch {
                                shareTransceiver = peerConnection.addTransceiver(screenTrack, { direction: "sendonly", streams: [display] });
                            }
                        }
                        state.shareVideoTransceiver = shareTransceiver;
                        await applySendCaps();
                        if (!current() || shareEnded) throw new DOMException("capture ended", "AbortError");
                        if (!trackFitsVideoLimits(screenTrack, state.mediaLimits)) throw new Error(tLabel("voice.mediaDimensionsFailed"));
                        await negotiateInitialCodec("screen", screenTrack, shareTransceiver, peerConnection, generation, () => !shareEnded && state.pc === peerConnection && state.shareStream === display, tabID);
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
                    tr = peerConnection.addTransceiver(displayAudio, { direction: "sendonly", streams: [display],
                        sendEncodings: [{ active: publicationUploadActive("screen", screenTrack) !== false }] });
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
                    await applySendCaps(current);
                    if (!current() || shareEnded) { discardDisplay(display); return; }
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
        studioShare = studio ? { stream: display, onEnded: studio.onEnded } : null;
        syncShareButton();
        if (requestedAudioMode !== "none" && audioMode === "none") {
            V().toast(tLabel(requestedAudioMode === "application" ? "share.applicationUnavailable" : "share.audioNotSelected"), "warn");
        }
        startShareStatus({ stream: display, pc: peerConnection, scope: shareScope, preset: p, surface, audioMode,
            generation: publicationSnapshot().find(p => p.publication.slot === "screen")?.generation,
            stop: () => { V().$("voice-screen").focus(); void doStopShare(); },
            change: () => studio ? studio.change() : openShareDialog(true, selection.preset, audioMode, surface, selection.custom, selection.bitrateCeiling),
            changeQuality: () => openLiveShareQuality(display, peerConnection, shareScope, p, selection),
            reduction: () => policy.lowBandwidth ? "share.lowBandwidth" : "" });
        return true;
    }

    // startStudioShare publishes the Studio's stable program stream through
    // the same guarded screen publication as an ordinary share: the media
    // session attached the encrypted transform to the share transceiver, so
    // encryption happens after encode; capture limits, caps, codec policy and
    // stop handling are the ordinary ones. The Studio never touches a sender.
    // A scene change later changes pixels only, never this track.
    async function startStudioShare(stream, { width, height, fps, onEnded = () => {}, change = () => {} }) {
        const { state } = V();
        if (updateStartupBlocked()) return false;
        if (!state.pc || state.pc.connectionState === "closed" || state.pc.connectionState === "failed") throw new Error(tLabel("share.connectionRequired"));
        if (state.shareStarting || state.shareStopping || state.screenSharing) throw new Error(tLabel("studio.shareBusy"));
        const request = {};
        state.shareStarting = request;
        syncShareButton();
        try {
            return !!await startShare({ surface: "studio", preset: "custom", custom: { width, height, fps }, bitrateCeiling: 0,
                audioMode: stream.getAudioTracks().length ? "system" : "none", selectedDisplay: stream, studio: { onEnded, change } });
        } finally {
            if (state.shareStarting === request) { state.shareStarting = null; syncShareButton(); }
        }
    }

    function studioShareActive(stream) {
        return !!studioShare && V().state.screenSharing && (!stream || studioShare.stream === stream);
    }

    function openLiveShareQuality(stream, pc, scope, profile, selection) {
        const { state } = V();
        const track = stream.getVideoTracks()[0];
        const current = () => mediaScopeIsCurrent(scope) && state.pc === pc && state.shareStream === stream && track.readyState === "live";
        if (!current() || state.shareQualityUpdating) return;
        const overlay = document.createElement("div");
        overlay.className = "dlg-overlay";
        overlay.innerHTML = `<div class="dlg share-dlg"><h3>${tLabel("share.changeQuality")}</h3>
            <p class="set-hint">${tLabel("share.qualityHelp")}</p><div class="live-share-quality"></div>
            <p class="live-share-quality-error warn" role="alert" hidden></p>
            <div class="dlg-buttons"><button type="button" class="dlg-cancel">${tLabel("desktop.cancel")}</button>
            <button type="button" class="dlg-ok primary">${tLabel("share.applyQuality")}</button></div></div>`;
        const controls = createShareQualityControls(`live-share-quality-${++shareDialogID}`, selection);
        overlay.querySelector(".live-share-quality").append(controls.element);
        overlay.querySelector(".dlg-cancel").onclick = () => overlay.remove();
        overlay.querySelector(".dlg-ok").onclick = async () => {
            const selected = controls.read(), next = selected && shareQuality(selected.preset, selected.custom, selected.bitrateCeiling);
            if (!next || !current() || state.shareQualityUpdating) return;
            const bitrateOnly = selected.preset === selection.preset &&
                ["width", "height", "fps", "original"].every(key => next[key] === profile[key]);
            if (bitrateOnly && (next.bitrateCeiling || 0) === (profile.bitrateCeiling || 0)) { overlay.remove(); return; }
            const operation = {};
            state.shareQualityUpdating = operation;
            const alive = () => current() && state.shareQualityUpdating === operation;
            overlay.dataset.blocking = "true";
            const disabledStates = [...overlay.querySelectorAll("button, input, select")].map(control => [control, control.disabled]);
            for (const [control] of disabledStates) control.disabled = true;
            refreshShareStatus();
            try {
                await queuePeerNegotiation(pc, async () => {
                    if (!alive()) return;
                    if (bitrateOnly) {
                        if (!await applyScreenBitrate(track, next, alive) || !alive()) return;
                        for (const key of Object.keys(profile)) delete profile[key];
                        Object.assign(profile, next);
                        Object.assign(selection, selected);
                        return;
                    }
                    const previous = track.getConstraints(), hint = track.contentHint, preferred = capturePreferences.get(track);
                    try {
                        await track.applyConstraints({ ...previous, ...screenShareConstraints(next, state.mediaLimits) });
                        if (!alive()) return;
                        if (!trackFitsVideoLimits(track, state.mediaLimits)) throw new Error(tLabel("voice.mediaDimensionsFailed"));
                        capturePreferences.set(track, { ...next, screen: true });
                        rememberVideoSenderProfile(track, next);
                        track.contentHint = selected.preset === "text" ? "text" : "motion";
                        await applySendCaps(alive);
                    } catch (error) {
                        if (!alive()) return;
                        capturePreferences.set(track, preferred);
                        rememberVideoSenderProfile(track, preferred);
                        track.contentHint = hint;
                        let restored = true;
                        try { await track.applyConstraints(previous); if (alive()) await applySendCaps(alive); }
                        catch { restored = false; }
                        throw new Error(tLabel(restored ? "share.qualityFailed" : "share.qualityRestoreFailed", { error: error.message || String(error) }));
                    }
                    if (!alive()) return;
                    for (const key of Object.keys(profile)) delete profile[key];
                    Object.assign(profile, next);
                    Object.assign(selection, selected);
                });
                overlay.remove();
            } catch (error) {
                if (alive() && overlay.isConnected) {
                    const notice = overlay.querySelector(".live-share-quality-error");
                    notice.hidden = false; notice.textContent = error.message || String(error);
                }
            } finally {
                if (state.shareQualityUpdating === operation) state.shareQualityUpdating = null;
                delete overlay.dataset.blocking;
                for (const [control, disabled] of disabledStates) control.disabled = disabled;
                refreshShareStatus();
            }
        };
        mountServerDialog(overlay);
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
        const studio = studioShare;
        studioShare = null;
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
            stopDisplayCapture(state.shareStream);
            state.shareStream = null;
        }
        applySendCaps();
        syncCameraButton();
        // The Studio owns its captures: a stopped program ends the Studio.
        try { studio?.onEnded(); } catch { /* the share stop proceeds */ }
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

    return { applyVideoLimits, shareToggle, resetCameraState, cameraToggle, applySendCaps, retryVideoHardware, syncCameraButton, syncShareButton, startStudioShare, studioShareActive, stopShare: doStopShare };
}
