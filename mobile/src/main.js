import { installViewport } from "./ui/viewport.js";
import { initInstallPrompt, renderInstallPrompt } from "./ui/install-prompt.js";
// main.js — boots the mobile web app: server info, stored device, router.
import "./styles.css";
import { app, connect, flushNow, loadDevice, loadInfo, markRead } from "./app.js";
import { parseLinkFragment } from "./core/device.js";
import { t, lang } from "./i18n.js";
import { h, toast } from "./ui/dom.js";
import { afterRoute, onWideChange, screenRoot } from "./ui/layout.js";

// Each screen is its own chunk, so Connect and Home load without the rest.
const screens = {
    connect: () => import("./ui/connect.js"),
    home: () => import("./ui/home.js"),
    "new-chat": () => import("./ui/new-chat.js"),
    "new-group": () => import("./ui/new-group.js"),
    servers: () => import("./ui/servers.js"),
    chat: () => import("./ui/chat.js"),
    dm: () => import("./ui/dm.js"),
    call: () => import("./ui/call.js"),
    group: () => import("./ui/group.js"),
    voice: () => import("./ui/voice.js"),
    you: () => import("./ui/you.js"),
    admin: () => import("./ui/admin.js"),
    invite: () => import("./ui/invite.js"),
};

document.documentElement.lang = lang();
const appRoot = document.getElementById("app");
// Screens render here: #app on phones, the wide shell's centre on tablets.
let root = appRoot;
installViewport();
initInstallPrompt();
let cleanup = null;

let routeToken = 0;

async function route() {
    cleanup?.();
    cleanup = null;
    const token = ++routeToken;
    root = screenRoot(appRoot, !!app.session);
    if (!app.session) {
        app.viewing = null;
        const { renderConnect } = await screens.connect();
        if (token === routeToken) cleanup = renderConnect(root, { onSignedIn });
        return;
    }
    let [, screen = "home", ...rest] = location.hash.split("/");
    let arg = "";
    try { arg = decodeURIComponent(rest.join("/")); } catch { arg = ""; }
    if (!screens[screen] || screen === "connect") screen = "home";
    if ((screen === "dm" || screen === "call" || screen === "group") && !arg) screen = "home";
    app.viewing = null;
    if (screen === "chat") app.viewing = `channel:${Number(arg) || 0}`;
    if (screen === "dm") app.viewing = `dm:${arg}`;
    if (screen === "group") app.viewing = `conversation:${arg}`;
    if (app.viewing) markRead(app.viewing);
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
        case "new-group": cleanup = module.renderNewGroup(root); break;
        case "new-chat": cleanup = module.renderNewChat(root); break;
        case "servers": module.renderServers(root); break;
        case "chat": cleanup = module.renderChat(root, Number(arg) || 0); break;
        case "dm": cleanup = module.renderDM(root, arg); break;
        case "call": cleanup = module.renderCall(root, arg); break;
        case "group": cleanup = module.renderGroup(root, arg); break;
        case "voice": cleanup = module.renderVoice(root); break;
        case "you": module.renderYou(root); break;
        case "admin": cleanup = module.renderAdmin(root, arg) || null; break;
        case "invite": module.renderInvite(root, arg); break;
        default: module.renderHome(root);
    }
    afterRoute(screen, arg);
    renderBanner();
}

function onSignedIn({ offerPasskey: offer } = {}) {
    if (!location.hash || location.hash === "#/") location.hash = "#/home";
    else route();
    if (offer) screens.you().then(module => module.offerPasskey());
}

function renderBanner() {
    renderInstallPrompt(root);
    document.querySelector(".banner")?.remove();
    const waiting = app.outboxWaiting ? ` · ${t("outbox.count", { count: app.outboxWaiting })}` : "";
    if (app.session && !app.online) root.prepend(h("div", { class: "banner", role: "status" }, t("offline.banner") + waiting));
}

