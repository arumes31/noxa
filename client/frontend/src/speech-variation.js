// Membership-based selection is identical for every observer. No local RNG,
// clock, tab ID, display name or output device participates in this choice.
export const VARIED_SPEECH_EVENTS = Object.freeze([
    "user_join", "user_leave", "user_disconnected", "user_moved", "user_moved_out",
    "user_kicked", "user_kicked_channel", "buddy_online", "stream_watch_started",
]);
export function presenceSpeechKey(client, channelID) {
    if (!client?.client_id) return "";
    return JSON.stringify([client.client_id, String(client.presence_token ?? 0), Number(channelID) || 0]);
}
export function variedSpeechClip(event, eventKey) {
    if (!VARIED_SPEECH_EVENTS.includes(event) || typeof eventKey !== "string" || !eventKey) return event;
    // Versioned, fixed-size set: upgrading the catalog cannot silently remap
    // one listener to a different voice. Text follows the chosen UI language.
    let hash = 2166136261;
    for (const char of "presence-v1:" + event + ":" + eventKey) {
        hash = Math.imul(hash ^ char.charCodeAt(0), 16777619) >>> 0;
    }
    const index = hash % 10;
    return index ? `${event}_v${String(index).padStart(2, "0")}` : event;
}
