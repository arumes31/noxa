import { t } from "./i18n.js";
import { mediaStatusText } from "./channel-media.js";

// Describe observed state separately from protocol guarantees. A lock must
// never imply that server-readable channel chat is end-to-end encrypted.
export function encryptionDetails({ session, diagnostics, media = { state: "off" }, capability, voiceOnly = false } = {}) {
    const parts = [];
    if (!voiceOnly) {
        parts.push(t("security.details.transport"), session?.connected
            ? (session.security || t("security.details.unknown")) : t("security.details.disconnected"));
        parts.push(t("security.details.messages"), diagnostics
            ? (diagnostics.active ? `${t("security.details.active")}\n${diagnostics.protocol || ""}\n${diagnostics.cipher || ""}` : `${t("security.details.inactive")}\n${diagnostics.unavailable || ""}`)
            : t("security.details.unknown"));
    }
    parts.push(t("security.details.media"), mediaStatusText(media) || t("security.details.mediaOff"));
    if (capability?.reason) {
        const key = `e2ee.reason.${capability.reason}`;
        // Only known adapter reasons are localized; never manufacture success.
        parts.push(["server_outdated", "keyless_guest", "no_device", "secure_unavailable"].includes(capability.reason) ? t(key) : t("e2ee.reason.unavailable"));
    }
    parts.push(t("security.details.mediaProtocol"), t("e2ee.media.limits"));
    if (!voiceOnly) parts.push(t("chat.encryptionHelp"));
    return parts.filter(Boolean).join("\n\n");
}