// Re-render screens that list live state when the session changes.
let sessions = 0;
app.addEventListener("session", event => {
    const session = event.detail;
    // After a reconnect the screen must use the new session.
    if (sessions++ > 0) route();
    for (const type of ["snapshot", "subscriptions"]) {
        session.addEventListener(type, () => {
            const screen = location.hash.split("/")[1] || "home";
            if (screen === "home" || screen === "servers" || (screen === "voice" && !app.voice?.active)) route();
        });
    }
    session.addEventListener("messages", () => {
        if ((location.hash.split("/")[1] || "home") === "home") route();
    });
});
app.addEventListener("reminders", () => {
    if ((location.hash.split("/")[1] || "home") === "home") route();
});
app.addEventListener("groups", () => {
    if ((location.hash.split("/")[1] || "home") === "home") route();
});
app.addEventListener("dm", () => {
    if ((location.hash.split("/")[1] || "home") === "home") route();
});
app.addEventListener("unread", () => {
    const screen = location.hash.split("/")[1] || "home";
    if (screen === "home" || screen === "servers") route();
});
app.addEventListener("online", renderBanner);
// The call bar follows voice state on the other screens.
// The call bar also follows private calls.
app.addEventListener("call", () => {
    const screen = location.hash.split("/")[1] || "home";
    if (screen === "call") return;
    const bottom = document.querySelector(".nav")?.closest(".bottom") || document.querySelector(".nav");
    if (bottom) import("./ui/common.js").then(({ nav }) => bottom.replaceWith(nav(screen)));
});
let voiceWasActive = false;
app.addEventListener("voice", () => {
    const screen = location.hash.split("/")[1] || "home";
    const active = !!app.voice?.active;
    const switched = active !== voiceWasActive;
    voiceWasActive = active;
    if (screen === "voice") {
        // Joining or leaving swaps the channel list and the room.
        if (switched) route();
        return;
    }
    const bar = document.querySelector(".call-bar");
    if (bar && active && !switched) {
        // Speaking and mute change often; update the bar in place.
        import("./ui/common.js").then(({ updateCallBar }) => updateCallBar(bar));
        return;
    }
    const bottom = document.querySelector(".nav")?.closest(".bottom") || document.querySelector(".nav");
    if (!bottom) return;
    import("./ui/common.js").then(({ nav }) => bottom.replaceWith(nav(screen)));
});
app.addEventListener("outbox", renderBanner);
// A tapped notification opens its conversation in the running app.
navigator.serviceWorker?.addEventListener("message", event => {
    if (event.data?.type === "flush-outbox") {
        flushNow().catch(() => {});
        return;
    }
    if (event.data?.type !== "open" || typeof event.data.url !== "string") return;
    const url = new URL(event.data.url, location.origin);
    if (url.origin === location.origin && url.pathname.startsWith("/app/")) location.hash = url.hash || "#/home";
});
app.addEventListener("signedout", () => {
    toast(t("signin.revoked"), "warn");
    route();
});
window.addEventListener("hashchange", route);
onWideChange(() => route());

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

// The control protocol this build speaks (internal/web.ProtocolVersion).
const PROTOCOL_VERSION = 1;

/**
 * A cached older app met a newer server. Reloading fetches the matching
 * bundle; the button waits for the user so a call is never cut off.
 */
function showUpdateRequired() {
    const bar = h("div", { class: "banner banner-update", role: "alert" }, t("update.required"), " ",
        h("button", { class: "btn", onclick: async () => {
            const registration = await navigator.serviceWorker?.getRegistration("/app/");
            registration?.waiting?.postMessage("activate-update");
            await registration?.update().catch(() => {});
            location.reload();
        } }, t("update.reload")));
    document.body.prepend(bar);
}

async function boot() {
    try {
        await loadInfo();
    } catch (error) {
        root.replaceChildren(h("main", { class: "connect" }, h("p", { class: "error" }, t("common.error", { error: error.message }))));
        return;
    }
    registerServiceWorker();
    if (app.info.protocol_version !== PROTOCOL_VERSION) showUpdateRequired();
    const link = location.pathname === "/link" ? parseLinkFragment(location.hash) : null;
    if (link) {
        // The secret must not linger in history or be re-claimed on reload.
        history.replaceState(null, "", "/app/");
        const { runLink } = await screens.connect();
        await runLink(root, link, { onSignedIn });
        return;
    }
    await loadDevice();
    if (app.identity && (app.meta.linked || (app.meta.guest && app.info.guest_login && !app.info.server_password_required))) {
        try {
            await connect();
        } catch (error) {
            if (!error.revoked) toast(t("signin.failed", { error: error.message }), "warn");
        }
    }
    route();
}

boot();
