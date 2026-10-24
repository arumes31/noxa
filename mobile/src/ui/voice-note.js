// voice-note.js — voice messages: the composer's microphone button with its
// recorder (level meter, 5-minute cap, preview before sending) and the
// compact inline player for audio attachments. The file and message format
// is the desktop's (core/voice-notes.js), so either side plays the other's.
import { withAttachmentMeta } from "../core/files.js";
import { VOICE_MAX_BYTES, VOICE_MAX_MS, audioMime, formatDuration, levelDB, pickVoiceMime, voiceNoteName, waveformPeaks } from "../core/voice-notes.js";
import { t } from "../i18n.js";
import { h, toast } from "./dom.js";
import { icon } from "./icons.js";
import { panel } from "./conversation-tools.js";

// --- Player ----------------------------------------------------------------------

const players = new Set();
const SPEEDS = [1, 1.5, 2];

function forget(player) {
    player.audio?.pause();
    if (player.url) URL.revokeObjectURL(player.url);
    players.delete(player);
}

// Players leave with their message; free their decoded audio then.
let observer = null;
function watchPlayers() {
    if (observer || typeof MutationObserver === "undefined") return;
    observer = new MutationObserver(() => {
        for (const player of players) if (!player.node.isConnected) forget(player);
        if (!players.size) { observer.disconnect(); observer = null; }
    });
    observer.observe(document.body, { childList: true, subtree: true });
}

/**
 * A compact player: play/pause, seek bar, time and 1x/1.5x/2x. blobPromise is
 * a Blob promise or a function returning one; it is awaited on the first play
 * only (attachments download then). meta may carry the token's durationMs and
 * peaks, shown before anything is loaded.
 */
export function voiceNotePlayer(blobPromise, name, meta = {}) {
    const duration = (meta.durationMs || 0) / 1000;
    let speed = 0;
    const play = h("button", { class: "voice-play", type: "button", "aria-label": t("vnote.play") }, icon("play"));
    const seek = h("input", { class: "voice-seek", type: "range", min: "0", max: "1000", value: "0", step: "1", "aria-label": t("vnote.position"), disabled: true });
    const time = h("span", { class: "voice-time", "aria-hidden": "true" }, duration ? formatDuration(duration * 1000) : "0:00");
    const rate = h("button", { class: "voice-rate", type: "button", "aria-label": t("vnote.speed", { speed: "1x" }) }, "1x");
    const wave = meta.peaks?.length ? h("span", { class: "voice-wave", "aria-hidden": "true" },
        meta.peaks.map(level => h("span", { style: `height:${Math.max(12, Math.round(level * 100))}%` }))) : null;
    const node = h("div", { class: "voice-note", role: "group", "aria-label": t("vnote.message", { duration: duration ? formatDuration(duration * 1000) : "" }) },
        play, h("span", { class: "voice-track" }, wave, seek), time, rate);
    const player = { node, audio: null, url: "" };

    const paint = () => {
        const audio = player.audio;
        const known = Number.isFinite(audio?.duration) && audio.duration > 0 ? audio.duration : duration;
        const current = audio?.currentTime || 0;
        seek.value = known ? String(Math.round(Math.min(1, current / known) * 1000)) : "0";
        node.style.setProperty("--voice-progress", `${known ? Math.min(100, current / known * 100) : 0}%`);
        time.textContent = audio && (current > 0 || !audio.paused) ? `${formatDuration(current * 1000)} / ${formatDuration(known * 1000)}` : formatDuration(known * 1000);
        const playing = audio && !audio.paused;
        play.replaceChildren(icon(playing ? "pause" : "play"));
        play.setAttribute("aria-label", t(playing ? "vnote.pause" : "vnote.play"));
        node.classList.toggle("playing", !!playing);
    };

    let loading = null;
    const load = () => loading ||= (async () => {
        const blob = await (typeof blobPromise === "function" ? blobPromise() : blobPromise);
        player.url = URL.createObjectURL(blob.type ? blob : new Blob([blob], { type: audioMime(name) || "audio/webm" }));
        const audio = h("audio", { preload: "metadata", src: player.url, hidden: true });
        audio.playbackRate = SPEEDS[speed];
        for (const type of ["timeupdate", "play", "pause", "ended", "loadedmetadata", "durationchange"]) audio.addEventListener(type, paint);
        audio.addEventListener("play", () => { for (const other of players) if (other !== player) other.audio?.pause(); });
        node.append(audio);
        player.audio = audio;
        seek.disabled = false;
        players.add(player);
        watchPlayers();
        return audio;
    })();

    play.onclick = async () => {
        play.disabled = true;
        play.setAttribute("aria-busy", "true");
        try {
            const audio = await load();
            if (audio.paused) await audio.play(); else audio.pause();
        } catch (error) {
            loading = null;
            toast(t("vnote.playFailed", { error: error.message }), "warn");
        } finally {
            play.disabled = false;
            play.removeAttribute("aria-busy");
            paint();
        }
    };
    seek.oninput = () => {
        const audio = player.audio;
        const known = Number.isFinite(audio?.duration) && audio.duration > 0 ? audio.duration : duration;
        if (audio && known) audio.currentTime = Number(seek.value) / 1000 * known;
        paint();
    };
    rate.onclick = () => {
        speed = (speed + 1) % SPEEDS.length;
        const label = `${SPEEDS[speed]}x`;
        rate.textContent = label;
        rate.setAttribute("aria-label", t("vnote.speed", { speed: label }));
        if (player.audio) player.audio.playbackRate = SPEEDS[speed];
    };
    return node;
}

