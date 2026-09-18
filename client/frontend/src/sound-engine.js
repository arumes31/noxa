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
        definitions = SOUND_DEFINITIONS, urls = SOUND_URLS, onStatusChange = () => {} }) {
        Object.assign(this, { getState, isDND, createContext, load, now, warn, onStatusChange });
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
        this.outputReady = false;
        this.outputState = "uninitialized";
        this.generation = 0;
        this.disposed = false;
    }

    report(kind) {
        if (this.warnings.has(kind)) return;
        this.warnings.add(kind);
        this.warn(`noXa sound: ${kind}`);
    }

    context() {
        if (this.disposed) return null;
        try {
            if (!this.ctx || this.ctx.state === "closed") {
                this.ctx = this.createContext();
                this.ctx.addEventListener?.("statechange", this.onStatusChange);
                this.requestedSink = undefined;
            }
            return this.ctx;
        } catch { this.setOutputState("unavailable"); this.report("audio context unavailable"); return null; }
    }

    async preload(names = Object.keys(this.definitions)) {
        const ctx = this.context();
        if (!ctx) return;
        const generation = this.generation;
        await this.setOutput(this.getState()?.settings?.playback_device_id);
        await Promise.all(names.map((name) => {
            if (!Object.hasOwn(this.definitions, name)) return undefined;
            if (this.buffers.has(name)) return undefined;
            if (!this.loading.has(name)) {
                const task = Promise.resolve().then(() => this.load(this.urls[name]))
                    .then(data => ctx.decodeAudioData(data))
                    .then(buffer => {
                        if (!this.disposed && generation === this.generation) this.buffers.set(name, buffer);
                    }).catch(() => this.report(`could not load ${name}`))
                    .finally(() => {
                        if (this.loading.get(name) === task) this.loading.delete(name);
                        this.onStatusChange();
                    });
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
        this.outputReady = false;
        this.setOutputState("routing");
        let routed = false;
        let fallback = false;
        this.output = this.output.then(async () => {
            if (this.disposed || ctx !== this.ctx || this.requestedSink !== sink) return;
            if (typeof ctx.setSinkId !== "function") {
                fallback = !!sink;
                if (sink) this.report("this WebView uses the default sound output");
                routed = true;
                return;
            }
            try { await ctx.setSinkId(sink); routed = true; }
            catch {
                fallback = true;
                this.report("selected output unavailable; using system default");
                try { await ctx.setSinkId(""); routed = true; } catch { this.report("output routing unavailable"); }
            }
        }).catch(() => this.report("output routing unavailable"))
            .finally(() => {
                if (!this.disposed && ctx === this.ctx && this.requestedSink === sink) {
                    this.outputReady = routed;
                    this.setOutputState(routed ? (fallback ? "fallback" : "ready") : "unavailable");
                }
            });
        return this.output;
    }

    setOutputState(status) {
        if (this.outputState === status) return;
        this.outputState = status;
        this.onStatusChange();
    }

    blockReason(name, options = {}, playback = true) {
        const state = this.getState();
        const settings = options.settings || state?.settings;
        const def = this.definitions[name];
        if (!Object.hasOwn(this.definitions, name) || !settings) return "unavailable";
        if (!options.preview && settings.play_sounds === false) return "master_muted";
        if (!options.preview && state?.replayingTabID) return "history";
        if (this.isDND(settings)) return "dnd";
        if (def.category !== "Speech" && settings.effects_enabled === false) return "effects_disabled";
        if (settings.event_sounds?.[name] === false) return "event_disabled";
        if (!soundVolume(options.volume ?? settings.sound_volume)) return "volume_zero";
        if (playback) {
            if (this.outputState === "unavailable") return "output_unavailable";
            if (this.outputState === "routing") return "routing";
            if (this.ctx?.state !== "running") return "suspended";
            if (!this.buffers.has(name)) return this.warnings.has(`could not load ${name}`) ? "load_failed" : "loading";
        }
        return "";
    }

    allowed(name, options) { return !this.blockReason(name, options, false); }

    duckFactor(settings) {
        const state = this.getState();
        return settings?.duck_effects_while_speaking && state?.myChannelID > 0
            && state.clients?.some(client => client.channel_id === state.myChannelID && client.is_speaking) ? .35 : 1;
    }

    updateDucking() {
        for (const entry of this.active) {
            if (!entry.duckEligible) continue;
            const volume = entry.baseVolume * this.duckFactor(entry.settings || this.getState()?.settings);
            if (entry.volume === volume) continue;
            const now = this.ctx.currentTime, param = entry.gain.gain;
            param.cancelScheduledValues?.(now);
            param.setValueAtTime(param.value, now);
            param.linearRampToValueAtTime(volume, now + .015);
            entry.volume = volume;
        }
    }

    play(name, options = {}) {
        if (!Object.hasOwn(this.definitions, name) || !this.allowed(name, options)) return false;
        const settings = options.settings || this.getState().settings;
        const def = this.definitions[name];
        const baseVolume = soundVolume(options.volume ?? settings.sound_volume) * (def.gain ?? 1);
        if (!baseVolume) return false;
        const duckEligible = def.category !== "Speech" && def.priority < 3;
        const volume = baseVolume * (duckEligible ? this.duckFactor(settings) : 1);
        const time = this.now();
        const state = this.getState();
        const scope = options.scope ?? `${state?.activeTabID || ""}:${state?.serverGeneration || 0}`;
        const key = scope + ":" + (def.category === "Other users" ? "movement" : name);
        if (!options.preview && time - (this.last.get(key) ?? -Infinity) < def.cooldown) return false;
        const ctx = this.context();
        if (!ctx) return false;
        // Never queue a stale PTT transition or replay an event after autoplay
        // unlock. Startup/gesture preloading prepares subsequent live actions.
        if (!this.buffers.has(name) || ctx.state !== "running") {
            void this.preload([name]);
            void this.resume();
            return false;
        }
        void this.setOutput(settings.playback_device_id);
        if (!this.outputReady) return false;
        // Audio advances independently of the main thread. Reclaim completed
        // fades even when a burst delays delivery of their ended callbacks.
        for (const entry of this.retiring) {
            if (entry.stopAt <= ctx.currentTime) this.release(entry, false);
        }
        const family = name.startsWith("ptt_") ? "ptt" : name;
        for (const entry of this.active) {
            if (entry.family === family && entry.scope === scope) this.retire(entry);
        }
        if (this.active.size >= 4) {
            const victim = [...this.active].sort((a, b) => a.priority - b.priority)[0];
            if (!options.preview && victim.priority > def.priority) return false;
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
            const entry = { source, gain, family, scope, startAt, volume, baseVolume, duckEligible, settings: options.settings,
                priority: def.priority, preview: !!options.preview, onEnded: options.onEnded };
            source.onended = () => this.release(entry, false);
            this.active.add(entry);
            source.start(startAt);
            this.last.set(key, time);
            if (this.last.size > 256) this.last.delete(this.last.keys().next().value);
            return true;
        } catch {
            for (const entry of this.active) if (entry.source === source) this.release(entry);
            try { source?.disconnect(); gain?.disconnect(); } catch { /* partial setup */ }
            this.report("cue playback unavailable");
            return false;
        }
    }

    release(entry, stop = true) {
        if (entry.released) return;
        entry.released = true;
        this.active.delete(entry);
        this.retiring.delete(entry);
        entry.source.onended = null;
        try { if (stop) entry.source.stop(); } catch { /* already ended */ }
        try { entry.source.disconnect(); entry.gain.disconnect(); } catch { /* detached */ }
        entry.onEnded?.();
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
