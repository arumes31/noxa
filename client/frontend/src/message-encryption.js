// message-encryption.js decides the single encryption badge every displayed
// message carries. Plaintext chat is not allowed: a body is end-to-end
// encrypted (lock), client→server→client encrypted (shield), or it stayed
// sealed / was refused (warning). There is no fourth, badge-less state.

// UNOPENED_BODIES are the exact strings the Go layer substitutes for a body it
// could not show, mapped to the text displayed instead. They must match
// client/e2e.go (and client/conversation.go for private groups) character for
// character — a drift would turn a refusal into an ordinary-looking message.
export const UNOPENED_BODIES = new Map([
    ["[encrypted message — key unavailable]", "chat.keyUnavailable"],
    ["[encrypted message — you do not have access]", "chat.noAccess"],
    ["[refused: server sent plaintext history]", "chat.plaintextRefused"],
    ["[encrypted message — decryption failed]", "chat.decryptFailed"],
    ["[Encryption key unavailable]", "chat.keyUnavailable"],
    ["[Message authentication failed]", "chat.decryptFailed"],
]);

// encryptionBadge returns { kind, label, bodyKey }:
//   kind    "lock" | "shield" | "warning" ("" only for a deletion tombstone,
//           which displays no message content)
//   label   i18n key for the badge's accessible name and tooltip
//   bodyKey i18n key that REPLACES the body (refused or sealed), or ""
//
// m.encMode is local evidence from the native layer ("e2e" | "server"); the
// legacy flags (e2e, direct+enc, enc) keep older paths badged. A message with
// none of them was not opened by this client and is refused, except a legacy
// local DM record (m.localUnverified), whose text the user already saw: it
// keeps its text under a warning.
export function encryptionBadge(m) {
    if (!m || m.deleted) return { kind: "", label: "", bodyKey: "" };
    const unopened = UNOPENED_BODIES.get(m.text);
    if (unopened) return { kind: "warning", label: "chat.encryptedHelp", bodyKey: unopened };
    if (m.encMode === "e2e" || (m.encMode !== "server" && (m.e2e || (m.direct && m.enc)))) {
        return { kind: "lock", label: m.direct ? "chat.directHelp" : "chat.groupHelp", bodyKey: "" };
    }
    if (m.encMode === "server" || m.enc) {
        return { kind: "shield", label: m.direct ? "chat.directServerHelp" : "chat.channelHelp", bodyKey: "" };
    }
    if (m.localUnverified) return { kind: "warning", label: "chat.unverifiedHelp", bodyKey: "" };
    return { kind: "warning", label: "chat.plaintextHelp", bodyKey: "chat.plaintextRefused" };
}
