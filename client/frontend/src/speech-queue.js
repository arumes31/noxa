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
        if (!def || !settings || this.isDND() || settings.spoken_messages === false) return false;
        if (!preview && (settings.play_sounds === false || this.getState()?.replayingTabID)) return false;
        if (settings.speech_events?.[event] === false || settings.event_sounds?.[def.effect] === false) return false;
        if (def.matrix && (settings.notify_matrix?.[def.matrix]?.sound === false || settings.event_sounds?.[def.matrix] === false)) return false;
        return def.category === "test" || settings["speech_" + def.category] !== false;
    }

    enqueue(event, { settings, preview = false, delay = 150 } = {}) {
        if (!Object.hasOwn(SPEECH_EVENTS, event)) return false;
        const selected = settings || this.getState()?.settings;
        if (!this.allowed(event, selected, preview)) return false;
        const now = this.now(), def = SPEECH_EVENTS[event];
        if (!preview && this.current?.priority > def.priority) return false;
        if (!preview && now - (this.last.get(event) ?? -Infinity) < 10000) return false;
        this.last.set(event, now);
        // Terminal/high-priority messages replace obsolete connection/admin speech.
        this.pending = this.pending.filter(item => item.priority > def.priority);
        if (this.current && (preview || def.priority > this.current.priority)) this.stopCurrent();
        this.pending.push({ event, priority: def.priority, settings, preview, ready: now + delay, expires: now + 8000 });
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
            if (item.expires < now || !this.allowed(item.event, settings, item.preview)) { this.pending.shift(); continue; }
            if (item.ready > now) { this.timer = this.schedule(() => this.pump(), item.ready - now); return; }
            this.pending.shift();
            const language = speechLanguage(settings, this.systemLanguage());
            const clip = this.assets[language]?.[item.event];
            if (!clip) continue; // No other-language or generated fallback.
            const id = "speech_" + language + "_" + item.event;
            this.current = { ...item, id };
            const playing = this.current;
            const played = this.engine.play(id, { force: item.preview, settings, volume: settings.speech_volume ?? 100,
                onEnded: () => { if (this.current === playing) { this.current = null; this.pump(); } } });
            if (played) return;
            this.current = null;
        }
    }

    stopCurrent() {
        if (this.current) {
            for (const entry of this.engine.active) if (entry.family === this.current.id) this.engine.retire(entry);
            this.current = null;
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
