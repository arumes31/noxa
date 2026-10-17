import { captureMediaScope, mediaScopeIsCurrent } from "./media-controls.js";
import { streamRequest, stopPublications, publicationSnapshot, reconcilePublications, reconcilePublicationUploads, isCurrentPublication } from "./stream-publication.js";
import { t } from "./i18n.js";
import { updateShareViewers } from "./share-status.js";
import { streamRecovery } from "./stream-recovery.js";
import { receiverVideoCodecs } from "./video-codecs.js";
import { receiverHEVCSupport, hevcReadmissionDelay } from "./video-hevc.js";
import { createReceiverRegistry } from "./remote-receivers.js";

const V = () => window.__noxa;
let session = null;
const key = stream => `${stream.publisher_id}|${stream.slot}`;
const current = s => session === s && V().state.pc === s.pc && mediaScopeIsCurrent(s.scope);

export function streamSessionIsCurrent(pc) { return !!session && session.pc === pc && current(session); }

// Capture intent before an asynchronous receiver repair. The transport may
// temporarily remove its track binding, but a stop, retry or new publication
// invalidates this intent; recovery never starts watching on its own.
export function streamReceiverRecoveryTarget(pc, id, track) {
    const s = session, entry = s?.streams.get(id);
    if (!s || s.pc !== pc || !current(s) || !entry?.watching || entry.pending || entry.available === false ||
        s.tracks.get(id)?.track !== track) return null;
    const revision = entry.revision, generation = entry.generation, watchSession = s.watchSession;
    return {
        key: JSON.stringify([id, generation, watchSession]),
        id,
        request: { action: "receiver_reset", publisher_id: entry.publisher_id, slot: entry.slot, generation,
            session: watchSession, revision: String(revision), active: true },
        withdrawHEVC: () => withdrawWatchHEVC(s, entry, revision, generation, watchSession, track),
        bindingCurrent: () => s.tracks.get(id)?.track === track,
        current: () => current(s) && s.streams.get(id) === entry && entry.watching && !entry.pending && entry.available !== false &&
            entry.generation === generation && entry.revision === revision && s.watchSession === watchSession,
    };
}

export function streamQualityTarget(publisherID, slot) {
    const s = session;
    if (!s || !current(s) || !s.watchSession) return null;
    const entry = s.streams.get(`${publisherID}|${slot}`);
    return entry?.watching && entry.available !== false ? {
        publisherID: entry.publisher_id, slot: entry.slot, generation: entry.generation, session: s.watchSession,
    } : null;
}

export function memberScreenStream(clientID) {
    const s = session;
    if (!s || !current(s)) return null;
    const entry = s.streams.get(`${clientID}|screen`);
    if (entry) return { active: entry.available !== false, watching: entry.available !== false && entry.watching };
    const member = V().state.clients.find(client => String(client.client_id) === String(clientID));
    return s.catalogLoaded && member?.channel_id === s.scope.channelID ? { active: false, watching: false } : null;
}

function refreshMemberStreams(s) {
    const signature = JSON.stringify([...s.streams.values()].filter(entry => entry.slot === "screen")
        .map(entry => [entry.publisher_id, entry.available !== false, entry.watching]).sort((a, b) => a[0].localeCompare(b[0])));
    if (signature === s.memberSignature) return;
    s.memberSignature = signature;
    V().renderTree?.();
}

// Receiver bindings stay negotiated while unwatched. Playback follows only
// acknowledged intent; the router separately enforces packet delivery.
// trackID is the publisher/slot the CURRENT remote description assigns to this
// receiver (remote-media.js). The browser track.id is never a fallback: a
// reused MID keeps its creation-time id, so two live receivers can share one.
export function receiveStreamTrack(track, publisher, trackID) {
    const s = session;
    if (!s || !current(s) || !trackID) { track.enabled = false; return; }
    // A reused receiver that still carried another slot releases it first, so
    // the previous publisher's tile can never keep rendering this receiver.
    for (const previous of s.tracks.bind(trackID, track, { track, publisher })) {
        s.removeVideo(previous);
        const old = s.streams.get(previous);
        if (old) renderEntry(s, old);
    }
    track.enabled = false;
    const entry = s.streams.get(trackID);
    if (entry) entry.receiverEnded = false;
    applyWatch(s, entry);
    if (entry) renderEntry(s, entry);
}

