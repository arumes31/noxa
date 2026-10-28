// you.js — You (14/15): profile, the searchable Settings sections (see
// settings-registry.js), passkeys and devices.
import { app, forgetDevice, recoverSecureMessaging } from "../app.js";
import { registerPasskey, passkeysSupported } from "../core/device.js";
import { Msg } from "../core/frame.js";
import { disablePush, enablePush, needsInstallForPush, pushSupported, setPushPreferences } from "../core/push.js";
import { t } from "../i18n.js";
import { h, sheet, toast } from "./dom.js";
import { icon } from "./icons.js";
import { header, nav } from "./common.js";
import { adminEntry } from "./admin-entry.js";
import { contactsSection } from "./contacts.js";
import { notificationBehaviour } from "./notify-settings.js";
import { profileCard } from "./you-social.js";
import { voiceSettings } from "./voice-settings.js";
import { filterSettings, registerSettings } from "./settings-registry.js";
import "./settings-sections.js";
import { dmPrivacySettings } from "./dm-settings.js";
import "./chat-settings.js";

import { backupFlow, renderDeviceList, serversSection } from "./devices.js";

export async function offerPasskey() {
    if (!passkeysSupported() || !app.session) return;
    const choice = await sheet(t("settings.passkeyPrompt"), t("settings.passkeyPromptBody"), [
        { label: t("settings.savePasskey"), value: "save", primary: true },
        { label: t("settings.notNow"), value: null },
    ]);
    if (choice === "save") await savePasskey();
}

async function savePasskey() {
    try {
        const session = app.session;
        await registerPasskey(session.conn, navigator.platform || "Passkey", navigator.credentials, session);
        toast(t("settings.passkeySaved"));
    } catch (error) {
        if (error?.name !== "NotAllowedError") toast(t("common.error", { error: error.message }), "warn");
    }
}

/** Settings, then Notifications: permission, Hide details, Always notify. */
function notificationSettings(session) {
    const box = h("div", { class: "card" });
    const toggle = (key, label, hint) => {
        const input = h("input", { type: "checkbox", role: "switch", checked: !!app.push.preferences?.[key] });
        input.onchange = async () => {
            input.disabled = true;
            try {
                app.push.preferences = await setPushPreferences(session.conn, { ...app.push.preferences, [key]: input.checked });
            } catch (error) {
                input.checked = !input.checked;
                toast(t("common.error", { error: error.message }), "warn");
            }
            input.disabled = false;
        };
        return h("label", { class: "toggle" }, h("span", { class: "row-main" }, h("span", { class: "row-title" }, label), h("span", { class: "row-sub" }, hint)), input);
    };
    const render = () => {
        const state = app.push;
        if (!pushSupported()) {
            box.replaceChildren(h("p", { class: "hint" }, needsInstallForPush() ? t("connect.install") : t("settings.notifyUnsupported")));
        } else if (!state.available) {
            box.replaceChildren(h("p", { class: "hint" }, t("settings.notifyUnavailable")));
        } else if (Notification.permission === "denied") {
            box.replaceChildren(h("p", { class: "hint" }, t("settings.notifyBlocked")));
        } else if (!state.preferences?.subscribed) {
            box.replaceChildren(
                h("p", { class: "hint", style: "margin-bottom:12px" }, t("settings.notifyHint")),
                h("button", { class: "btn btn-primary btn-block", onclick: async event => {
                    event.currentTarget.disabled = true;
                    try {
                        app.push.preferences = await enablePush(session.conn, state);
                    } catch (error) {
                        if (error?.name !== "NotAllowedError") toast(t("common.error", { error: error.message }), "warn");
                    }
                    render();
                } }, t("settings.notifyEnable")));
        } else {
            box.replaceChildren(
                toggle("hide_details", t("settings.hideDetails"), t("settings.hideDetailsHint")),
                toggle("always_notify", t("settings.alwaysNotify"), t("settings.alwaysNotifyHint")),
                h("button", { class: "btn btn-block", style: "margin-top:8px", onclick: async () => {
                    try { app.push.preferences = await disablePush(session.conn); } catch (error) { toast(t("common.error", { error: error.message }), "warn"); }
                    render();
                } }, t("settings.notifyDisable")));
        }
    };
    render();
    return { box, render };
}

// Push notifications are the first row of the Notifications section; the
// notification workstream adds its own rows next to it.
registerSettings("notifications", [{
    key: "push", label: t("settings.notifications"), keywords: "push alerts benachrichtigungen",
    render: () => app.meta.guest ? h("p", { class: "hint" }, t("connect.guestHint")) : notificationSettings(app.session).box,
}, {
    // Do not disturb, quiet hours, the per-event matrix and sounds (ui/notify-settings.js).
    key: "behaviour", label: t("notify.behaviour"), keywords: "dnd quiet hours sounds matrix ruhezeiten töne nicht stören",
    render: () => h("div", {}, h("p", { class: "section-label" }, t("notify.behaviour")), notificationBehaviour()),
}], { title: t("settings.section.notifications") });

// Read receipts and typing indicators (ui/dm-settings.js).
registerSettings("dm", [{
    key: "activity", label: t("privacy.activity"), keywords: "privacy read receipts typing lesebestätigung tippen datenschutz",
    render: () => app.meta.guest ? h("p", { class: "hint" }, t("connect.guestHint")) : dmPrivacySettings(),
}], { title: t("settings.section.dm") });

// Microphone, activation, volumes and the data saver (ui/voice-settings.js).
registerSettings("voice", [{
    key: "voice", label: t("vc.settingsTitle"), keywords: "microphone mikrofon push to talk echo noise volume data saver datensparmodus",
    render: () => voiceSettings(),
}], { title: t("settings.section.voice") });

