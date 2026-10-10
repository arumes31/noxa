// Fixed clip IDs only. This module has no text input, TTS or asset-generation path.
import { soundVolume } from "./sound-engine.js";
export const SPEECH_EVENTS = {
    microphone_muted: { priority: 3, category: "audio", family: "microphone", effect: "mic_off", cooldown: 0 },
    microphone_unmuted: { priority: 3, category: "audio", family: "microphone", effect: "mic_on", cooldown: 0 },
    sound_muted: { priority: 3, category: "audio", family: "deafen", effect: "deafen_on", cooldown: 0 },
    sound_unmuted: { priority: 3, category: "audio", family: "deafen", effect: "deafen_off", cooldown: 0 },
    client_closing: { priority: 8, category: "application" },
    banned: { priority: 7, category: "admin", effect: "ban", matrix: "kick" },
    kicked: { priority: 6, category: "admin", effect: "kick", matrix: "kick" },
    kicked_channel: { priority: 6, category: "admin", effect: "kick", matrix: "kick" },
    server_shutdown: { priority: 6, category: "connection", family: "connection", effect: "connection_disconnected" },
    reconnect_failed: { priority: 6, category: "connection", family: "connection", effect: "connection_failed" },
    connection_lost: { priority: 5, category: "connection", family: "connection", effect: "connection_lost" },
    connection_reconnected: { priority: 3, category: "connection", family: "connection", effect: "connection_reconnected", cooldown: 1000 },
    connection_connected: { priority: 2, category: "connection", family: "connection", effect: "connection_connected", cooldown: 100 },
    connection_disconnected: { priority: 2, category: "connection", family: "connection", effect: "connection_disconnected", cooldown: 100 },
    connection_reconnecting: { priority: 1, category: "connection", family: "connection", effect: "connection_reconnecting", cooldown: 5000 },
    connection_failed: { priority: 3, category: "connection", family: "connection", effect: "connection_failed", cooldown: 3000 },
    disconnect_failed: { priority: 3, category: "connection", family: "connection", effect: "connection_failed", cooldown: 3000 },
    server_error: { priority: 3, category: "admin", effect: "server_error", cooldown: 3000 },
    moved_by_admin: { priority: 4, category: "admin", effect: "own_channel_switch" },
    permission_denied: { priority: 4, category: "admin", effect: "server_error" },
    user_kicked: { priority: 3, category: "admin", effect: "kick", matrix: "kick", cooldown: 1000 },
    user_kicked_channel: { priority: 3, category: "admin", effect: "kick", matrix: "kick", cooldown: 1000 },
    channel_join: { priority: 2, category: "channel", effect: "own_channel_join", cooldown: 1000 },
    channel_leave: { priority: 2, category: "channel", effect: "own_channel_leave", cooldown: 1000 },
    user_moved_out: { priority: 2, category: "channel", effect: "user_move_out", matrix: "join_leave", cooldown: 1000 },
    user_join: { priority: 1, category: "channel", effect: "user_join", matrix: "join_leave", cooldown: 1000 },
    user_leave: { priority: 1, category: "channel", effect: "user_leave", matrix: "join_leave", cooldown: 1000 },
    user_disconnected: { priority: 1, category: "channel", effect: "user_leave", matrix: "join_leave", cooldown: 1000 },
    user_moved: { priority: 1, category: "channel", effect: "user_move_out", matrix: "join_leave", cooldown: 1000 },
    poke: { priority: 2, category: "notification", effect: "poke", matrix: "poke", cooldown: 5000 },
    buddy_online: { priority: 1, category: "notification", effect: "buddy_online", matrix: "buddy_online", cooldown: 5000 },
    channel_watch: { priority: 0, category: "notification", effect: "channel_watch", matrix: "channel_watch", cooldown: 10000 },
    stream_watch_started: { priority: 1, category: "notification", effect: "stream_watch_started", cooldown: 3000 },
    test: { priority: 4, category: "test" },
};

export function speechLanguage(settings, systemLanguage = "en") {
    if (["en", "de"].includes(settings?.speech_language)) return settings.speech_language;
    const language = settings?.language;
    const selected = language === "system" || !language ? systemLanguage : language;
    return typeof selected === "string" && selected.toLowerCase().startsWith("de") ? "de" : "en";
}

// Explicit speech choices take precedence over legacy effect switches. Keep
// old muted choices until the user configures the corresponding announcement.
export function speechEventEnabled(settings, event, effect = SPEECH_EVENTS[event]?.effect) {
    return settings?.speech_events?.[event] ?? (settings?.event_sounds?.[effect] !== false);
}

export class SpeechQueue {
    constructor({ engine, assets, getState, isDND, systemLanguage = () => "en", now = () => Date.now(), schedule = (fn, ms) => setTimeout(fn, ms), cancel = id => clearTimeout(id) }) {
        Object.assign(this, { engine, assets, getState, isDND, systemLanguage, now, schedule, cancel });
        this.pending = []; this.current = null; this.timer = null; this.last = new Map();
        this.reconnecting = new Set();
        this.connectionState = new Map();
    }

