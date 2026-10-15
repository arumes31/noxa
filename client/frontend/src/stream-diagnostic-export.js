import { t } from "./i18n.js";

let saving = false;
// Snapshot serialization happens at the explicit click, before the native dialog.
// Never resample, save automatically, or remove a saved recording on cancel/error.
export async function saveStreamDiagnostics(contents, { isCurrent = () => true } = {}) {
    if (saving) return "busy";
    saving = true;
    let result = "error";
    try {
        const value = await window.go?.main?.App?.SaveStreamDiagnostics(contents);
        if (["saved", "cancelled", "busy"].includes(value)) result = value;
    } catch { /* Native errors can contain paths; show only a fixed message. */ }
    finally { saving = false; }
    if (isCurrent()) window.__noxa.toast(t(`record.save.${result}`), result === "error" ? "warn" : undefined);
    return result;
}
