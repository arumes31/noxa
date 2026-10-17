// message-encryption.js decides the single encryption badge every displayed
// message carries. Plaintext chat is not allowed: a body is end-to-end
// encrypted (lock), client→server→client encrypted (shield, channel and
// global chat only), or it stayed sealed / was refused / is a DM the server
// could read (warning). There is no fourth, badge-less state. Direct messages
// are end-to-end only, so a DM never draws the shield.

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

// DOWNGRADED is the mode the native layer gives an opened server-encrypted
// DM. It is never verified.
export const DOWNGRADED = "downgraded";

// encModeFrom maps the native enc_verified / enc_mode pair to the local mode:
// "e2e" or "server" only next to verification, "downgraded" only without it.
export function encModeFrom(verified, mode) {
    if (verified) return mode === "e2e" || mode === "server" ? mode : "";
    return mode === DOWNGRADED ? DOWNGRADED : "";
}

// encryptionBadge returns { kind, label, bodyKey }:
//   kind    "lock" | "shield" | "warning" ("" only for a deletion tombstone,
//           which displays no message content)
//   label   i18n key for the badge's accessible name and tooltip
//   bodyKey i18n key that REPLACES the body (refused or sealed), or ""
//
// m.encMode is local evidence from the native layer ("e2e" | "server", or
// the unverified "downgraded"); a direct message in "server" mode is treated
// as downgraded, since DMs must be end-to-end. The legacy flags (e2e, direct+enc, enc) keep older paths badged. A message with
// none of them was not opened by this client and is refused, except a legacy
// local DM record (m.localUnverified), whose text the user already saw: it
// keeps its text under a warning.
export function encryptionBadge(m) {
    if (!m || m.deleted) return { kind: "", label: "", bodyKey: "" };
    const unopened = UNOPENED_BODIES.get(m.text);
    if (unopened) return { kind: "warning", label: "chat.encryptedHelp", bodyKey: unopened };
    // A DM opened with the server key: the server could read (or have
    // written) it. The body is shown, under a warning, never a lock or shield.
    if (m.encMode === DOWNGRADED || (m.direct && m.encMode === "server")) {
        return { kind: "warning", label: "chat.downgradedHelp", bodyKey: "" };
    }
    if (m.encMode === "e2e" || (m.encMode !== "server" && (m.e2e || (m.direct && m.enc)))) {
        return { kind: "lock", label: m.direct ? "chat.directHelp" : "chat.groupHelp", bodyKey: "" };
    }
    if (m.encMode === "server" || m.enc) {
        return { kind: "shield", label: "chat.channelHelp", bodyKey: "" };
    }
    if (m.localUnverified) return { kind: "warning", label: "chat.unverifiedHelp", bodyKey: "" };
    return { kind: "warning", label: "chat.plaintextHelp", bodyKey: "chat.plaintextRefused" };
}
