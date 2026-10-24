// settings.js — preferences of this phone, kept in localStorage under one
// key. Screens read with get(), write with set() and follow changes through
// the "change" event. Values never leave the device.

const KEY = "noxa.settings";

export const DEFAULTS = Object.freeze({
    // Appearance
    theme: "midnight", // midnight | arctic | light | contrast | system
    accent: "", // "" keeps the theme's accent
    fontScale: 1, // 0.9 .. 1.3
    compact: false,
    reduceMotion: false, // in addition to the phone's own setting
    // Chat display
    timestamps: "absolute", // absolute | relative | off
    showJoinLeave: true,
    linkPreviews: true,
    inlineImages: true,
    undoSendSeconds: 0, // 0 sends immediately
    keywords: [], // highlighted words
    blockedUsers: [], // unique IDs
    hideChatActivity: false, // no typing or read receipts
    // Voice
    microphoneID: "",
    echoCancellation: true,
    noiseSuppression: true,
    autoGainControl: true,
    activation: "continuous", // continuous | voice | ptt
    voiceThreshold: 0.02,
    pttReleaseMs: 150,
    warnMutedTalking: true,
    lowBandwidth: false,
    masterVolume: 1,
    userVolumes: {}, // unique ID -> 0..3
    mutedUsers: [], // unique IDs muted locally
    shareVolumes: {}, // unique ID -> 0..3 for shared screen audio
    receiveQuality: "auto", // auto | high | medium | low
    cameraEffect: "none", // none | blur (full-frame privacy blur)
    blurStrength: 12, // px, 2 .. 30
    // Notifications
    sounds: true,
    soundVolume: 1,
    speech: false,
    matrix: {}, // event -> {sound, toast, push}
    channelNotify: {}, // channel ID -> "all" | "mentions" | "none"
    chatLevel: "all", // all | mentions | dm
    dnd: false,
    quietFrom: "",
    quietTo: "",
    snoozeUntil: 0,
    channelWatch: {}, // channel ID -> member count
    buddies: [], // unique IDs that notify when online
    // Presence
    autoAwayMinutes: 0,
    autoAwayMessage: "",
    mutedConversations: [], // "dm:<uid>" | "conversation:<id>"
});

let cache = null;
const target = new EventTarget();

function load() {
    if (cache) return cache;
    let stored = {};
    try { stored = JSON.parse(globalThis.localStorage?.getItem(KEY) || "{}") || {}; } catch { stored = {}; }
    cache = { ...DEFAULTS, ...stored };
    return cache;
}

/** The current value of one setting. */
export function get(name) {
    return load()[name];
}

/** Every setting, defaults applied. */
export function all() {
    return { ...load() };
}

/** Stores one or more settings and notifies listeners. */
export function set(values) {
    const current = load();
    const next = { ...current, ...values };
    cache = next;
    const changed = Object.keys(values).filter(name => JSON.stringify(current[name]) !== JSON.stringify(next[name]));
    try {
        const diff = {};
        for (const name of Object.keys(next)) if (JSON.stringify(next[name]) !== JSON.stringify(DEFAULTS[name])) diff[name] = next[name];
        globalThis.localStorage?.setItem(KEY, JSON.stringify(diff));
    } catch { /* private window or quota */ }
    if (changed.length) target.dispatchEvent(new CustomEvent("change", { detail: { changed, settings: next } }));
}

/** Resets settings to their defaults (all of them when names is empty). */
export function reset(names = []) {
    const values = {};
    for (const name of names.length ? names : Object.keys(DEFAULTS)) values[name] = DEFAULTS[name];
    set(values);
}

/** Calls listener({changed, settings}) after every change; returns a stop function. */
export function onChange(listener) {
    const handler = event => listener(event.detail);
    target.addEventListener("change", handler);
    return () => target.removeEventListener("change", handler);
}

/** Quiet hours, DND and snooze: whether alerts are silenced right now. */
export function silenced(now = new Date()) {
    const s = load();
    if (s.dnd) return true;
    if (s.snoozeUntil && s.snoozeUntil > now.getTime()) return true;
    if (s.quietFrom && s.quietTo && s.quietFrom !== s.quietTo) {
        const minutes = now.getHours() * 60 + now.getMinutes();
        const [fh, fm] = s.quietFrom.split(":").map(Number);
        const [th, tm] = s.quietTo.split(":").map(Number);
        const from = fh * 60 + fm, to = th * 60 + tm;
        return from < to ? minutes >= from && minutes < to : minutes >= from || minutes < to;
    }
    return false;
}

/** Test support: forget the cached settings. */
export function _resetCache() {
    cache = null;
}
