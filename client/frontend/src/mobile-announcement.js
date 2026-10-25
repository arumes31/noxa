// Account-scoped discovery of the PWA. Never create a pairing link until Set up.
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { openDeviceLinkDialog } from "./device-link-ui.js";
import { DEVICE_LIST_CHANGED } from "./my-devices.js";

export function initMobileAnnouncement() {
    const banner = document.createElement("section");
    banner.id = "mobile-announcement";
    banner.hidden = true;
    banner.setAttribute("aria-label", t("mobileAnnouncement.title"));
    document.getElementById("chat-head").after(banner);
    const shortcut = document.createElement("button");
    shortcut.id = "mobile-setup-banner";
    shortcut.type = "button";
    shortcut.hidden = true;
    shortcut.setAttribute("aria-haspopup", "dialog");
    shortcut.innerHTML = `${icon("screen")}<span>noXa Mobile</span>${icon("link")}`;
    document.getElementById("sidebar").append(shortcut);
    let shortcutSession = null;
    shortcut.onclick = () => {
        const current = state();
        if (!shortcut.hidden && shortcutSession && !current.isGuest
            && shortcutSession.tab === current.activeTabID && shortcutSession.client === current.myClientID
            && shortcutSession.generation === current.serverGeneration && shortcutSession.session === current.sessionGeneration) {
            openDeviceLinkDialog({ quickGuide: true });
        }
    };
    const dismissed = new Set(); // banner "Not now": this session only
    // (453/454) the persistent hint mode lives in settings.mobile_hint:
    // "" = until a phone is linked, "dismissed" = hidden, "shown" = always.
    const hintMode = () => String(state().settings?.mobile_hint || "");
    function setHintMode(mode) {
        const settings = state().settings;
        if (!settings) return;
        settings.mobile_hint = mode;
        void Promise.resolve(window.go.main.App.SaveSettings(settings)).catch(() => {});
    }
    let request = 0;
    let shownKey = "";
    const state = () => window.__noxa.state;
    function clear() { request++; shownKey = ""; banner.hidden = true; shortcut.hidden = true; shortcutSession = null; }
    function render(key) {
        const glyph = document.createElement("span");
        glyph.className = "mobile-announcement-icon";
        glyph.innerHTML = icon("screen");
        const copy = document.createElement("div");
        copy.className = "mobile-announcement-copy";
        const title = document.createElement("strong");
        title.textContent = t("mobileAnnouncement.title");
        const subtitle = document.createElement("span");
        subtitle.textContent = t("mobileAnnouncement.subtitle");
        copy.append(title, subtitle);
        const later = document.createElement("button");
        later.type = "button";
        later.textContent = t("mobileAnnouncement.later");
        const dismiss = () => { dismissed.add(key); shownKey = ""; banner.hidden = true; };
        later.onclick = dismiss;
        // The close button hides hint and sidebar shortcut for good (453).
        const dismissForever = () => { dismiss(); setHintMode("dismissed"); void refresh(); };
        const setup = document.createElement("button");
        setup.type = "button";
        setup.className = "mobile-announcement-setup";
        setup.textContent = t("mobileAnnouncement.setup");
        setup.setAttribute("aria-haspopup", "dialog");
        const tab = state().activeTabID, client = state().myClientID;
        setup.onclick = () => { if (tab === state().activeTabID && client === state().myClientID && !banner.hidden) openDeviceLinkDialog({ quickGuide: true }); };
        const close = document.createElement("button");
        close.type = "button";
        close.className = "icon-btn";
        close.innerHTML = icon("close");
        close.setAttribute("aria-label", t("common.close"));
        close.onclick = dismissForever;
        banner.setAttribute("aria-label", t("mobileAnnouncement.title"));
        banner.replaceChildren(glyph, copy, later, setup, close);
        shownKey = key;
        banner.hidden = false;
    }
    async function refresh() {
        const serial = ++request;
        shortcut.hidden = true;
        shortcutSession = null;
        const { activeTabID: tab, serverGeneration: generation, sessionGeneration: session, myClientID: client } = state();
        if (!tab || !client || state().isGuest) { banner.hidden = true; return; }
        const current = () => serial === request && tab === state().activeTabID
            && generation === state().serverGeneration && session === state().sessionGeneration && client === state().myClientID;
        try {
            const info = await window.go.main.App.SessionInfoForTab(tab);
            if (!current()) return;
            if (!info?.connected || info.is_guest || !info.mobile_available) { banner.hidden = true; return; }
            const key = info.mobile_account_key;
            const devices = await window.go.main.App.DevicesForTab(tab);
            if (!current()) return;
            // Missing/failed device metadata must never become an advertisement.
            const linked = !Array.isArray(devices) || devices.some(device => device.kind === "web" && !device.revoked_at);
            const mode = hintMode();
            // (453) the sidebar hint hides once a phone is linked or the user
            // dismissed it; Help can bring it back ("shown") even then (454).
            const hideShortcut = mode === "dismissed" || (mode !== "shown" && linked);
            if (!hideShortcut) {
                shortcutSession = { tab, client, generation, session };
                shortcut.hidden = false;
                shortcut.title = t("mobileAnnouncement.setup");
            }
            if (!key || dismissed.has(key) || linked || mode === "dismissed") { banner.hidden = true; return; }
            render(key);
        } catch { if (current()) banner.hidden = true; }
    }
    for (const event of ["tab_reset", "disconnected"]) window.runtime.EventsOn(event, clear);
    window.runtime.EventsOn("device_event", event => {
        if (["linked", "revoked"].includes(event?.event)) void refresh();
    });
    window.addEventListener("noxa-language-changed", () => { if (!banner.hidden) render(shownKey); });
    window.addEventListener(DEVICE_LIST_CHANGED, () => void refresh());
    window.__noxaMobile = {
        refresh, clear,
        hintHidden: () => shortcut.hidden || hintMode() === "dismissed",
        showHint: () => { setHintMode("shown"); dismissed.clear(); void refresh(); },
    };
}
