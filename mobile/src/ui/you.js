// you.js — You (14/15): profile, notifications, passkeys and devices.
import { app, forgetDevice } from "../app.js";
import { registerPasskey, passkeysSupported } from "../core/device.js";
import { Msg } from "../core/frame.js";
import { disablePush, enablePush, needsInstallForPush, pushSupported, setPushPreferences } from "../core/push.js";
import { t } from "../i18n.js";
import { avatar, h, sheet, toast } from "./dom.js";
import { icon } from "./icons.js";
import { header, nav } from "./common.js";
import { adminEntry } from "./admin-entry.js";

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

export function renderYou(root) {
    const session = app.session;
    const list = h("ul", { class: "list" });
    const passkeys = h("ul", { class: "list" });
    const notifications = notificationSettings(session);
    const guest = !!app.meta.guest;
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
            h("div", { class: "card row" }, avatar(session.nickname, "md"),
                h("span", { class: "row-main" }, h("span", { class: "row-title" }, session.nickname), h("span", { class: "row-sub" }, app.info?.server_name || ""))),
            guest ? null : adminEntry(session),
            guest ? h("p", { class: "hint" }, t("connect.guestHint")) : h("p", { class: "section-label" }, t("settings.notifications")),
            guest ? null : notifications.box,
            guest ? null : h("p", { class: "section-label" }, t("settings.passkeys")),
            passkeys,
            !guest && passkeysSupported() ? h("button", { class: "btn btn-primary btn-block", onclick: async () => { await savePasskey(); refresh(); } }, icon("passkey"), " ", t("settings.savePasskey")) : null,
            guest ? null : h("p", { class: "section-label" }, t("settings.devices")),
            list,
            guest ? null : h("button", { class: "btn btn-block", type: "button", onclick: backupFlow }, t("acct.backup")),
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
}
