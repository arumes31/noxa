// shutdown-notice.js — the toast text for server shutdown events.
import { t } from "./i18n.js";

/** The text for a scheduled (server_shutdown_scheduled) or immediate shutdown. */
export function shutdownNotice(type, data) {
    if (type !== "server_shutdown_scheduled") return t("server.shutdown");
    const seconds = Math.max(1, Math.round(Number(data?.seconds) || 0));
    // Minutes only when exact, so the warning never overstates the time left.
    return seconds >= 60 && seconds % 60 === 0
        ? t("server.shutdownMinutes", { minutes: seconds / 60 })
        : t("server.shutdownSeconds", { seconds });
}
