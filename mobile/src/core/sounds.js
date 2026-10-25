// sounds.js — in-app sounds and spoken announcements of the desktop's
// sound pack (public/sounds, public/speech; licences in
// public/noxa-audio-licenses.txt). One AudioContext, each file fetched and
// decoded once. Browsers start the context suspended until the first tap
// (iOS in particular), so unlock() is called from a user gesture.

/** Event key -> file in /app/sounds/. The ring loops the announcement cue. */
export const SOUND_FILES = Object.freeze({
    dm: "dm.wav", channel_message: "channel_message.wav", mention: "mention.wav", keyword: "keyword.wav",
    poke: "poke.wav", whisper: "whisper.wav", buddy_online: "buddy_online.wav", channel_watch: "channel_watch.wav",
    ring: "announcement.wav", connection_lost: "connection_lost.wav", connection_reconnected: "connection_reconnected.wav",
});

/** Announcements shipped in English and German (/app/speech/<lang>/). */
export const SPEECH_EVENTS = Object.freeze(["connection_lost", "connection_reconnected", "channel_join", "channel_leave", "kicked", "kicked_channel", "banned", "poke", "buddy_online", "channel_watch"]);

/** Matrix rows use these sound keys. */
export const EVENT_SOUNDS = Object.freeze({
    mention: "mention", keyword: "keyword", dm: "dm", group: "dm", channel: "channel_message", poke: "poke",
    whisper: "whisper", buddy_online: "buddy_online", channel_watch: "channel_watch", call: "ring",
});

export class SoundEngine {
    /**
     * options: createContext() -> AudioContext, load(url) -> ArrayBuffer,
     * state() -> {enabled, volume (0..1), speech, silenced, language},
     * base URL of the app (default "/app/").
     */
    constructor({ createContext, load, state, base = "/app/" }) {
        Object.assign(this, { createContext, load, state, base });
        this.ctx = null;
        this.buffers = new Map();
        this.loops = new Map();
        this.playing = new Set();
    }

    /** Creates or resumes the context; call from a tap or key press. */
    unlock() {
        try {
            this.ctx ||= this.createContext();
            if (this.ctx.state === "suspended") this.ctx.resume?.().catch(() => {});
        } catch {
            this.ctx = null;
        }
        return this.ctx;
    }

    #buffer(url) {
        if (!this.buffers.has(url)) {
            const pending = Promise.resolve(this.load(url))
                .then(bytes => new Promise((resolve, reject) => {
                    // Safari's decodeAudioData only takes callbacks.
                    const result = this.ctx.decodeAudioData(bytes, resolve, reject);
                    result?.then?.(resolve, reject);
                }))
                .catch(error => { this.buffers.delete(url); throw error; });
            this.buffers.set(url, pending);
        }
        return this.buffers.get(url);
    }

    async #play(url, { force = false } = {}) {
        const state = this.state();
        if (!force && (!state.enabled || state.silenced)) return null;
        const volume = Math.max(0, Math.min(1, Number(state.volume ?? 1)));
        if (!volume || !this.unlock()) return null;
        try {
            const buffer = await this.#buffer(url);
            const source = this.ctx.createBufferSource();
            source.buffer = buffer;
            const gain = this.ctx.createGain();
            gain.gain.value = volume;
            source.connect(gain).connect(this.ctx.destination);
            this.playing.add(source);
            source.onended = () => this.playing.delete(source);
            source.start();
            return source;
        } catch {
            return null;
        }
    }

    /** Plays an event sound (SOUND_FILES key). force ignores mute and DND (previews). */
    play(key, options) {
        const file = SOUND_FILES[key];
        return file ? this.#play(`${this.base}sounds/${file}`, options) : Promise.resolve(null);
    }

    /** Speaks an announcement when spoken announcements are on. */
    speak(event, { force = false } = {}) {
        const state = this.state();
        if (!SPEECH_EVENTS.includes(event) || (!force && !state.speech)) return Promise.resolve(null);
        const language = state.language === "de" ? "de" : "en";
        return this.#play(`${this.base}speech/${language}/${event}.wav`, { force });
    }

    /** Repeats a sound every interval ms until stopLoop(key) (the ring). */
    loop(key, interval = 2500) {
        if (this.loops.has(key)) return;
        const tick = () => { this.play(key); };
        tick();
        this.loops.set(key, setInterval(tick, interval));
    }

    stopLoop(key) {
        clearInterval(this.loops.get(key));
        this.loops.delete(key);
    }

    stopAll() {
        for (const key of [...this.loops.keys()]) this.stopLoop(key);
        for (const source of this.playing) { try { source.stop(); } catch { /* already ended */ } }
        this.playing.clear();
    }
}
