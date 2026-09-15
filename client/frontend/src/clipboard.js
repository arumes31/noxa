import { t } from "./i18n.js";

// Keep copied values and raw errors out of feedback: callers may copy keys.
export async function copyToClipboard(value, { success = t("clipboard.copied"), failure = t("clipboard.failed"), isCurrent = () => true } = {}) {
    try {
        if (typeof window.runtime?.ClipboardSetText === "function") {
            if (await window.runtime.ClipboardSetText(value) === false) throw new Error("Clipboard unavailable");
        } else {
            await navigator.clipboard.writeText(value);
        }
    } catch {
        if (isCurrent()) window.__voicx.toast(failure, "warn");
        return false;
    }
    if (isCurrent()) window.__voicx.toast(success);
    return true;
}
