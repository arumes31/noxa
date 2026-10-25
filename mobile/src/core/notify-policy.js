// notify-policy.js — what an incoming event does on this phone: sound,
// in-app banner (toast), push while closed, and whether it counts as
// unread. Pure functions over the settings object so the rules are tested
// without a DOM; app.js and the notifier apply the decisions, and the push
// part is sent to the server as filters (internal/server/push.go).

/** Rows of the notification matrix, in the order the settings show them. */
export const EVENTS = Object.freeze(["mention", "keyword", "dm", "group", "channel", "poke", "whisper", "buddy_online", "channel_watch", "call"]);

/** Events the server can push; the others only exist while the app runs. */
export const PUSH_EVENTS = Object.freeze({ mention: "mention", dm: "dm", group: "conversation", poke: "poke", call: "call" });

/** Per-channel levels: everything, only mentions and keywords, count silently, or ignore. */
export const CHANNEL_LEVELS = Object.freeze(["all", "mentions", "none", "mute"]);

/** Chat levels: which conversations may alert at all. */
export const CHAT_LEVELS = Object.freeze(["all", "mentions", "dm"]);

/** The outputs an event has until the user changes its row. */
export function defaultRow(event) {
    switch (event) {
        case "channel": return { sound: false, toast: false, push: false };
        case "buddy_online":
        case "channel_watch":
        case "whisper": return { sound: true, toast: true, push: false };
        default: return { sound: true, toast: true, push: !!PUSH_EVENTS[event] };
    }
}

export function matrixRow(settings, event) {
    const row = { ...defaultRow(event), ...(settings.matrix?.[event] || {}) };
    if (!PUSH_EVENTS[event]) row.push = false;
    return row;
}

export function channelLevel(settings, channelID) {
    const level = settings.channelNotify?.[String(channelID)];
    return CHANNEL_LEVELS.includes(level) ? level : "all";
}

function minutesOf(value) {
    const match = /^(\d{2}):(\d{2})$/.exec(value || "");
    return match ? Number(match[1]) * 60 + Number(match[2]) : null;
}

/** Whether DND, an active snooze or quiet hours silence alerts at now. */
export function silencedAt(settings, now = new Date()) {
    if (settings.dnd) return true;
    if (settings.snoozeUntil && settings.snoozeUntil > now.getTime()) return true;
    const from = minutesOf(settings.quietFrom), to = minutesOf(settings.quietTo);
    if (from === null || to === null || from === to) return false;
    const minute = now.getHours() * 60 + now.getMinutes();
    return from < to ? minute >= from && minute < to : minute >= from || minute < to;
}

/** Whether text contains one of the keywords (case-insensitive). */
export function matchesKeyword(text, keywords = []) {
    if (typeof text !== "string" || !text) return false;
    const lower = text.toLowerCase();
    return keywords.some(word => typeof word === "string" && word.trim() && lower.includes(word.trim().toLowerCase()));
}

/** The matrix event of a channel message. */
export function channelEvent({ mentioned = false, text = "", keywords = [] } = {}) {
    if (mentioned) return "mention";
    return matchesKeyword(text, keywords) ? "keyword" : "channel";
}

const NOTHING = Object.freeze({ sound: false, toast: false, push: false, count: false });

/**
 * Decides one event. event is a matrix row; tag the conversation
 * ("channel:4", "dm:<uid>", "conversation:<id>") or ""; channelID the
 * channel of channel events; from the sender's unique ID; viewing whether
 * the conversation is on screen right now.
 */
export function decide({ event, tag = "", channelID = null, from = "", viewing = false, settings, now = new Date() }) {
    if (from && (settings.blockedUsers || []).includes(from)) return NOTHING;
    const row = matrixRow(settings, event);
    let alert = true;
    let count = !!tag;
    const level = channelID === null ? "all" : channelLevel(settings, channelID);
    if (level === "mute") return NOTHING;
    const direct = event === "mention" || event === "keyword";
    if (level === "none" || (level === "mentions" && !direct)) alert = false;
    if (tag && (settings.mutedConversations || []).includes(tag)) alert = false;
    const chat = settings.chatLevel || "all";
    if (chat === "mentions" && (event === "channel" || event === "group")) alert = false;
    if (chat === "dm" && (event === "channel" || event === "group" || direct)) alert = false;
    const push = alert && row.push;
    if (silencedAt(settings, now)) alert = false;
    if (viewing) {
        alert = false;
        count = false;
    }
    return { sound: alert && row.sound, toast: alert && row.toast, push, count };
}

/** Far enough ahead to mean "until DND is turned off" (2100-01-01). */
const FOREVER = 4102444800;

/**
 * The server-side push filters (netproto.PushFilters) for these settings:
 * pushes then follow the same matrix, levels and quiet times while the app
 * is closed.
 */
export function pushFilters(settings, now = new Date()) {
    const mutedChannels = Object.entries(settings.channelNotify || {})
        .filter(([, level]) => level === "none" || level === "mute")
        .map(([id]) => Number(id)).filter(id => Number.isSafeInteger(id) && id >= 0);
    const blocked = settings.blockedUsers || [];
    const mutedTags = [...new Set([...(settings.mutedConversations || []), ...blocked.flatMap(uid => [`dm:${uid}`, `poke:${uid}`])])];
    const mutedKinds = Object.entries(PUSH_EVENTS).filter(([event]) => !matrixRow(settings, event).push).map(([, kind]) => kind);
    const snooze = settings.snoozeUntil > now.getTime() ? Math.ceil(settings.snoozeUntil / 1000) : 0;
    const filters = {
        muted_channels: mutedChannels.slice(0, 500),
        muted_tags: mutedTags.slice(0, 500),
        muted_kinds: mutedKinds,
        chat_level: CHAT_LEVELS.includes(settings.chatLevel) ? settings.chatLevel : "all",
        dnd_until: settings.dnd ? FOREVER : snooze,
        utc_offset_minutes: -now.getTimezoneOffset(),
    };
    if (minutesOf(settings.quietFrom) !== null && minutesOf(settings.quietTo) !== null) {
        filters.quiet_from = settings.quietFrom;
        filters.quiet_to = settings.quietTo;
    }
    return filters;
}

/** The end of a snooze: minutes from now, or "tomorrow" (next 08:00). */
export function snoozeEnd(choice, now = new Date()) {
    if (choice === "tomorrow") {
        const end = new Date(now);
        end.setDate(end.getDate() + 1);
        end.setHours(8, 0, 0, 0);
        return end.getTime();
    }
    return now.getTime() + Number(choice) * 60000;
}
