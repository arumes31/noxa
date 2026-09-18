import { t } from "./i18n.js";

export function formatTransferETA(seconds) {
    if (!Number.isFinite(seconds) || seconds < 0) return "";
    if (seconds < 60) return t("eta.soon");
    if (seconds < 3600) {
        const count = Math.ceil(seconds / 60);
        return t(count === 1 ? "eta.minute" : "eta.minutes", { count });
    }
    const count = Math.ceil(seconds / 3600);
    return t(count === 1 ? "eta.hour" : "eta.hours", { count });
}
