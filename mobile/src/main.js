// main.js — boots the mobile web app: server info, stored device, router.
import "./styles.css";
import { app, connect, loadDevice, loadInfo, markRead } from "./app.js";
import { parseLinkFragment } from "./core/device.js";
import { t, lang } from "./i18n.js";
import { h, toast } from "./ui/dom.js";

// Each screen is its own chunk, so Connect and Home load without the rest.
const screens = {
    connect: () => import("./ui/connect.js"),
    home: () => import("./ui/home.js"),
    servers: () => import("./ui/servers.js"),
    chat: () => import("./ui/chat.js"),
    voice: () => import("./ui/voice.js"),
    you: () => import("./ui/you.js"),
};

document.documentElement.lang = lang();
const root = document.getElementById("app");
let cleanup = null;

let routeToken = 0;

async function route() {
    cleanup?.();
    cleanup = null;
    const token = ++routeToken;
    if (!app.session) {
        app.viewing = null;
        const { renderConnect } = await screens.connect();
        if (token === routeToken) renderConnect(root, { onSignedIn });
        return;
    }
    let [, screen = "home", arg] = location.hash.split("/");
    if (!screens[screen] || screen === "connect") screen = "home";
    app.viewing = null;
    if (screen === "chat") {
        app.viewing = Number(arg) || 0;
        markRead(app.viewing);
    }
    let module;
    try {
        module = await screens[screen]();
    } catch (error) {
        // A deploy replaced the chunk; reload to the new version.
        toast(t("common.error", { error: error.message }), "warn");
        return;
    }
    if (token !== routeToken) return;
    switch (screen) {
        case "servers": module.renderServers(root); break;
        case "chat": cleanup = module.renderChat(root, app.viewing); break;
        case "voice": module.renderVoice(root); break;
        case "you": module.renderYou(root); break;
        default: module.renderHome(root);
    }
    renderBanner();
}

function onSignedIn({ offerPasskey: offer } = {}) {
    if (!location.hash || location.hash === "#/") location.hash = "#/home";
    else route();
    if (offer) screens.you().then(module => module.offerPasskey());
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
app.addEventListener("unread", () => {
    const screen = location.hash.split("/")[1] || "home";
    if (screen === "home" || screen === "servers") route();
});
app.addEventListener("online", renderBanner);
// A tapped notification opens its conversation in the running app.
navigator.serviceWorker?.addEventListener("message", event => {
    if (event.data?.type !== "open" || typeof event.data.url !== "string") return;
    const url = new URL(event.data.url, location.origin);
    if (url.origin === location.origin && url.pathname.startsWith("/app/")) location.hash = url.hash || "#/home";
});
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
        const { runLink } = await screens.connect();
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