// Only the receiver that currently owns trackID can remove it. A stale binding
// or another receiver with the same browser id leaves the live tile alone.
export function removeStreamTrack(track, trackID) {
    const s = session;
    if (!s || !s.tracks.release(trackID, track)) return;
    track.enabled = false;
    s.removeVideo(trackID);
    const entry = s.streams.get(trackID);
    if (entry) entry.receiverEnded = track.readyState === "ended";
    if (entry) renderEntry(s, entry);
}

export function receiveShareAudio(track, publisherID) {
    const s = session;
    if (!s || !current(s)) { track.enabled = false; return; }
    s.audio.bind(String(publisherID), track, track);
    track.enabled = !!s.streams.get(`${publisherID}|screen`)?.watching;
}

export function removeShareAudio(track, publisherID) {
    if (session?.audio.release(String(publisherID), track)) track.enabled = false;
}

function applyWatch(s, entry) {
    if (!entry) return;
    const id = key(entry), received = s.tracks.get(id);
    if (received) {
        received.track.enabled = entry.watching;
        if (entry.watching) entry.tile = s.addVideo(id, new MediaStream([received.track]), received.publisher, entry.liveControls);
        else s.removeVideo(id);
    }
    if (entry.slot === "screen" && s.audio.has(entry.publisher_id)) s.audio.get(entry.publisher_id).enabled = entry.watching;
}

async function toggleWatch(s, entry, retry = false) {
    if (!current(s) || s.streams.get(key(entry)) !== entry || entry.pending) return;
    if (retry && !streamRecovery(entry, s.tracks.get(key(entry))?.track).retry) return;
    if (entry.available === false) {
        entry.card.remove();
        s.streams.delete(key(entry));
        s.container.hidden = ![...s.streams.values()].some(item => !item.card.hidden);
        return;
    }
    const focusButton = retry ? entry.retryButton : entry.watchButton;
    const restoreFocus = document.activeElement === focusButton;
    const active = retry || !entry.watching;
    const retainedHEVC = retry && entry.watching ? entry.hevcRetry : null;
    entry.pending = true;
    entry.error = "";
    entry.revision++;
    if (!active) {
        s.removeVideo(key(entry));
        const received = s.tracks.get(key(entry));
        if (received) received.track.enabled = false;
        if (entry.slot === "screen" && s.audio.has(entry.publisher_id)) s.audio.get(entry.publisher_id).enabled = false;
    }
    renderEntry(s, entry);
    try {
        const codecSupport = await window.go.main.App.SupportsStreamCodecForTab?.(s.scope.tabID) || false;
        const hevc = codecSupport && await window.go.main.App.SupportsStreamHEVCForTab?.(s.scope.tabID) === true;
        const hardware = active && hevc && !retainedHEVC ? await receiverHEVCSupport() : null;
        if (!current(s) || s.streams.get(key(entry)) !== entry || entry.available === false) return;
        let capabilities = codecSupport ? { codecs: [...(hardware ? ["h265"] : []), ...receiverVideoCodecs()], ...(hardware || {}) } : {};
        if (retainedHEVC && codecSupport) {
            capabilities = entry.codecCapabilities || { codecs: receiverVideoCodecs() };
            // A repair of the same watch cannot bypass a withdrawal cooldown
            // or refill its lifetime budget, including a lost withdrawal reply.
            if (retainedHEVC.withdrawn) capabilities = { codecs: capabilities.codecs.filter(codec => codec !== "h265") };
        }
        await streamRequest(s.scope, { action: "watch", publisher_id: entry.publisher_id, slot: entry.slot, generation: entry.generation, revision: String(entry.revision), session: s.watchSession, active, ...capabilities });
        entry.codecCapabilities = capabilities;
        entry.hevcRetry = retainedHEVC || { enabled: hevc, done: !!hardware, next: Date.now() + 30000, attempts: 0 };
        if (retainedHEVC?.withdrawn) {
            const delay = hevcReadmissionDelay(retainedHEVC.readmissions || 0);
            retainedHEVC.done = delay === null;
            retainedHEVC.next = delay === null ? Infinity : Math.max(retainedHEVC.next, Date.now() + delay);
        }
        if (!current(s) || s.streams.get(key(entry)) !== entry || entry.available === false) return;
        entry.watching = active;
        entry.watchStartedAt = Date.now();
        entry.recoverySince = entry.watchStartedAt;
        applyWatch(s, entry);
    } catch (error) {
        if (current(s)) entry.error = t("streams.failed", { error: String(error) });
    } finally {
        entry.pending = false;
        if (current(s) && s.streams.get(key(entry)) === entry) {
            renderEntry(s, entry);
            refreshMemberStreams(s);
            if (restoreFocus && (document.activeElement === document.body || document.activeElement === focusButton)) {
                entry.watchButton.focus({ preventScroll: true });
            }
        }
    }
}

