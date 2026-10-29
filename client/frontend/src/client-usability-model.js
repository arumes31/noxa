import { t } from "./i18n.js";
export function sidebarWidth(value, viewport) {
    return Math.round(Math.max(220, Math.min(420, viewport - 400, Number(value) || 290)));
}
// Encrypted voice refusals of a join or move (server netproto.MediaE2EEReason*
// codes) keep their own localized explanation.
const MEDIA_JOIN_REASONS = { media_e2ee_channel_full: "channel_full", media_e2ee_invisible: "invisible", media_e2ee_client_outdated: "client_outdated",
    media_e2ee_keyless_guest: "keyless_guest", media_e2ee_unavailable: "unavailable" };
export function mediaJoinReason(error) {
    const code = String(error?.message || error || "").match(/\b(media_e2ee_[a-z_]+)\b/)?.[1];
    return code && MEDIA_JOIN_REASONS[code] ? t("e2ee.reason." + MEDIA_JOIN_REASONS[code]) : "";
}
export function moveFailure(error) {
    return t("runtime.moveFailed", { error: mediaJoinReason(error) || String(error?.message || error || "") });
}
export function joinFailure(error) {
    const media = mediaJoinReason(error);
    if (media) return media;
    const raw = String(error?.message || error || "");
    const text = raw.toLowerCase();
    const key = /password/.test(text) ? "password" : /channel (?:is )?full|capacity/.test(text) ? "full"
        : /rules/.test(text) ? "rules" : /permission|denied|not authorized|access/.test(text) ? "denied"
            : /not found|does not exist/.test(text) ? "missing" : /not connected|disconnected|connection closed/.test(text) ? "offline" : "other";
    return t("usability.join." + key, { error: raw });
}
export function unreadLabel(count, mentions = 0, replies = 0) {
    return [t("usability.unread", { count }), mentions && t("usability.mentions", { count: mentions }), replies && t("usability.replies", { count: replies })].filter(Boolean).join(" · ");
}
