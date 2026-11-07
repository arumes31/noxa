// studio-launcher.js — the Studio (studio-ui.js with its compositor, capture
// registry, assets and audio graph) is a separate chunk loaded when the
// Studio button is first pressed (83). The startup work stays here: the
// voice-bar button and the sweep that finishes an interrupted identity wipe.
import { t } from "./i18n.js";
import { labelButton } from "./icons.js";
import { lazyModule } from "./lazy-module.js";
import { sweepOrphanStudio, studioStoredIdentities } from "./studio-storage.js";

export const studioModule = lazyModule(() => import("./studio-ui.js"));

/** Opens the Studio, loading it first if needed. */
export function openStudioLazy({ toast = window.__noxa?.toast, button = document.getElementById("voice-studio") } = {}) {
    const loaded = studioModule.current();
    // Opens synchronously once loaded; callers always get a promise.
    if (loaded) return Promise.resolve(loaded.openStudio());
    button?.setAttribute("aria-busy", "true");
    return studioModule.load().then(module => module.openStudio(), error => {
        toast?.(t("studio.loadFailed", { error: String(error?.message || error) }), "warn");
        throw error;
    }).finally(() => button?.removeAttribute("aria-busy"));
}

/**
 * Ends an open Studio (server/channel switch, disconnect). Nothing to do
 * while the module has not been loaded: a Studio session needs it.
 */
export function disposeStudio() {
    studioModule.current()?.disposeStudio();
}

export function initStudio() {
    // Finish a Studio wipe that an identity deletion could not complete
    // (the webview closed after the native deletion committed). Identities
    // are listed only when some Studio data is stored at all.
    void studioStoredIdentities().then(owners => owners.length ? window.go?.main?.App?.ListIdentities?.() : null).then(entries => entries && sweepOrphanStudio(entries)).catch(() => {});
    const bar = document.querySelector("#voice-bar .voice-buttons");
    if (!bar || document.getElementById("voice-studio")) return;
    const button = document.createElement("button");
    button.type = "button";
    button.id = "voice-studio";
    button.className = "voice-control";
    button.onclick = () => { void openStudioLazy({ button }).catch(() => {}); };
    labelButton(button, "streamLive", t("studio.open"));
    button.setAttribute("aria-label", t("studio.openHelp"));
    button.title = t("studio.openHelp");
    (bar.querySelector(".voice-secondary-actions") || bar).append(button);
}