// A sustained actual software HEVC decoder withdraws only this watch's HEVC
// capability. Consent remains active; a bounded later poll may re-admit hardware.
async function withdrawWatchHEVC(s, entry, revision, generation, watchSession, track) {
    if (!current(s) || s.streams.get(key(entry)) !== entry || !entry.watching || entry.pending ||
        entry.revision !== revision || entry.generation !== generation || s.watchSession !== watchSession ||
        s.tracks.get(key(entry))?.track !== track || !entry.codecCapabilities?.codecs.includes("h265")) return false;
    const capabilities = { codecs: entry.codecCapabilities.codecs.filter(codec => codec !== "h265") };
    entry.pending = true;
    const sentRevision = ++entry.revision;
    const delay = hevcReadmissionDelay(entry.hevcRetry.readmissions || 0);
    entry.hevcRetry.withdrawn = true;
    entry.hevcRetry.done = delay === null; entry.hevcRetry.next = delay === null ? Infinity : Date.now() + delay;
    try {
        await streamRequest(s.scope, { action: "watch", publisher_id: entry.publisher_id, slot: entry.slot,
            generation, revision: String(sentRevision), session: watchSession, active: true, ...capabilities });
        if (!current(s) || s.streams.get(key(entry)) !== entry || entry.generation !== generation ||
            entry.revision !== sentRevision || s.watchSession !== watchSession || !entry.watching) return false;
        entry.codecCapabilities = capabilities;
        return true;
    } finally {
        entry.pending = false;
        if (current(s) && s.streams.get(key(entry)) === entry) renderEntry(s, entry);
    }
}

// Reuse catalog polling for a bounded late capability augmentation. A native
// timeout retains its cached query; it never causes a new upload or watch toggle.
function refreshWatchHEVC(s, entry) {
    const retry = entry.hevcRetry;
    if (!retry?.enabled || retry.done || retry.pending || entry.pending || !entry.watching || Date.now() < retry.next) return;
    const revision = entry.revision, generation = entry.generation, watchSession = s.watchSession;
    const valid = () => current(s) && s.streams.get(key(entry)) === entry && entry.watching && !entry.pending && entry.available !== false &&
        entry.generation === generation && entry.revision === revision && s.watchSession === watchSession;
    if (!valid()) return;
    retry.pending = true;
    retry.next = Date.now() + [30000, 120000, 300000][Math.min(retry.attempts++, 2)];
    void (async () => {
        try {
            const hardware = await receiverHEVCSupport();
            if (!hardware || !valid()) return;
            if (retry.withdrawn) {
                if (hevcReadmissionDelay(retry.readmissions || 0) === null) { retry.done = true; return; }
                retry.readmissions = (retry.readmissions || 0) + 1;
            }
            const capabilities = { codecs: [...new Set(["h265", ...(entry.codecCapabilities?.codecs || receiverVideoCodecs())])], ...hardware };
            entry.pending = true;
            entry.revision++;
            const sentRevision = entry.revision;
            try {
                await streamRequest(s.scope, { action: "watch", publisher_id: entry.publisher_id, slot: entry.slot,
                    generation, revision: String(sentRevision), session: watchSession, active: true, ...capabilities });
                if (current(s) && s.streams.get(key(entry)) === entry && entry.generation === generation &&
                    entry.revision === sentRevision && s.watchSession === watchSession && entry.watching) {
                    entry.codecCapabilities = capabilities; retry.done = true;
                }
            } finally {
                entry.pending = false;
                if (current(s) && s.streams.get(key(entry)) === entry) renderEntry(s, entry);
            }
        } catch { /* Keep existing playback and the finite retry cadence. */ }
        finally { retry.pending = false; }
    })();
}

