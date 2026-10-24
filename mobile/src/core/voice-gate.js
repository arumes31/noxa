// voice-gate.js — the pure parts of the microphone and playback logic, kept
// free of Web Audio and timers so they can be tested with injected clocks.
//
// The desktop runs its gate in an AudioWorklet (client/frontend/src/
// voice-gate.js); on the phone the level is sampled from an AnalyserNode
// about 20 times a second and the gate opens or closes the sending track.

/** Root mean square of a block of samples (-1..1). */
export function rms(samples) {
    if (!samples?.length) return 0;
    let sum = 0;
    for (const sample of samples) sum += sample * sample;
    return Math.sqrt(sum / samples.length);
}

/** The level a meter shows, 0..1, for an RMS level; METER_MAX is full scale. */
export const METER_MAX = 0.25;
export function meterFraction(level) {
    return Math.max(0, Math.min(1, (level || 0) / METER_MAX));
}

/**
 * Voice activation: opens as soon as the level reaches the threshold and
 * stays open for holdMs after it last did, so word endings and short pauses
 * are not clipped.
 */
export class VoiceGate {
    constructor({ threshold = 0.02, holdMs = 400 } = {}) {
        this.threshold = threshold;
        this.holdMs = holdMs;
        this.open = false;
        this.lastAbove = -Infinity;
    }

    /** Feeds one level sample; returns whether the gate changed state. */
    update(level, now) {
        const wasOpen = this.open;
        if (level >= this.threshold) {
            this.lastAbove = now;
            this.open = true;
        } else if (this.open && now - this.lastAbove >= this.holdMs) {
            this.open = false;
        }
        return wasOpen !== this.open;
    }

    reset() {
        this.open = false;
        this.lastAbove = -Infinity;
    }
}

/**
 * Push-to-talk with a release delay: the microphone opens on press and
 * closes releaseMs after the button is let go (0 closes at once). A new
 * press during the delay keeps it open. Timers are injectable for tests.
 */
export class PushToTalkLatch {
    constructor({ releaseMs = 0, onChange = () => {}, setTimer = (fn, ms) => setTimeout(fn, ms), clearTimer = id => clearTimeout(id) } = {}) {
        this.releaseMs = releaseMs;
        this.onChange = onChange;
        this.setTimer = setTimer;
        this.clearTimer = clearTimer;
        this.active = false;
        this.pressed = false;
        this.timer = null;
    }

    #set(active) {
        if (this.active === active) return;
        this.active = active;
        this.onChange(active);
    }

    press() {
        this.pressed = true;
        if (this.timer !== null) this.clearTimer(this.timer);
        this.timer = null;
        this.#set(true);
    }

    release() {
        if (!this.pressed) return;
        this.pressed = false;
        if (this.timer !== null) this.clearTimer(this.timer);
        this.timer = null;
        if (this.releaseMs <= 0) {
            this.#set(false);
            return;
        }
        this.timer = this.setTimer(() => {
            this.timer = null;
            if (!this.pressed) this.#set(false);
        }, this.releaseMs);
    }

    /** Closes immediately, e.g. when leaving or switching modes. */
    cancel() {
        this.pressed = false;
        if (this.timer !== null) this.clearTimer(this.timer);
        this.timer = null;
        this.#set(false);
    }
}

/** Per-user volumes are stored 0..MAX_VOLUME (2 = 200 %). */
export const MAX_VOLUME = 2;
/** How far other members are lowered while a priority speaker talks. */
export const PRIORITY_DUCK = 0.35;

function clamp(value, fallback = 1) {
    const number = Number(value);
    if (!Number.isFinite(number)) return fallback;
    return Math.max(0, Math.min(MAX_VOLUME, number));
}

/**
 * The gain of one received track. kind is "mic" or "share" (shared-screen
 * audio). Muted users and deafen silence only microphones of others and
 * everything respectively; the master volume scales the result.
 */
export function playbackGain({ uid, kind = "mic", settings = {}, deafened = false, ducked = false }) {
    if (deafened) return 0;
    const master = clamp(settings.masterVolume ?? 1);
    if (kind === "share") return clamp(settings.shareVolumes?.[uid] ?? 1) * master;
    if ((settings.mutedUsers || []).includes(uid)) return 0;
    return clamp(settings.userVolumes?.[uid] ?? 1) * master * (ducked ? PRIORITY_DUCK : 1);
}

/** Publisher and kind of a received audio key ("clientID" or "clientID|screenaudio"). */
export function parseKeyKind(key) {
    const s = String(key ?? "");
    const i = s.indexOf("|");
    if (i < 0) return { clientID: s, kind: "mic" };
    const slot = s.slice(i + 1);
    return { clientID: s.slice(0, i), kind: slot === "mic" ? "mic" : "share" };
}

/** Whole percent for a stored volume (1 -> 100). */
export function volumePercent(volume) {
    return Math.round(clamp(volume) * 100);
}

/** A stored volume from a slider percent (150 -> 1.5). */
export function volumeFromPercent(percent) {
    return clamp(Number(percent) / 100);
}

/** Members in the channel with a raised hand, first raised first. */
export function speakingQueue(clients, channelID) {
    return (clients || []).filter(c => c.channel_id === channelID && c.hand_raised_order > 0)
        .sort((a, b) => a.hand_raised_order - b.hand_raised_order);
}

/** Warns at most once per interval while the level stays loud while muted. */
export class MutedTalkingWarning {
    constructor({ intervalMs = 60000 } = {}) {
        this.intervalMs = intervalMs;
        this.last = -Infinity;
    }

    /** Returns true when a warning should be shown now. */
    check({ muted, level, threshold, now }) {
        if (!muted || level < threshold) return false;
        if (now - this.last < this.intervalMs) return false;
        this.last = now;
        return true;
    }
}
