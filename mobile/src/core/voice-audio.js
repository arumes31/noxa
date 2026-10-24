// voice-audio.js — Web Audio for voice: one AudioContext for the whole app,
// microphone level meters and per-track playback gain (0..200 %).
//
// Received tracks keep their <audio> element: Chrome only feeds a remote
// WebRTC stream into Web Audio while a media element plays it, and Safari
// ignores element.volume. When the context runs, the element is muted and
// the sound goes element-stream -> GainNode -> speakers. Until the first tap
// unlocks audio (iOS, autoplay policies) the element plays directly with
// its volume capped at 100 %.
import { rms } from "./voice-gate.js";

let context = null;
const listeners = new Set();

/** The shared AudioContext, or null where Web Audio is missing. */
export function audioContext() {
    if (context) return context;
    const Context = globalThis.AudioContext || globalThis.webkitAudioContext;
    if (!Context) return null;
    try { context = new Context({ latencyHint: "interactive" }); } catch { return null; }
    context.onstatechange = () => listeners.forEach(listener => listener(context.state));
    return context;
}

/** Calls listener(state) whenever the context starts or stops; returns a stop function. */
export function onAudioState(listener) {
    listeners.add(listener);
    return () => listeners.delete(listener);
}

export function audioRunning() {
    return context?.state === "running";
}

let unlockInstalled = false;

/**
 * Resumes the context on the next touch or key press. iOS only lets audio
 * start inside a user gesture, so this listens for every gesture until the
 * context runs (it can be suspended again by a phone call).
 */
export function installAudioUnlock() {
    if (unlockInstalled || typeof document === "undefined") return;
    unlockInstalled = true;
    const resume = () => {
        const ctx = audioContext();
        if (ctx && ctx.state !== "running") ctx.resume().catch(() => {});
    };
    for (const type of ["pointerdown", "touchend", "keydown"]) document.addEventListener(type, resume, { capture: true, passive: true });
}

/** Level of a microphone stream, sampled on demand. */
export class LevelMeter {
    constructor(stream) {
        const ctx = audioContext();
        if (!ctx || !stream?.getAudioTracks().length) throw new Error("Web Audio unavailable");
        this.source = ctx.createMediaStreamSource(stream);
        this.analyser = ctx.createAnalyser();
        this.analyser.fftSize = 1024;
        this.buffer = new Float32Array(this.analyser.fftSize);
        this.source.connect(this.analyser);
    }

    /** Current RMS level (0..1); 0 while the context is suspended. */
    level() {
        if (!audioRunning()) return 0;
        this.analyser.getFloatTimeDomainData(this.buffer);
        return rms(this.buffer);
    }

    close() {
        try { this.source.disconnect(); } catch { /* already gone */ }
    }
}

/** Per-key playback gain for the <audio> elements of received tracks. */
export class Mixer {
    constructor() {
        /** key -> {element, source, gain, value} */
        this.routes = new Map();
        this.sinkId = "";
        this.stopState = onAudioState(() => this.refresh());
    }

    /** Web Audio routing works when the context runs and can reach the chosen output. */
    #webAudio() {
        if (!audioRunning()) return false;
        return !this.sinkId || typeof context.setSinkId === "function";
    }

    attach(key, element, value) {
        this.detach(key);
        this.routes.set(key, { element, source: null, gain: null, value });
        this.#apply(key);
    }

    detach(key) {
        const route = this.routes.get(key);
        if (!route) return;
        try { route.source?.disconnect(); route.gain?.disconnect(); } catch { /* closed */ }
        this.routes.delete(key);
    }

    setGain(key, value) {
        const route = this.routes.get(key);
        if (!route) return;
        route.value = value;
        this.#apply(key);
    }

    async setSink(deviceId) {
        this.sinkId = deviceId;
        if (audioRunning() && typeof context.setSinkId === "function") await context.setSinkId(deviceId).catch(() => {});
        this.refresh();
    }

    refresh() {
        for (const key of this.routes.keys()) this.#apply(key);
    }

    #apply(key) {
        const route = this.routes.get(key);
        const { element, value } = route;
        if (this.#webAudio() && element.srcObject) {
            if (!route.gain) {
                try {
                    route.source = context.createMediaStreamSource(element.srcObject);
                    route.gain = context.createGain();
                    route.source.connect(route.gain).connect(context.destination);
                } catch {
                    route.source = route.gain = null;
                }
            }
            if (route.gain) {
                route.gain.gain.value = value;
                element.muted = true;
                return;
            }
        } else if (route.gain) {
            try { route.source.disconnect(); route.gain.disconnect(); } catch { /* closed */ }
            route.source = route.gain = null;
        }
        element.muted = value <= 0;
        try { element.volume = Math.min(1, Math.max(0, value)); } catch { /* read-only on iOS */ }
    }

    /** The gain applied to a key right now (for tests and the UI). */
    gainOf(key) {
        return this.routes.get(key)?.value ?? null;
    }

    close() {
        for (const key of [...this.routes.keys()]) this.detach(key);
        this.stopState();
    }
}