// --- Recorder ----------------------------------------------------------------------

/** Measures a recording: exact duration and waveform when it decodes, else the elapsed time. */
async function summarize(blob, elapsedMs) {
    const Context = globalThis.AudioContext || globalThis.webkitAudioContext;
    let context;
    try {
        context = new Context();
        const decoded = await context.decodeAudioData(await blob.arrayBuffer());
        const channels = Array.from({ length: decoded.numberOfChannels }, (_, index) => decoded.getChannelData(index));
        return { durationMs: Math.round(decoded.duration * 1000) || Math.round(elapsedMs), peaks: waveformPeaks(channels) };
    } catch {
        return { durationMs: Math.round(elapsedMs), peaks: null };
    } finally {
        await context?.close().catch(() => {});
    }
}

/**
 * The microphone button of a composer. composerCtx: {upload(file) -> token,
 * send(token) -> Promise, disabled() -> bool}. Tapping it opens the
 * recorder: recording starts at once; Stop shows a preview to play, send,
 * record again or discard.
 */
export function voiceNoteButton(composerCtx) {
    const button = h("button", { class: "attach voice-button", type: "button", "aria-label": t("vnote.record") }, icon("mic"));
    button.onclick = () => {
        if (composerCtx.disabled?.()) return;
        if (!globalThis.MediaRecorder || !navigator.mediaDevices?.getUserMedia || !pickVoiceMime()) {
            toast(t("vnote.unsupported"), "warn");
            return;
        }
        openRecorder(composerCtx);
    };
    return button;
}

