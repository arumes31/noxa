import { SOUND_DEFINITIONS, SOUND_URLS } from "./sound-catalog.js";

export function soundVolume(value) {
    if ((typeof value !== "number" && typeof value !== "string") || !Number.isFinite(Number(value))) return 1;
    return Math.max(0, Math.min(2, Number(value) / 100));
}

// One decoded buffer per cue, one context per application. The source ceiling
// and authored 0.115 asset peak guarantee <=0.92 even with all four at 200%.
// This avoids compressor lookahead on the PTT path and gain-dependent distortion.
export class SoundEngine {
    constructor({ getState, isDND, createContext, load, now = () => performance.now(), warn = console.warn,
        definitions = SOUND_DEFINITIONS, urls = SOUND_URLS }) {
        Object.assign(this, { getState, isDND, createContext, load, now, warn });
        this.definitions = definitions;
        this.urls = urls;
        this.buffers = new Map();
        this.loading = new Map();
        this.active = new Set();
        this.retiring = new Set();
        this.last = new Map();
        this.warnings = new Set();
        this.ctx = null;
        this.output = Promise.resolve();
        this.requestedSink = undefined;
        this.generation = 0;
        this.disposed = false;
    }

    report(kind) {
        if (this.warnings.has(kind)) return;
        this.warnings.add(kind);
        this.warn(`VOICX sound: ${kind}`);
    }

    context() {
        if (this.disposed) return null;
        try {
            if (!this.ctx || this.ctx.state === "closed") {
                this.ctx = this.createContext();
                this.requestedSink = undefined;
            }
            return this.ctx;
        } catch { this.report("audio context unavailable"); return null; }
    }

    async preload() {
        const ctx = this.context();
        if (!ctx) return;
        const generation = this.generation;
        await Promise.all(Object.keys(this.definitions).map((name) => {
            if (this.buffers.has(name)) return undefined;
            if (!this.loading.has(name)) {
                const task = Promise.resolve().then(() => this.load(this.urls[name]))
                    .then(data => ctx.decodeAudioData(data))
                    .then(buffer => {
                        if (!this.disposed && generation === this.generation) this.buffers.set(name, buffer);
                    }).catch(() => this.report(`could not load ${name}`));
                this.loading.set(name, task);
            }
            return this.loading.get(name);
        }));
    }

    resume() {
        const ctx = this.context();
        if (!ctx) return Promise.resolve(false);
        if (ctx.state === "running") return Promise.resolve(true);
        if (!this.resuming) {
            this.resuming = Promise.resolve().then(() => ctx.resume())
                .then(() => ctx.state === "running")
                .catch(() => { this.report("playback is suspended"); return false; })
                .finally(() => { this.resuming = null; });
        }
        return this.resuming;
    }

    setOutput(id = "") {
        const ctx = this.context();
        if (!ctx) return Promise.resolve();
        const sink = typeof id === "string" ? id : "";
        if (this.requestedSink === sink) return this.output;
        this.requestedSink = sink;
        this.output = this.output.then(async () => {
            if (this.disposed || ctx !== this.ctx) return;
            if (typeof ctx.setSinkId !== "function") {
                if (sink) this.report("this WebView uses the default sound output");
                return;
            }
            try { await ctx.setSinkId(sink); }
            catch {
                this.report("selected output unavailable; using system default");
                try { await ctx.setSinkId(""); } catch { this.report("output routing unavailable"); }
            }
        }).catch(() => this.report("output routing unavailable"));
        return this.output;
    }

    allowed(name, options) {
        const state = this.getState();
        const settings = options.settings || state?.settings;
        return !!settings && !this.isDND()
            && settings.event_sounds?.[name] !== false
            && (options.force || (settings.play_sounds !== false && !state?.replayingTabID));
    }

    play(name, options = {}) {
        if (!Object.hasOwn(this.definitions, name) || !this.allowed(name, options)) return false;
        const settings = options.settings || this.getState().settings;
        const volume = soundVolume(options.volume ?? settings.sound_volume);
        if (!volume) return false;
        const def = this.definitions[name];
        const time = this.now();
        const key = def.category === "Other users" ? "movement" : name;
        if (!options.force && time - (this.last.get(key) ?? -Infinity) < def.cooldown) return false;
        const ctx = this.context();
        if (!ctx) return false;
        // Never queue a stale PTT transition or replay an event after autoplay
        // unlock. Startup/gesture preloading prepares subsequent live actions.
        if (!this.buffers.has(name) || ctx.state !== "running") {
            void this.preload();
            void this.resume();
            return false;
        }
        void this.setOutput(settings.playback_device_id);
        // Audio advances independently of the main thread. Reclaim completed
        // fades even when a burst delays delivery of their ended callbacks.
        for (const entry of this.retiring) {
            if (entry.stopAt <= ctx.currentTime) this.release(entry, false);
        }
        const family = name.startsWith("ptt_") ? "ptt" : name;
        for (const entry of this.active) {
            if (entry.family === family) this.retire(entry);
        }
        if (this.active.size >= 4) {
            const victim = [...this.active].sort((a, b) => a.priority - b.priority)[0];
            if (!options.force && victim.priority > def.priority) return false;
            this.retire(victim);
        }
        let source, gain;
        try {
            source = ctx.createBufferSource();
            gain = ctx.createGain();
            source.buffer = this.buffers.get(name);
            gain.gain.value = volume;
            source.connect(gain).connect(ctx.destination);
            // Reserve the slot while its previous cue fades. Rapid replacements
            // cancel unstarted sources, so no burst can accumulate audible tails.
            const startAt = Math.max(ctx.currentTime, ...[...this.retiring].map(e => e.stopAt));
            const entry = { source, gain, family, startAt, volume, priority: def.priority, preview: !!options.force };
            source.onended = () => { this.release(entry, false); options.onEnded?.(); };
            this.active.add(entry);
            source.start(startAt);
            this.last.set(key, time);
            return true;
        } catch {
            for (const entry of this.active) if (entry.source === source) this.release(entry);
            try { source?.disconnect(); gain?.disconnect(); } catch { /* partial setup */ }
            this.report("cue playback unavailable");
            return false;
        }
    }

    release(entry, stop = true) {
        this.active.delete(entry);
        this.retiring.delete(entry);
        entry.source.onended = null;
        try { if (stop) entry.source.stop(); } catch { /* already ended */ }
        try { entry.source.disconnect(); entry.gain.disconnect(); } catch { /* detached */ }
    }

    retire(entry) {
        const now = this.ctx.currentTime;
        if (entry.startAt > now) { this.release(entry); return; }
        this.active.delete(entry);
        entry.stopAt = now + .003;
        this.retiring.add(entry);
        try {
            entry.gain.gain.setValueAtTime(entry.volume, now);
            entry.gain.gain.linearRampToValueAtTime(0, entry.stopAt);
            entry.source.stop(entry.stopAt);
        } catch { this.release(entry); }
    }

    stopPreviews() {
        for (const entry of this.active) if (entry.preview) this.retire(entry);
    }

    async dispose() {
        this.disposed = true;
        this.generation++;
        for (const entry of this.active) this.release(entry);
        for (const entry of this.retiring) this.release(entry);
        this.buffers.clear(); this.loading.clear(); this.last.clear();
        try { await this.ctx?.close(); } catch { /* shutdown */ }
        this.ctx = null;
    }
}