// Sections the user opened stay open while the app runs.
const openSections = new Set(["appearance", "notifications"]);
let searchQuery = "";

/** The searchable, registered settings sections. */
function settingsArea() {
    const container = h("div", { class: "settings-list" });
    const search = h("input", { class: "input settings-search", type: "search", value: searchQuery, placeholder: t("settings.search"), "aria-label": t("settings.search"), autocomplete: "off" });
    const draw = () => {
        const sections = filterSettings(searchQuery);
        if (!sections.length) {
            container.replaceChildren(h("p", { class: "hint settings-empty", role: "status" }, t("settings.noMatch", { query: searchQuery.trim() })));
            return;
        }
        container.replaceChildren(...sections.map(section => {
            const details = h("details", { class: "card settings-section", "data-section": section.id, open: !!searchQuery.trim() || openSections.has(section.id) },
                h("summary", {}, section.title || t(`settings.section.${section.id}`)),
                h("div", { class: "settings-rows" }, section.rows.map(row => {
                    try { return row.render(); } catch (error) { return h("p", { class: "error" }, t("common.error", { error: error.message })); }
                })));
            details.addEventListener("toggle", () => {
                if (searchQuery.trim()) return;
                if (details.open) openSections.add(section.id); else openSections.delete(section.id);
            });
            return details;
        }));
    };
    search.oninput = () => { searchQuery = search.value; draw(); };
    draw();
    return h("div", { class: "settings-area" }, search, container);
}

export function renderYou(root) {
    const session = app.session;
    const list = h("ul", { class: "list" });
    const passkeys = h("ul", { class: "list" });
    const guest = !!app.meta.guest;
    const profile = profileCard(session);
    const refresh = async () => {
        try {
            // Rows offer rename, confirm and history copy (ui/devices.js).
            const result = await renderDeviceList(session, list, { refresh, revokeButton: device => device.current || device.first_device ? null : h("button", { class: "btn btn-danger", onclick: async () => {
                const ok = await sheet(t("settings.revoke"), device.name, [{ label: t("settings.revoke"), value: true, danger: true }, { label: t("link.cancel"), value: false }]);
                if (!ok) return;
                await session.conn.request(Msg.DeviceRequest, { action: "revoke", device_id: device.device_id }, Msg.DeviceResult).catch(e => toast(e.message, "warn"));
                refresh();
            } }, t("settings.revoke")) });
            passkeys.replaceChildren(...(result.passkeys || []).map(key => h("li", { class: "row" },
                icon("passkey"),
                h("span", { class: "row-main" }, h("span", { class: "row-title" }, key.name), h("span", { class: "row-sub" }, new Date(key.created_at * 1000).toLocaleDateString())),
                h("button", { class: "btn", onclick: async () => {
                    await session.conn.request(Msg.DeviceRequest, { action: "passkey_delete", passkey_id: key.passkey_id }, Msg.DeviceResult).catch(e => toast(e.message, "warn"));
                    refresh();
                } }, t("settings.removePasskey")))));
        } catch (error) {
            toast(t("common.error", { error: error.message }), "warn");
        }
    };
    root.replaceChildren(h("section", { class: "screen" },
        header(t("settings.title")),
        h("div", { class: "screen-body" },
            profile.card,
            guest ? null : adminEntry(session),
            h("p", { class: "section-label" }, t("contacts.title")),
            contactsSection(),
            settingsArea(),
            h("p", { class: "section-label" }, t("settings.section.account")),
            guest ? h("p", { class: "hint" }, t("connect.guestHint")) : null,
            guest ? null : h("p", { class: "section-label" }, t("settings.passkeys")),
            passkeys,
            !guest && passkeysSupported() ? h("button", { class: "btn btn-primary btn-block", onclick: async () => { await savePasskey(); refresh(); } }, icon("passkey"), " ", t("settings.savePasskey")) : null,
            guest ? null : h("p", { class: "section-label" }, t("settings.devices")),
            list,
            guest ? null : h("button", { class: "btn btn-block", type: "button", onclick: backupFlow }, t("acct.backup")),
            // Explicit secure-messaging recovery after a restore or a lost store.
            guest ? null : h("p", { class: "hint" }, t("acct.recoverHelp")),
            guest ? null : h("button", { class: "btn btn-block", type: "button", onclick: async () => {
                const ok = await sheet(t("acct.recover"), t("acct.recoverAsk"), [{ label: t("acct.recoverConfirm"), value: true }, { label: t("link.cancel"), value: false }]);
                if (!ok) return;
                try { await recoverSecureMessaging(); toast(t("acct.recovered")); }
                catch (error) { toast(t(`acct.recoverFail.${["not_needed", "invalid_evidence"].includes(error.code) ? error.code : "other"}`, { error: error.message }), "warn"); }
            } }, t("acct.recover")),
            serversSection(),
            h("div", { style: "height:16px" }),
            h("button", { class: "btn btn-danger btn-block", onclick: async () => {
                const ok = await sheet(t("settings.signOut"), t(guest ? "settings.guestSignOutConfirm" : "settings.signOutConfirm"), [{ label: t("settings.signOut"), value: true, danger: true }, { label: t("link.cancel"), value: false }]);
                if (!ok) return;
                try {
                    if (!guest) await session.conn.request(Msg.DeviceRequest, { action: "sign_out" }, Msg.DeviceResult, { accept: r => r.action === "sign_out" });
                    await forgetDevice();
                } catch (error) {
                    toast(t("common.error", { error: error.message }), "warn");
                }
            } }, t("settings.signOut")))),
        nav("you"));
    if (!guest) refresh();
    return profile.stop;
}