    allowed(event, settings, preview, effect = SPEECH_EVENTS[event]?.effect) {
        const def = SPEECH_EVENTS[event];
        if (!def || !settings || this.isDND(settings) || settings.spoken_messages === false) return false;
        if (!preview && (settings.play_sounds === false || (def.category !== "application" && this.getState()?.replayingTabID))) return false;
        if (!speechEventEnabled(settings, event, effect)) return false;
        if (def.matrix && (settings.notify_matrix?.[def.matrix]?.sound === false || settings.event_sounds?.[def.matrix] === false)) return false;
        if (event === "permission_denied" && settings.speech_permissions === false) return false;
        if (["banned", "kicked", "kicked_channel", "user_kicked", "user_kicked_channel"].includes(event) && settings.speech_removal === false) return false;
        return def.category === "test" || settings["speech_" + def.category] !== false;
    }

    scope() {
        const state = this.getState();
        return `${state?.activeTabID || ""}:${state?.serverGeneration || 0}`;
    }

    hasLiveSpeech() {
        return (this.current && !this.current.preview) || this.pending.some(item => !item.preview);
    }

    enqueue(event, { settings, preview = false, delay = 150, effect, onEnded } = {}) {
        if (!Object.hasOwn(SPEECH_EVENTS, event)) return false;
        if (preview && this.hasLiveSpeech()) return false;
        if (!preview) this.stopPreview();
        const selected = settings || this.getState()?.settings;
        const now = this.now(), def = SPEECH_EVENTS[event], scope = this.scope();
        const key = scope + ":" + event;
        const coolingDown = !preview && now - (this.last.get(key) ?? -Infinity) < (def.cooldown ?? 10000);
        if (!preview && def.family === "connection") {
            if (event === "connection_reconnecting") {
                // Persistent retries report the outage once, even after the
                // short duplicate cooldown expires. A new connection edge re-arms it.
                if (this.reconnecting.has(scope)) return false;
            } else {
                // Audio cooldown does not decide whether the connection changed:
                // a new loss after recovery invalidates it even when muted.
                const duplicateLoss = event === "connection_lost" && this.connectionState.get(scope) === event;
                if (duplicateLoss && coolingDown) return false;
                if (!duplicateLoss) this.reconnecting.delete(scope);
                this.connectionState.set(scope, event);
                while (this.connectionState.size > 128) this.connectionState.delete(this.connectionState.keys().next().value);
                // New connection state invalidates obsolete state even if its
                // own recording is muted. Loss cannot displace a terminal shutdown.
                const obsolete = entry => entry?.scope === scope
                    && SPEECH_EVENTS[entry.event]?.family === "connection" && entry.event !== event
                    && (event !== "connection_lost" || entry.priority <= def.priority);
                this.pending = this.pending.filter(entry => !obsolete(entry));
                if (obsolete(this.current)) this.stopCurrent();
            }
        }
        const item = { event, scope, priority: def.priority, settings, preview, onEnded, effect: effect || def.effect,
            ready: now + delay, expires: now + 8000 };
        // Spoken actions never play an effect, even when their recording is
        // disabled, silent or unavailable.
        const language = speechLanguage(selected, this.systemLanguage());
        if (!this.allowed(event, selected, preview, item.effect) || !soundVolume(selected?.speech_volume ?? 100)
            || !this.assets[language]?.[event]) return false;
        if (coolingDown || (!preview && this.current?.scope === scope && this.current?.priority > def.priority)) return false;
        if (!preview) {
            this.last.set(key, now);
            while (this.last.size > 128) this.last.delete(this.last.keys().next().value);
            if (event === "connection_reconnecting") {
                this.reconnecting.add(scope);
                while (this.reconnecting.size > 128) this.reconnecting.delete(this.reconnecting.values().next().value);
            }
        }
        // Terminal/high-priority messages replace obsolete connection/admin speech.
        this.pending = this.pending.filter(entry => entry.scope !== scope || entry.priority > def.priority);
        const replacesCurrentState = def.family && SPEECH_EVENTS[this.current?.event]?.family === def.family;
        if (this.current && (preview || (this.current.scope === scope && (def.priority > this.current.priority || replacesCurrentState)))) this.stopCurrent();
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
            if (item.expires < now || (!item.preview && item.scope !== this.scope()) || !this.allowed(item.event, settings, item.preview, item.effect)) { this.pending.shift(); item.onEnded?.(); continue; }
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
        // Live timers continue independently. Preview cleanup must not start
        // overdue speech before an incoming state event can invalidate it.
    }

    reconcile() {
        if (this.current && !this.allowed(this.current.event, this.current.settings || this.getState()?.settings, this.current.preview, this.current.effect)) this.stopCurrent();
        this.pump();
    }
}
