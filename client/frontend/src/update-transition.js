import { t } from "./i18n.js";

let blocked = false;
let initialized;
let dialog;

export const updateStartupBlocked = () => blocked;

export function refreshUpdateTransition() {
    if (dialog) {
        dialog.textContent = t("runtime.updateCompleting");
        dialog.setAttribute("aria-label", t("runtime.updateCompleting"));
    }
}

// The bridge query precedes settings and automatic activity. While unresolved
// or rejected it stays inert and cannot acknowledge startup as healthy.
// Only native true selects the temporary compatibility process.
export function initializeUpdateTransition() {
    if (initialized) return initialized;
    const query = window.go?.main?.App.UpdateStartupPending;
    if (typeof query !== "function") return (initialized = Promise.resolve(false));
    blocked = true;
    dialog = document.createElement("dialog");
    dialog.id = "update-transition";
    dialog.tabIndex = -1;
    dialog.setAttribute("aria-live", "polite");
    dialog.setAttribute("aria-busy", "true");
    dialog.setAttribute("aria-label", t("runtime.updateCompleting"));
    refreshUpdateTransition();
    document.body.appendChild(dialog);
    dialog.addEventListener("cancel", event => event.preventDefault());
    dialog.showModal();
    const events = ["click", "pointerdown", "mousedown", "keydown", "keyup", "submit", "input", "change"];
    const suppress = event => {
        if (!blocked) return;
        event.preventDefault();
        event.stopImmediatePropagation();
    };
    for (const name of events) window.addEventListener(name, suppress, true);
    initialized = Promise.resolve().then(() => query()).then(pending => {
        if (pending === true) return true;
        blocked = false;
        for (const name of events) window.removeEventListener(name, suppress, true);
        dialog.close();
        dialog.remove();
        dialog = null;
        return false;
    });
    return initialized;
}
