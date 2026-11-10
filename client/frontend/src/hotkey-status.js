// hotkey-status.js — last registration error per hotkey action (301). The
// listener is installed at startup (settings-loader.js) so conflicts reported
// before the Settings dialog is first opened are not lost; the dialog reads
// the map when it renders its hotkey rows.
export const hotkeyErrors = new Map();

/** Records a hotkey_status event and refreshes any visible hotkey row. */
export function recordHotkeyStatus(status, doc = globalThis.document) {
    if (!status?.action) return;
    if (status.error) hotkeyErrors.set(status.action, status.error);
    else hotkeyErrors.delete(status.action);
    for (const row of doc?.querySelectorAll?.(".hk-map-row") || []) {
        if (row.dataset.hotkeyAction !== status.action) continue;
        const err = row.querySelector(".hk-err");
        if (err) err.textContent = status.error || "";
    }
}
