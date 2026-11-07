// settings-loader.js — the Settings dialog (settings-ui.js, ~100 KB of
// source) is a separate chunk loaded on first use and preloaded when the app
// is idle (83). Everything other modules expect at startup is installed here:
// window.__noxa.openSettings, settingsPages(), the "noxa-open-settings" deep
// link and the hotkey_status listener.
import { lazyModule, preloadWhenIdle } from "./lazy-module.js";
import { recordHotkeyStatus } from "./hotkey-status.js";
import { t } from "./i18n.js";

export const settingsModule = lazyModule(() => import("./settings-ui.js"));

/**
 * Opens Settings. Synchronous once the dialog module has loaded; before that
 * it marks the page busy, loads the module and then opens the dialog.
 */
export function openSettingsLazy(target = "general", options = {}, { toast = window.__noxa?.toast, doc = document } = {}) {
    const loaded = settingsModule.current();
    if (loaded) return loaded.openSettings(target, options);
    doc.body?.setAttribute("aria-busy", "true");
    doc.body?.classList.add("module-loading");
    return settingsModule.load().then(module => module.openSettings(target, options), error => {
        toast?.(t("settings.loadFailed", { error: String(error?.message || error) }), "warn");
        throw error;
    }).finally(() => {
        doc.body?.removeAttribute("aria-busy");
        doc.body?.classList.remove("module-loading");
    });
}

export function installSettingsLauncher({ runtime = window.runtime, preload = true } = {}) {
    const noxa = window.__noxa;
    noxa.openSettings = (target, options) => openSettingsLazy(target, options);
    // Launchers such as the quick switcher fall back to their own list until
    // the dialog module is available.
    noxa.settingsPages = () => settingsModule.current()?.settingsPages() ?? null;
    window.addEventListener("noxa-open-settings", event => {
        void openSettingsLazy(event.detail || "general").catch(() => {});
    });
    runtime?.EventsOn?.("hotkey_status", status => recordHotkeyStatus(status));
    if (preload) preloadWhenIdle(settingsModule);
}
