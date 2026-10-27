// Conversation context comes from the renderer, never from names or DOM attributes.
// An unknown destination (including a composer/share target) is private by default.
const contexts = new WeakMap();
export function previewConversation(message) {
    return message && !message.isDM && !message.direct && !message.e2e && message.encMode !== "e2e" &&
        Number.isInteger(message.channelID) && message.channelID >= 0 ? "channel" : "private";
}
export function setPreviewConversation(root, conversation) {
    contexts.set(root, conversation === "channel" ? "channel" : "private");
}
export function previewConversationFor(node) {
    for (let current = node; current; current = current.parentElement) {
        if (contexts.has(current)) return contexts.get(current);
    }
    return "private";
}
