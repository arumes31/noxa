import { t } from "./i18n.js";
export function sidebarWidth(value, viewport) {
    return Math.round(Math.max(220, Math.min(420, viewport - 400, Number(value) || 290)));
}
export function joinFailure(error) {
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
