// Voice is more important than video and must always go through.
//
// `priority` is the browser's bitrate-allocation weight between senders;
// `networkPriority` selects the packet marking (DSCP EF for "high" audio) and
// send-queue class where the platform supports it. The microphone is high on
// both. Video keeps its existing allocation weight but is never placed in the
// voice network class, so a congested uplink queue drains voice first.

export const VOICE_PRIORITY = "high";
export const VIDEO_NETWORK_PRIORITY = "low";

// Fresh initial encoding for a microphone transceiver (addTransceiver).
export function voiceSendEncoding() {
    return { priority: VOICE_PRIORITY, networkPriority: VOICE_PRIORITY };
}

// Mark existing microphone sender parameters in place; returns them.
export function applyVoicePriority(parameters) {
    for (const encoding of parameters?.encodings || []) {
        encoding.priority = VOICE_PRIORITY;
        encoding.networkPriority = VOICE_PRIORITY;
    }
    return parameters;
}

// Keep a video encoding out of the voice network class without touching its
// bitrate-allocation weight; returns the encoding.
export function applyVideoNetworkPriority(encoding) {
    if (encoding) encoding.networkPriority = VIDEO_NETWORK_PRIORITY;
    return encoding;
}
