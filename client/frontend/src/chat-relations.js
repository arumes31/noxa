// Relationships come from authenticated protocol identifiers, never text or
// whichever conversation happens to be open when a response arrives.
export function resolveParent(messages, message) {
    return message.replyToID ? messages.find(candidate => candidate.id === message.replyToID) || null : null;
}

export function directPeer(event, message) {
    return message.self ? (event.to_unique_id || "") : (message.fromUID || "");
}
