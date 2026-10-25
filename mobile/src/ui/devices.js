// devices.js — You → Devices and Servers, and the account cards on Home:
// rename devices, confirm a new device for direct messages, copy direct
// messages to it, back up this device's keys and the server bookmark list.
import { app } from "../app.js";
import { backupFileName, exportBackup } from "../core/backup.js";
import { copyHistory, endorseDevice, listDevices, needsConfirmation, renameDevice } from "../core/devices.js";
import { addServer, appURL, fetchServerInfo, listServers, normalizeServer, removeServer } from "../core/servers.js";
import { get as setting, set as setSettings } from "../core/settings.js";
import { t } from "../i18n.js";
import { deviceOffers, dismissOffer, formSheet } from "./account.js";
import { h, sheet, toast } from "./dom.js";

// --- Devices ---------------------------------------------------------------------

/** Renames a device after asking for the new name. Resolves true when renamed. */
export async function renameFlow(session, device) {
    const values = await formSheet({
        title: t("acct.renameNamed", { name: device.name }), submit: t("acct.rename"),
        fields: [{ name: "name", label: t("acct.deviceName"), value: device.name, maxlength: 64 }],
        validate: values => values.name.trim() ? "" : t("acct.deviceName"),
    });
    if (!values) return false;
    try {
        const renamed = await renameDevice(session.conn, device.device_id, values.name);
        toast(t("acct.renamed", { name: renamed?.name || values.name.trim() }));
        return true;
    } catch (error) {
        toast(t("common.error", { error: error.message }), "warn");
        return false;
    }
}

/** Copies direct messages to a device after asking for the range. */
export async function historyFlow(session, device) {
    const values = await formSheet({
        title: t("acct.copyTitle", { name: device.name }), body: t("acct.copyBody"), submit: t("acct.copyHistory"),
        fields: [{ name: "days", label: t("acct.copyHistory"), value: "90", options: [
            { value: "7", label: t("acct.range.7") }, { value: "30", label: t("acct.range.30") },
            { value: "90", label: t("acct.range.90") }, { value: "-1", label: t("acct.range.all") }] }],
    });
    if (!values || !app.dm) return false;
    try {
        const result = await copyHistory(session, app.dm, device.device_id, Number(values.days),
            progress => toast(t("acct.copying", progress)));
        toast(result.total ? t("acct.copied", { messages: result.messages, name: device.name }) : t("acct.copyNothing"));
        return true;
    } catch (error) {
        toast(t("acct.copyFailed", { error: error.message }), "warn");
        return false;
    }
}

/** Confirms a device for direct messages, then offers the history copy. */
export async function confirmFlow(session, device) {
    const ok = await sheet(t("acct.confirmTitle", { name: device.name }), t("acct.confirmBody", { fingerprint: device.fingerprint }), [
        { label: t("acct.confirm"), value: true, primary: true }, { label: t("acct.cancel"), value: false }]);
    if (!ok) return false;
    try {
        await endorseDevice(session, device);
    } catch (error) {
        toast(t("common.error", { error: error.message }), "warn");
        return false;
    }
    // The device directory now lists the confirmed device.
    app.dm?.directory.delete(session.uniqueID);
    toast(t("acct.confirmed", { name: device.name }));
    await historyFlow(session, device);
    return true;
}

/**
 * One row of You → Devices. self is this device's entry (its confirmed
 * flag decides whether it may confirm others and copy history).
 */