function renderEntry(s, entry) {
    const card = entry.card;
    const person = V().state.clients.find(c => String(c.client_id) === entry.publisher_id);
    card.querySelector(".stream-name").textContent = person?.nickname || entry.publisher_id;
    card.querySelector(".stream-kind").textContent = t(entry.slot === "screen" ? "streams.screen" : "streams.camera");
    card.setAttribute("aria-label", `${person?.nickname || entry.publisher_id} · ${card.querySelector(".stream-kind").textContent}`);
    const button = entry.watchButton;
    const inVideo = entry.watching && !!entry.tile?.isConnected;
    if (inVideo && button.parentElement !== entry.liveControls) entry.liveControls.append(button, entry.audioControls);
    else if (!inVideo && button.parentElement !== card.querySelector(".stream-thumbnail")) {
        card.querySelector(".stream-thumbnail").append(button);
        card.append(entry.audioControls);
    }
    card.hidden = inVideo;
    s.container.hidden = ![...s.streams.values()].some(item => !item.card.hidden);
    const actionLabel = t(entry.available === false ? "streams.dismiss" : entry.pending ? "streams.updating" : entry.watching ? "streams.stop" : "streams.watch");
    button.textContent = inVideo ? "×" : actionLabel;
    button.setAttribute("aria-label", actionLabel);
    button.title = actionLabel;
    button.disabled = entry.pending;
    button.setAttribute("aria-pressed", String(entry.watching));
    const seconds = entry.previewAt ? Math.max(0, Math.floor((Date.now() - entry.previewAt) / 1000)) : 0;
    const track = s.tracks.get(key(entry))?.track;
    const recovery = streamRecovery(entry, track);
    const status = recovery.state === "idle" ? "" : t(`streams.${recovery.state}`);
    entry.retryButton.textContent = t("streams.retry");
    entry.retryButton.hidden = !recovery.retry;
    entry.retryButton.disabled = entry.pending;
    const recoveryContainer = inVideo ? entry.liveRecovery : card;
    if (entry.retryButton.parentElement !== recoveryContainer) recoveryContainer.append(entry.retryButton);
    const terminal = recovery.state === "ended" || recovery.state === "stopped";
    entry.liveStatus.textContent = terminal ? status : entry.error || entry.pollError || (recovery.state === "live" ? "" : status);
    entry.liveStatus.hidden = !entry.liveStatus.textContent;
    card.querySelector(".stream-status").textContent = terminal ? status : entry.error || entry.pollError || (entry.watching
        ? status
        : entry.previewAt ? t(seconds > 150 ? "streams.stale" : "streams.previewAge", { seconds }) : t("streams.noPreview"));
    if (entry.error || entry.pollError) card.querySelector(".stream-status").setAttribute("role", "alert");
    else card.querySelector(".stream-status").removeAttribute("role");
    const controls = entry.audioControls;
    const audio = V().shareAudioCtl?.get(entry.publisher_id);
    controls.hidden = entry.slot !== "screen" || !entry.watching || !audio;
    if (audio) {
        const mute = controls.querySelector("button");
        mute.textContent = t(audio.muted ? "polish.sharedAudioUnmute" : "polish.sharedAudioMute");
        mute.setAttribute("aria-pressed", String(audio.muted));
        const slider = controls.querySelector("input");
        if (document.activeElement !== slider) slider.value = String(audio.volume);
        slider.setAttribute("aria-label", t("polish.sharedAudio"));
        slider.setAttribute("aria-valuetext", `${audio.volume}%`);
        controls.querySelector("output").textContent = `${audio.volume}%`;
    }
}

