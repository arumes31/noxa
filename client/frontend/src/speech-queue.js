// Fixed clip IDs only. This module has no text input, TTS or asset-generation path.
export const SPEECH_EVENTS = {
    banned: { priority: 7, category: "admin", effect: "ban", matrix: "kick" },
    kicked: { priority: 6, category: "admin", effect: "kick", matrix: "kick" },
    kicked_channel: { priority: 6, category: "admin", effect: "kick", matrix: "kick" },
    server_shutdown: { priority: 6, category: "connection", effect: "connection_disconnected" },
    reconnect_failed: { priority: 6, category: "connection", effect: "connection_failed" },
    connection_lost: { priority: 5, category: "connection", effect: "connection_lost" },
    moved_by_admin: { priority: 4, category: "admin", effect: "own_channel_switch" },
    permission_denied: { priority: 4, category: "admin", effect: "server_error" },
    test: { priority: 4, category: "test" },
};

export function speechLanguage(settings, systemLanguage = "en") {
    if (["en", "de"].includes(settings?.speech_language)) return settings.speech_language;
    const language = settings?.language;
    const selected = language === "system" || !language ? systemLanguage : language;
    return typeof selected === "string" && selected.toLowerCase().startsWith("de") ? "de" : "en";
}

export class SpeechQueue {
    constructor({ engine, assets, getState, isDND, systemLanguage = () => "en", now = () => Date.now(), schedule = (fn, ms) => setTimeout(fn, ms), cancel = id => clearTimeout(id) }) {
        Object.assign(this, { engine, assets, getState, isDND, systemLanguage, now, schedule, cancel });
        this.pending = []; this.current = null; this.timer = null; this.last = new Map();
    }

    allowed(event, settings, preview) {
        const def = SPEECH_EVENTS[event];
        if (!def || !settings || this.isDND(settings) || settings.spoken_messages === false) return false;
        if (!preview && (settings.play_sounds === false || this.getState()?.replayingTabID)) return false;
        if (settings.speech_events?.[event] === false || settings.event_sounds?.[def.effect] === false) return false;
        if (def.matrix && (settings.notify_matrix?.[def.matrix]?.sound === false || settings.event_sounds?.[def.matrix] === false)) return false;
        if (event === "permission_denied" && settings.speech_permissions === false) return false;
        if (["banned", "kicked", "kicked_channel"].includes(event) && settings.speech_removal === false) return false;
        return def.category === "test" || settings["speech_" + def.category] !== false;
    }

    scope() {
        const state = this.getState();
        return `${state?.activeTabID || ""}:${state?.serverGeneration || 0}`;
    }

    hasLiveSpeech() {
        return (this.current && !this.current.preview) || this.pending.some(item => !item.preview);
    }

    enqueue(event, { settings, preview = false, delay = 150, withEffect = false, effect, onEnded } = {}) {
        if (!Object.hasOwn(SPEECH_EVENTS, event)) return false;
        if (preview && this.hasLiveSpeech()) return false;
        if (!preview) this.stopPreview();
        const selected = settings || this.getState()?.settings;
        const now = this.now(), def = SPEECH_EVENTS[event], scope = this.scope();
        const key = scope + ":" + event;
        const coolingDown = !preview && now - (this.last.get(key) ?? -Infinity) < 10000;
        const item = { event, scope, priority: def.priority, settings, preview, onEnded,
            ready: now + delay, expires: now + 8000, waitingEffect: false };
        let effectPlayed = false;
        if (withEffect && (!def.matrix || (selected?.notify_matrix?.[def.matrix]?.sound !== false && selected?.event_sounds?.[def.matrix] !== false))) {
            item.waitingEffect = true;
            effectPlayed = this.engine.play(effect || def.effect, { settings: selected, scope,
                onEnded: () => {
                    item.waitingEffect = false;
                    item.ready = Math.max(item.ready, this.now() + 150);
                    this.pump();
                } });
            if (!effectPlayed) item.waitingEffect = false;
        }
        if (coolingDown || !this.allowed(event, selected, preview)) return effectPlayed;
        if (!preview && this.current?.scope === scope && this.current?.priority > def.priority) return effectPlayed;
        if (!preview) {
            this.last.set(key, now);
            while (this.last.size > 128) this.last.delete(this.last.keys().next().value);
        }
        // Terminal/high-priority messages replace obsolete connection/admin speech.
        this.pending = this.pending.filter(entry => entry.scope !== scope || entry.priority > def.priority);
        if (this.current && (preview || (this.current.scope === scope && def.priority > this.current.priority))) this.stopCurrent();
        this.pending.push(item);
        this.pending.sort((a, b) => b.priority - a.priority);
        this.pending = this.pending.slice(0, 3);
        this.pump();
        return true;
    }

    pump() {
        if (this.current) return;
        this.cancel(this.timer); this.timer = null;
        while (this.pending.length) {
            const item = this.pending[0], now = this.now();
            const settings = item.settings || this.getState()?.settings;
            if (item.expires < now || (!item.preview && item.scope !== this.scope()) || !this.allowed(item.event, settings, item.preview)) { this.pending.shift(); item.onEnded?.(); continue; }
            if (item.waitingEffect) {
                this.timer = this.schedule(() => this.pump(), Math.max(1, item.expires - now + 1));
                return;
            }
            if (item.ready > now) { this.timer = this.schedule(() => this.pump(), item.ready - now); return; }
            this.pending.shift();
            const language = speechLanguage(settings, this.systemLanguage());
            const clip = this.assets[language]?.[item.event];
            if (!clip) { item.onEnded?.(); continue; } // No other-language or generated fallback.
            const id = "speech_" + language + "_" + item.event;
            this.current = { ...item, id };
            const playing = this.current;
            const played = this.engine.play(id, { preview: item.preview, settings, scope: item.scope, volume: settings.speech_volume ?? 100,
                onEnded: () => { if (this.current === playing) { this.current = null; item.onEnded?.(); this.pump(); } } });
            if (played) return;
            this.current = null;
            item.onEnded?.();
        }
    }

    stopCurrent() {
        if (this.current) {
            const stopped = this.current;
            this.current = null;
            for (const entry of this.engine.active) if (entry.family === stopped.id) this.engine.retire(entry);
            stopped.onEnded?.();
        }
    }

    clear(category) {
        const matches = item => !category || SPEECH_EVENTS[item.event].category === category;
        this.pending = this.pending.filter(item => !matches(item));
        if (this.current && matches(this.current)) this.stopCurrent();
        this.cancel(this.timer); this.timer = null;
        this.pump();
    }

    stopPreview() {
        this.pending = this.pending.filter(item => !item.preview);
        if (this.current?.preview) this.stopCurrent();
        this.pump();
    }

    reconcile() {
        if (this.current && !this.allowed(this.current.event, this.current.settings || this.getState()?.settings, this.current.preview)) this.stopCurrent();
        this.pump();
    }
}