function openRecorder(composerCtx) {
    const status = h("p", { class: "hint", role: "status" }, t("vnote.permission"));
    const elapsed = h("output", { class: "voice-elapsed", role: "timer", "aria-live": "off", "aria-label": t("vnote.elapsed") }, `0:00 / ${formatDuration(VOICE_MAX_MS)}`);
    const level = h("span");
    const meter = h("div", { class: "voice-meter", role: "meter", "aria-label": t("vnote.level"), "aria-valuemin": "-60", "aria-valuemax": "0", "aria-valuenow": "-60" }, level);
    const live = h("div", { class: "voice-live", hidden: true }, h("span", { class: "voice-dot", "aria-hidden": "true" }), elapsed, meter);
    const preview = h("div", { class: "voice-preview" });
    const actions = h("div", { class: "voice-actions" });
    const view = panel(t("vnote.title"), h("div", { class: "voice-recorder" }, status, live, preview, actions));

    let stream = null, recorder = null, chunks = [], bytes = 0, started = 0, timer = 0, meterTimer = 0, context = null;
    let result = null, closed = false, sending = false;

    const release = () => {
        clearInterval(timer);
        clearInterval(meterTimer);
        context?.close().catch(() => {});
        context = null;
        for (const track of stream?.getTracks() || []) track.stop();
        stream = null;
    };
    view.closed.then(() => {
        closed = true;
        if (recorder && recorder.state !== "inactive") { recorder.onstop = null; recorder.stop(); }
        release();
    });
    const button = (label, onclick, kind = "") => h("button", { class: `btn ${kind}`, type: "button", onclick }, label);

    const showRecording = () => {
        live.hidden = false;
        preview.replaceChildren();
        actions.replaceChildren(
            button(t("vnote.stop"), () => recorder?.state !== "inactive" && recorder.stop(), "btn-primary"),
            button(t("vnote.cancel"), () => { result = null; view.close(); }));
        actions.querySelector(".btn-primary").focus();
    };

    const showPreview = () => {
        live.hidden = true;
        status.textContent = t("vnote.ready", { duration: formatDuration(result.elapsedMs) });
        const player = voiceNotePlayer(Promise.resolve(result.blob), result.name, { durationMs: result.elapsedMs });
        preview.replaceChildren(player);
        const send = button(t("vnote.send"), async () => {
            if (sending) return;
            sending = true;
            send.disabled = true;
            status.textContent = t("vnote.sending");
            try {
                const summary = await summarize(result.blob, result.elapsedMs);
                const file = new File([result.blob], result.name, { type: result.blob.type });
                const token = withAttachmentMeta(await composerCtx.upload(file), summary);
                if (closed) return;
                view.close();
                await composerCtx.send(token);
            } catch (error) {
                status.textContent = t("vnote.failed", { error: error.message });
                send.disabled = false;
            } finally {
                sending = false;
            }
        }, "btn-primary");
        actions.replaceChildren(send, button(t("vnote.again"), start), button(t("vnote.discard"), () => view.close(), "btn-danger"));
        send.focus();
    };

    const startMeter = () => {
        try {
            const Context = globalThis.AudioContext || globalThis.webkitAudioContext;
            context = new Context();
            const analyser = context.createAnalyser();
            analyser.fftSize = 1024;
            // Measurement only: the microphone is never connected to the speakers.
            context.createMediaStreamSource(stream).connect(analyser);
            const samples = new Float32Array(analyser.fftSize);
            meterTimer = setInterval(() => {
                analyser.getFloatTimeDomainData(samples);
                const db = levelDB(samples);
                meter.setAttribute("aria-valuenow", db.toFixed(0));
                level.style.width = `${Math.round((db + 60) / 60 * 100)}%`;
            }, 100);
            context.resume().catch(() => {});
        } catch {
            meter.hidden = true; // a missing meter never blocks recording
        }
    };

    async function start() {
        result = null;
        chunks = [];
        bytes = 0;
        actions.replaceChildren();
        preview.replaceChildren();
        status.textContent = t("vnote.permission");
        try {
            stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true }, video: false });
            if (closed) { release(); return; }
            const mimeType = pickVoiceMime();
            recorder = new MediaRecorder(stream, { mimeType, audioBitsPerSecond: 48000 });
            recorder.ondataavailable = event => {
                if (!event.data.size) return;
                bytes += event.data.size;
                if (bytes > VOICE_MAX_BYTES) { status.textContent = t("vnote.tooLarge"); recorder.stop(); return; }
                chunks.push(event.data);
            };
            recorder.onstop = () => {
                const elapsedMs = Math.min(VOICE_MAX_MS, Date.now() - started);
                release();
                if (closed) return;
                if (!chunks.length) { status.textContent = t("vnote.empty"); actions.replaceChildren(button(t("vnote.again"), start, "btn-primary")); return; }
                const type = recorder.mimeType || mimeType;
                result = { blob: new Blob(chunks, { type: type.split(";")[0] }), name: voiceNoteName(type), elapsedMs };
                chunks = [];
                showPreview();
            };
            recorder.start(250);
            started = Date.now();
            status.textContent = t("vnote.recording");
            startMeter();
            timer = setInterval(() => {
                const ms = Math.min(VOICE_MAX_MS, Date.now() - started);
                elapsed.textContent = `${formatDuration(ms)} / ${formatDuration(VOICE_MAX_MS)}`;
                if (ms >= VOICE_MAX_MS && recorder.state !== "inactive") recorder.stop();
                else if (ms >= VOICE_MAX_MS - 30000) status.textContent = t("vnote.limitSoon");
            }, 250);
            showRecording();
        } catch (error) {
            release();
            status.textContent = t("vnote.failed", { error: error.message });
            actions.replaceChildren(button(t("vnote.again"), start, "btn-primary"));
        }
    }
    start();
}