function createEntry(s, stream) {
    const entry = { ...stream, previewAt: 0, revision: BigInt(stream.watch_revision), watching: false, pending: false, error: "" };
    const card = document.createElement("article");
    card.className = "stream-card";
    card.dataset.publisher = stream.publisher_id;
    card.dataset.slot = stream.slot;
    card.innerHTML = `<div class="stream-thumbnail"><img class="stream-preview" alt="" hidden><button class="stream-watch"></button></div><div class="stream-heading"><strong class="stream-name"></strong><span class="stream-kind"></span></div><p class="stream-status"></p><div class="stream-audio" hidden><button></button><input type="range" min="0" max="200" step="5"><output></output></div>`;
    entry.card = card;
    entry.watchButton = card.querySelector(".stream-watch");
    entry.audioControls = card.querySelector(".stream-audio");
    entry.liveControls = document.createElement("div");
    entry.liveControls.className = "stream-live-controls";
    entry.liveStatus = document.createElement("p");
    entry.liveStatus.className = "stream-live-notice";
    entry.liveStatus.setAttribute("role", "status");
    entry.liveRecovery = document.createElement("div");
    entry.liveRecovery.className = "stream-recovery";
    entry.liveRecovery.append(entry.liveStatus);
    entry.liveControls.append(entry.liveRecovery);
    entry.retryButton = document.createElement("button");
    entry.retryButton.className = "stream-retry";
    entry.retryButton.type = "button";
    entry.retryButton.onclick = () => { void toggleWatch(s, entry, true); };
    entry.liveControls.onclick = event => event.stopPropagation();
    entry.liveControls.oncontextmenu = event => event.stopPropagation();
    card.querySelector(".stream-watch").onclick = () => { void toggleWatch(s, entry); };
    card.querySelector(".stream-audio button").onclick = () => {
        const audio = V().shareAudioCtl.get(entry.publisher_id);
        if (audio) V().shareAudioCtl.setMuted(entry.publisher_id, !audio.muted);
        renderEntry(s, entry);
    };
    card.querySelector("input").oninput = event => {
        V().shareAudioCtl.setVolume(entry.publisher_id, Number(event.target.value));
        renderEntry(s, entry);
    };
    s.container.append(card);
    return entry;
}