export function deviceRow(session, device, { self, refresh, revokeButton }) {
    const canVouch = !!self?.confirmed;
    const actions = [
        h("button", { class: "btn", type: "button", "aria-label": t("acct.renameNamed", { name: device.name }), onclick: async () => { if (await renameFlow(session, device)) refresh(); } }, t("acct.rename")),
    ];
    if (canVouch && needsConfirmation(device)) {
        actions.push(h("button", { class: "btn btn-primary", type: "button", "aria-label": t("acct.confirmNamed", { name: device.name }), onclick: async () => { if (await confirmFlow(session, device)) refresh(); } }, t("acct.confirm")));
    }
    if (canVouch && device.confirmed && !device.current && !device.revoked_at) {
        actions.push(h("button", { class: "btn", type: "button", "aria-label": t("acct.copyNamed", { name: device.name }), onclick: () => historyFlow(session, device) }, t("acct.copyHistory")));
    }
    if (revokeButton) actions.push(revokeButton);
    return h("li", { class: "acct-device" },
        h("div", { class: "row" },
            h("span", { class: "row-main" },
                h("span", { class: "row-title" }, device.name),
                h("span", { class: "row-sub" }, [t(`settings.kind.${device.kind}`), device.current ? t("settings.current") : null, device.first_device ? t("settings.firstDevice") : null].filter(Boolean).join(" · ")),
                h("span", { class: "row-sub mono" }, device.fingerprint),
                needsConfirmation(device) ? h("span", { class: "row-sub acct-warning" }, t("acct.unconfirmed")) : null,
                device.expiry_warned_at ? h("span", { class: "row-sub acct-warning" }, t("settings.expiry", { date: new Date((device.last_seen_at + 180 * 86400) * 1000).toLocaleDateString() })) : null)),
        h("div", { class: "acct-row-actions" }, actions));
}

/** Downloads a passphrase-protected backup of this device's keys. */
export async function backupFlow() {
    const values = await formSheet({
        title: t("acct.backup"), body: t("acct.backupBody"), submit: t("acct.backupAction"),
        fields: [
            { name: "passphrase", label: t("acct.passphrase"), type: "password", autocomplete: "new-password", hint: t("acct.passphraseShort") },
            { name: "repeat", label: t("acct.passphraseRepeat"), type: "password", autocomplete: "new-password" },
        ],
        validate: values => values.passphrase.length < 8 ? t("acct.passphraseShort") : values.passphrase !== values.repeat ? t("acct.passphraseMismatch") : "",
    });
    if (!values || !app.identity) return;
    toast(t("acct.working"));
    try {
        const text = await exportBackup(app.identity, { ...app.meta, deviceID: app.session?.deviceID || app.meta.deviceID }, values.passphrase, { serverName: app.info?.server_name || "" });
        const url = URL.createObjectURL(new Blob([text], { type: "application/json" }));
        const link = h("a", { href: url, download: backupFileName(app.info?.server_name || "noXa"), hidden: "" });
        document.body.append(link);
        link.click();
        link.remove();
        setTimeout(() => URL.revokeObjectURL(url), 60000);
        toast(t("acct.backupSaved"));
    } catch (error) {
        toast(t("common.error", { error: error.message }), "warn");
    }
}

/** The devices list with rename, confirm, history copy and the given revoke buttons. */
export async function renderDeviceList(session, list, { revokeButton, refresh }) {
    const { devices, passkeys } = await listDevices(session.conn);
    const self = devices.find(d => d.current);
    list.replaceChildren(...devices.map(device => deviceRow(session, device, { self, refresh, revokeButton: revokeButton(device) })));
    return { devices, passkeys };
}

// --- Servers -------------------------------------------------------------------------

/** Adds a server by address; resolves with the list or null. */
export async function addServerFlow() {
    const values = await formSheet({
        title: t("acct.addServer"), submit: t("acct.addServer"),
        fields: [{ name: "address", label: t("acct.serverAddress"), inputmode: "url", autocomplete: "url", placeholder: "chat.example" }],
        validate: values => normalizeServer(values.address) ? "" : t("acct.serverInvalid"),
    });
    if (!values) return null;
    const origin = normalizeServer(values.address);
    let name = "";
    try { name = (await fetchServerInfo(origin))?.server_name || ""; } catch { /* saved by address */ }
    const list = addServer(origin, name || listServers().find(s => s.origin === origin)?.name || "");
    toast(t("acct.serverAdded", { name: name || new URL(origin).host }));
    return list;
}

/** Opens another server's app (each server keeps its own app and identity). */
export function switchServer(origin) {
    if (origin === location.origin) location.assign(appURL(origin) + "#/home");
    else location.assign(appURL(origin));
}

