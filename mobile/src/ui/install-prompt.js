import { app } from "../app.js";
import { t } from "../i18n.js";
import { h, sheet, toast } from "./dom.js";

let pendingInstall = null;
let installed = false;
let dismissed = false;
let host = null;
const storageKey = "noxa-install-dismissed-v1";
try { dismissed = sessionStorage.getItem(storageKey) === "true"; } catch { /* optional browser storage */ }

export function standalone() {
    return installed || navigator.standalone === true || window.matchMedia("(display-mode: standalone)").matches
        || window.matchMedia("(display-mode: fullscreen)").matches;
}
function iphone() { return /iPhone|iPad|iPod/.test(navigator.userAgent) || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1); }
function dismiss() {
    dismissed = true;
    try { sessionStorage.setItem(storageKey, "true"); } catch { /* optional browser storage */ }
    renderInstallPrompt(host);
}
export function initInstallPrompt() {
    window.addEventListener("beforeinstallprompt", event => {
        if (!iphone() && !/Android/.test(navigator.userAgent)) return;
        event.preventDefault();
        pendingInstall = event;
        renderInstallPrompt(host);
    });
    window.addEventListener("appinstalled", () => { installed = true; pendingInstall = null; renderInstallPrompt(host); });
    window.matchMedia("(display-mode: standalone)").addEventListener("change", () => renderInstallPrompt(host));
}
export function renderInstallPrompt(root) {
    if (!root) return;
    host = root;
    root.querySelector("#install-banner")?.remove();
    if (!app.session || dismissed || standalone() || (!iphone() && !/Android/.test(navigator.userAgent))) return;
    const action = h("button", { class: "btn", type: "button", onclick: async () => {
        action.disabled = true;
        try {
            if (pendingInstall) {
                const prompt = pendingInstall;
                pendingInstall = null;
                await prompt.prompt();
                const choice = await prompt.userChoice;
                if (choice?.outcome === "accepted") installed = true;
                dismiss();
            } else {
                await sheet(t("install.title"), t(iphone() ? "install.ios" : "install.android"), [
                    { label: t("install.done"), value: "done", primary: true },
                ]);
            }
        } catch (error) { toast(t("common.error", { error: error.message }), "warn"); }
        finally { action.disabled = false; }
    } }, t(pendingInstall ? "install.action" : "install.how"));
    root.prepend(h("aside", { id: "install-banner", "aria-label": t("install.title") },
        h("div", {}, h("strong", {}, t("install.title")), h("span", {}, t("install.hint"))),
        action,
        h("button", { class: "btn", type: "button", onclick: dismiss }, t("settings.notNow"))));
}
