// shutdown-notice.js — the toast text for server shutdown events.
import { t } from "./i18n.js";

/** The text for a scheduled (server_shutdown_scheduled) or immediate shutdown. */
export function shutdownNotice(type, data) {
    if (type !== "server_shutdown_scheduled") return t("server.shutdown");
    const seconds = Math.max(1, Math.round(Number(data?.seconds) || 0));
    return seconds >= 90
        ? t("server.shutdownMinutes", { minutes: Math.round(seconds / 60) })
        : t("server.shutdownSeconds", { seconds });
}