function serverLabel(server) {
    return server.name || new URL(server.origin).host;
}

/** The saved servers, the current one marked; rows open the server. */
export function serverList(onChange = () => {}) {
    const list = h("ul", { class: "list acct-servers" });
    const render = () => {
        const servers = listServers();
        list.replaceChildren(...servers.map(server => {
            const current = server.origin === location.origin;
            return h("li", { class: "row" },
                h("button", { class: "row", type: "button", style: "padding:0;min-height:44px", "aria-current": current ? "true" : null, "aria-label": t("acct.switchTo", { name: serverLabel(server) }), onclick: () => switchServer(server.origin) },
                    h("span", { class: "row-main" },
                        h("span", { class: "row-title" }, serverLabel(server)),
                        h("span", { class: "row-sub mono" }, new URL(server.origin).host)),
                    current ? h("span", { class: "row-meta acct-server-current" }, t("acct.current")) : null),
                current ? null : h("button", { class: "icon-btn", type: "button", "aria-label": t("acct.removeServer", { name: serverLabel(server) }), onclick: () => { removeServer(server.origin); render(); onChange(); } }, "×"));
        }));
    };
    render();
    return { list, render };
}

/** You → Servers: the bookmark list and Add server. */
export function serversSection() {
    const { list, render } = serverList();
    return [
        h("p", { class: "section-label" }, t("acct.servers")),
        h("p", { class: "hint", style: "margin:0 4px 8px" }, t("acct.serversHint")),
        list,
        h("button", { class: "btn btn-block", type: "button", onclick: async () => { if (await addServerFlow()) render(); } }, t("acct.addServer")),
    ];
}

// --- Home cards --------------------------------------------------------------------

/** A short stable hash of a text, to remember which MOTD was dismissed. */
export function textHash(text) {
    let hash = 0x811c9dc5;
    for (let i = 0; i < text.length; i++) {
        hash ^= text.charCodeAt(i);
        hash = Math.imul(hash, 0x01000193) >>> 0;
    }
    return `${text.length}:${hash.toString(16)}`;
}

/** Server news (the MOTD) when it changed since it was last dismissed. */
function newsCard(session) {
    const motd = (session.motd || "").trim();
    if (!motd || setting("motdSeen") === textHash(motd)) return null;
    const card = h("section", { class: "card acct-news", "aria-labelledby": "news-title" },
        h("div", { class: "acct-news-head" },
            h("h2", { class: "row-title", id: "news-title", style: "margin:0;font-size:1rem" }, t("acct.news")),
            h("button", { class: "btn", type: "button", onclick: () => { setSettings({ motdSeen: textHash(motd) }); card.remove(); } }, t("acct.dismiss"))),
        h("p", { class: "acct-news-text" }, motd));
    return card;
}

/** A card per newly linked device: confirm it or copy history to it. */
function offerCards(session) {
    return [...deviceOffers.values()].map(device => {
        const confirm = !device.confirmed;
        return h("section", { class: "card acct-news", role: "group", "aria-label": t("acct.newDevice", { name: device.name }) },
            h("p", { class: "row-title", style: "margin:0" }, t("acct.newDevice", { name: device.name })),
            h("p", { class: "hint" }, t(confirm ? "acct.newDeviceConfirm" : "acct.newDeviceCopy")),
            h("div", { class: "acct-card-actions" },
                h("button", { class: "btn btn-primary", type: "button", onclick: async () => {
                    const done = confirm ? await confirmFlow(session, device) : await historyFlow(session, device);
                    if (done) dismissOffer(device.device_id);
                } }, confirm ? t("acct.confirm") : t("acct.copyHistory")),
                h("button", { class: "btn", type: "button", onclick: () => dismissOffer(device.device_id) }, t("acct.dismiss"))));
    });
}

/** The account cards Home shows above the recent conversations. */
export function homeCards(session) {
    return [newsCard(session), ...offerCards(session)].filter(Boolean);
}
