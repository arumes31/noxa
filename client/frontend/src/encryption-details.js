import { t } from "./i18n.js";

export function compactMediaEncryptionStatus(media = {}) {
    let status;
    switch (media.state) {
        case "encrypted": status = !media.recipients ? t("security.compact.ready")
            : media.keyHolders?.length ? "E2EE" : t("security.compact.preparing"); break;
        case "keying": status = t("security.compact.preparing"); break;
        case "partial": status = t("security.compact.partial"); break;
        case "paused": status = t("security.compact.paused"); break;
        default: return "";
    }
    if (media.waiting && media.state !== "paused") status += ` · ${t("security.compact.waiting")}`;
    if (media.membershipMismatch?.length) status += `\n${t("security.compact.mismatch")}`;
    return status;
}

// Everyday lock hovers show state, not protocol internals or account IDs.
// Never imply E2EE for server-readable channel chat or private-call media.
export function encryptionDetails({ session, diagnostics, media = {}, capability, voiceOnly = false } = {}) {
    const rows = [];
    if (!voiceOnly) {
        const transport = session?.connected === false ? t("security.compact.disconnected")
            : session?.connected && /^TLS\b/i.test(session.security || "") ? t("security.compact.encrypted")
                : session?.connected && /^(?:plaintext|unencrypted)\b/i.test(session.security || "") ? t("security.compact.unencrypted")
                    : t("security.compact.unknown");
        rows.push(`${t("security.compact.server")}: ${transport}`);
        const messages = diagnostics ? diagnostics.active ? "E2EE" : t("security.compact.blocked") : t("security.compact.unknown");
        rows.push(`${t("security.compact.users")}: ${messages} (${t("security.compact.messages")})`);
    }
    const status = compactMediaEncryptionStatus(media) || t("security.compact.off");
    rows.push(voiceOnly ? `${t("security.compact.media")}\n${t("security.compact.users")}: ${status}`
        : `${t("security.compact.media")}: ${status}`);
    if (capability?.reason) rows.push(t(["server_outdated", "keyless_guest", "no_device", "secure_unavailable"].includes(capability.reason)
        ? `security.compact.${capability.reason}` : "security.compact.unavailable"));
    if (!voiceOnly) rows.push(t("security.compact.exceptions"));
    return `${rows.join("\n")}\n\n${t("security.compact.footer")}`;
}
