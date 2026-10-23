// main.js — boots the mobile web app: server info, stored device, router.
import "./styles.css";
import { app, connect, loadDevice, loadInfo } from "./app.js";
import { parseLinkFragment } from "./core/device.js";
import { t, lang } from "./i18n.js";
import { renderConnect, runLink } from "./ui/connect.js";
import { h, toast } from "./ui/dom.js";
import { offerPasskey, renderChat, renderHome, renderServers, renderVoice, renderYou } from "./ui/screens.js";

document.documentElement.lang = lang();
const root = document.getElementById("app");
let cleanup = null;

function route() {
    cleanup?.();
    cleanup = null;
    if (!app.session) {
        renderConnect(root, { onSignedIn });
        return;
    }
    const [, screen = "home", arg] = location.hash.split("/");
    switch (screen) {
        case "servers": renderServers(root); break;
        case "chat": cleanup = renderChat(root, Number(arg) || 0); break;
        case "voice": renderVoice(root); break;
        case "you": renderYou(root); break;
        default: renderHome(root);
    }
    renderBanner();
}

function onSignedIn({ offerPasskey: offer } = {}) {
    if (!location.hash || location.hash === "#/") location.hash = "#/home";
    else route();
    if (offer) offerPasskey();
}

function renderBanner() {
    document.querySelector(".banner")?.remove();
    if (app.session && !app.online) root.prepend(h("div", { class: "banner", role: "status" }, t("offline.banner")));
}

// Re-render screens that list live state when the session changes.
app.addEventListener("session", event => {
    const session = event.detail;
    for (const type of ["snapshot", "subscriptions"]) {
        session.addEventListener(type, () => {
            const screen = location.hash.split("/")[1] || "home";
            if (screen === "home" || screen === "servers") route();
        });
    }
    session.addEventListener("messages", () => {
        if ((location.hash.split("/")[1] || "home") === "home") route();
    });
});
app.addEventListener("online", renderBanner);
app.addEventListener("signedout", () => {
    toast(t("signin.revoked"), "warn");
    route();
});
window.addEventListener("hashchange", route);

async function registerServiceWorker() {
    if (!("serviceWorker" in navigator) || location.protocol !== "https:") return;
    try {
        const registration = await navigator.serviceWorker.register("/app/sw.js", { scope: "/app/" });
        registration.addEventListener("updatefound", () => {
            const worker = registration.installing;
            worker?.addEventListener("statechange", () => {
                if (worker.state === "installed" && navigator.serviceWorker.controller) {
                    toast(t("update.ready"));
                }
            });
        });
    } catch {
        // The app works without offline support.
    }
}

async function boot() {
    try {
        await loadInfo();
    } catch (error) {
        root.replaceChildren(h("main", { class: "connect" }, h("p", { class: "error" }, t("common.error", { error: error.message }))));
        return;
    }
    registerServiceWorker();
    const link = location.pathname === "/link" ? parseLinkFragment(location.hash) : null;
    if (link) {
        // The secret must not linger in history or be re-claimed on reload.
        history.replaceState(null, "", "/app/");
        await runLink(root, link, { onSignedIn });
        return;
    }
    await loadDevice();
    if (app.identity && app.meta.linked) {
        try {
            await connect();
        } catch (error) {
            if (!error.revoked) toast(t("signin.failed", { error: error.message }), "warn");
        }
    }
    route();
}

boot();
