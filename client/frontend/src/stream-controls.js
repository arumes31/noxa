import { captureMediaScope, mediaScopeIsCurrent } from "./media-controls.js";
import { streamRequest, stopPublications, publicationSnapshot, reconcilePublications } from "./stream-publication.js";
import { t } from "./i18n.js";

const V = () => window.__noxa;
let session = null;
const key = stream => `${stream.publisher_id}|${stream.slot}`;
const current = s => session === s && V().state.pc === s.pc && mediaScopeIsCurrent(s.scope);

export function streamSessionIsCurrent(pc) { return !!session && session.pc === pc && current(session); }

// Receiver bindings stay negotiated while unwatched. Playback follows only
// acknowledged intent; the router separately enforces packet delivery.
export function receiveStreamTrack(track, publisher) {
    const s = session;
    if (!s || !current(s)) { track.enabled = false; return; }
    s.tracks.set(track.id, { track, publisher });
    track.enabled = false;
    const entry = s.streams.get(track.id);
    applyWatch(s, entry);
    if (entry) renderEntry(s, entry);
}

export function receiveShareAudio(track, publisherID) {
    const s = session;
    if (!s || !current(s)) { track.enabled = false; return; }
    s.audio.set(String(publisherID), track);
    track.enabled = !!s.streams.get(`${publisherID}|screen`)?.watching;
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

async function toggleWatch(s, entry) {
    if (!current(s) || entry.pending) return;
    const restoreFocus = document.activeElement === entry.watchButton;
    const active = !entry.watching;
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
        await streamRequest(s.scope, { action: "watch", publisher_id: entry.publisher_id, slot: entry.slot, generation: entry.generation, revision: String(entry.revision), session: s.watchSession, active });
        if (!current(s) || s.streams.get(key(entry)) !== entry) return;
        entry.watching = active;
        applyWatch(s, entry);
    } catch (error) {
        if (current(s)) entry.error = t("streams.failed", { error: String(error) });
    } finally {
        entry.pending = false;
        if (current(s) && s.streams.get(key(entry)) === entry) {
            renderEntry(s, entry);
            if (restoreFocus && (document.activeElement === document.body || document.activeElement === entry.watchButton)) {
                entry.watchButton.focus({ preventScroll: true });
            }
        }
    }
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
    const actionLabel = t(entry.pending ? "streams.updating" : entry.watching ? "streams.stop" : "streams.watch");
    button.textContent = inVideo ? "×" : actionLabel;
    button.setAttribute("aria-label", actionLabel);
    button.title = actionLabel;
    button.disabled = entry.pending;
    button.setAttribute("aria-pressed", String(entry.watching));
    const seconds = entry.previewAt ? Math.max(0, Math.floor((Date.now() - entry.previewAt) / 1000)) : 0;
    const track = s.tracks.get(key(entry))?.track;
    entry.liveStatus.textContent = entry.error || entry.pollError || (track?.muted ? t("streams.waiting") : "");
    entry.liveStatus.hidden = !entry.liveStatus.textContent;
    card.querySelector(".stream-status").textContent = entry.error || entry.pollError || (entry.watching
        ? t(track && !track.muted ? (V().state.settings?.low_bandwidth ? "streams.paused" : "streams.live") : "streams.waiting")
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
    entry.liveControls.append(entry.liveStatus);
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
    try {
        const publications = publicationSnapshot();
        const result = await streamRequest(s.scope, { action: "list" });
        if (!current(s)) return;
        reconcilePublications(publications, result.streams);
        if (s.watchSession && s.watchSession !== result.session) {
            for (const entry of s.streams.values()) { entry.watching = false; applyWatch(s, entry); entry.card.remove(); }
            s.streams.clear();
        }
        s.watchSession = result.session;
        const available = new Map(result.streams.filter(stream => stream.publisher_id !== s.scope.clientID).map(stream => [key(stream), stream]));
        for (const [id, entry] of s.streams) {
            if (available.get(id)?.generation === entry.generation) continue;
            entry.watching = false;
            applyWatch(s, entry);
            entry.card.remove();
            s.streams.delete(id);
        }
        for (const [id, stream] of available) {
            let entry = s.streams.get(id);
            if (!entry) { entry = createEntry(s, stream); s.streams.set(id, entry); }
            entry.pollError = "";
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
        if (current(s)) s.timer = setTimeout(() => { void poll(s); }, 3000);
    }
}

export function startStreamSession(pc, addVideo, removeVideo) {
    stopStreamSession();
    const container = document.createElement("section");
    container.id = "stream-catalog";
    container.hidden = true;
    container.setAttribute("aria-label", t("streams.available"));
    const grid = document.getElementById("video-grid");
    const media = document.createElement("div");
    media.className = "stream-media";
    grid.before(media);
    media.append(container, grid);
    const s = { pc, scope: captureMediaScope(), addVideo, removeVideo, container, media, grid, streams: new Map(), tracks: new Map(), audio: new Map() };
    session = s;
    void poll(s);
}

export function stopStreamSession() {
    const s = session;
    session = null;
    if (s) {
        clearTimeout(s.timer);
        for (const entry of s.streams.values()) { entry.watching = false; applyWatch(s, entry); }
        s.media.before(s.grid);
        s.media.remove();
    }
    stopPublications();
}

window.addEventListener("noxa-language-changed", () => {
    if (session && current(session)) {
        session.container.setAttribute("aria-label", t("streams.available"));
        for (const entry of session.streams.values()) renderEntry(session, entry);
    }
});