async function poll(s) {
    if (!current(s)) return;
    if (s.polling) { s.pollAgain = true; return; }
    clearTimeout(s.timer);
    s.polling = true;
    try {
        const publications = publicationSnapshot();
        const result = await streamRequest(s.scope, { action: "list" });
        if (!current(s)) return;
        s.catalogLoaded = true;
        reconcilePublications(publications, result.streams);
        if (!s.pollAgain) reconcilePublicationUploads(publications, result.streams);
        updateShareViewers(result.streams);
        if (s.watchSession && s.watchSession !== result.session) {
            for (const entry of s.streams.values()) { entry.watching = false; applyWatch(s, entry); entry.card.remove(); }
            s.streams.clear();
        }
        s.watchSession = result.session;
        const available = new Map(result.streams.filter(stream => stream.publisher_id !== s.scope.clientID).map(stream => [key(stream), stream]));
        for (const [id, entry] of s.streams) {
            if (available.get(id)?.generation === entry.generation) continue;
            const wasWatching = entry.watching || entry.pending || entry.available === false;
            entry.watching = false;
            applyWatch(s, entry);
            if (!available.has(id) && wasWatching) {
                entry.available = false;
                renderEntry(s, entry);
                continue;
            }
            entry.card.remove();
            s.streams.delete(id);
        }
        for (const [id, stream] of available) {
            let entry = s.streams.get(id);
            if (!entry) { entry = createEntry(s, stream); s.streams.set(id, entry); }
            entry.available = true;
            entry.quality_mode = stream.quality_mode;
            const revision = BigInt(stream.watch_revision);
            if (revision > entry.revision) entry.revision = revision;
            entry.pollError = "";
            refreshWatchHEVC(s, entry);
            if (stream.preview_at && stream.preview_at !== entry.previewAt) {
                const preview = await streamRequest(s.scope, { action: "preview", publisher_id: stream.publisher_id, slot: stream.slot, generation: stream.generation });
                if (!current(s)) return;
                if (s.streams.get(id) !== entry) continue;
                if (preview.jpeg) {
                    const img = entry.card.querySelector("img");
                    img.src = `data:image/jpeg;base64,${preview.jpeg}`;
                    img.hidden = false;
                    entry.previewAt = preview.preview_at;
                }
            }
            renderEntry(s, entry);
        }
        s.container.hidden = ![...s.streams.values()].some(entry => !entry.card.hidden);
    } catch (error) {
        if (current(s)) for (const entry of s.streams.values()) {
            entry.pollError = t("streams.failed", { error: String(error) });
            renderEntry(s, entry);
        }
    } finally {
        s.polling = false;
        if (current(s)) {
            refreshMemberStreams(s);
            window.dispatchEvent(new Event("noxa-stream-catalog"));
            s.timer = setTimeout(() => { void poll(s); }, s.pollAgain ? 0 : 3000);
            s.pollAgain = false;
        }
    }
}

export function refreshStreamUploads(data) {
    if (session && current(session) && isCurrentPublication(data)) void poll(session);
}

// availableStreams lists active publications this client is not watching,
// for the stage's "+ Add" picker. watch() goes through the same toggleWatch
// as the catalog card, so server limits, pending state and errors apply.
export function availableStreams() {
    const s = session;
    if (!s || !current(s)) return [];
    return [...s.streams.values()].filter(entry => entry.available !== false && !entry.watching && !entry.pending).map(entry => {
        const person = V().state.clients.find(c => String(c.client_id) === entry.publisher_id);
        const img = entry.card.querySelector(".stream-preview");
        return { id: key(entry), publisherID: entry.publisher_id, slot: entry.slot, name: person?.nickname || entry.publisher_id,
            preview: img && !img.hidden ? img.src : "", watch: () => toggleWatch(s, entry) };
    });
}

export function streamUsesSourceQuality(publisherID, slot) {
    const s = session;
    return !!s && current(s) && s.streams.get(`${publisherID}|${slot}`)?.quality_mode === "source";
}

export function startStreamSession(pc, addVideo, removeVideo) {
    stopStreamSession();
    const container = document.createElement("section");
    container.id = "stream-catalog";
    container.hidden = true;
    container.setAttribute("aria-label", t("streams.available"));
    // The stream stage (stream-stage.js) may wrap the grid; move the wrapper.
    const gridEl = document.getElementById("video-grid");
    const grid = gridEl.closest?.(".stream-stage") || gridEl;
    const media = document.createElement("div");
    media.className = "stream-media";
    grid.before(media);
    media.append(container, grid);
    const s = { pc, scope: captureMediaScope(), addVideo, removeVideo, container, media, grid, streams: new Map(), tracks: createReceiverRegistry(), audio: createReceiverRegistry() };
    session = s;
    void poll(s);
}

export function stopStreamSession() {
    const s = session;
    session = null;
    if (s) {
        clearTimeout(s.timer);
        for (const entry of s.streams.values()) { entry.watching = false; applyWatch(s, entry); }
        const gridEl = document.getElementById("video-grid");
        s.media.before(gridEl?.closest?.(".stream-stage") || gridEl || s.grid);
        s.media.remove();
        V().renderTree?.();
    }
    stopPublications();
}

window.addEventListener("noxa-language-changed", () => {
    if (session && current(session)) {
        session.container.setAttribute("aria-label", t("streams.available"));
        for (const entry of session.streams.values()) renderEntry(session, entry);
    }
});
