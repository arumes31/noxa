// server-tools.js — the Servers tab's server menu (server info, switch
// server, subscribed channels, invitation links), the server info screen
// (#/server-info) and joining password-protected voice channels.
import { app, joinVoice } from "../app.js";
import { get as setting, set as setSettings } from "../core/settings.js";
import { t } from "../i18n.js";
import { channelPassword, pasteInvite } from "./account.js";
import { addServerFlow, serverList } from "./devices.js";
import { h, sheet, toast } from "./dom.js";
import { header, nav } from "./common.js";
import { icon } from "./icons.js";

// --- Server menu -------------------------------------------------------------------

/** The Servers header's menu button. */
export function serverMenuButton(session) {
    return h("button", { class: "icon-btn", type: "button", "aria-label": t("acct.serverMenu"), "aria-haspopup": "dialog", onclick: async () => {
        const choice = await sheet(app.info?.server_name || "noXa", "", [
            { label: t("acct.serverInfo"), value: "info", primary: true },
            { label: t("acct.subscriptions"), value: "subscriptions" },
            { label: t("acct.invitePaste"), value: "invite" },
            { label: t("acct.switchServer"), value: "switch" },
            { label: t("acct.cancel"), value: null },
        ]);
        if (app.session !== session) return;
        if (choice === "info") location.hash = "#/server-info";
        else if (choice === "subscriptions") subscriptionsSheet(session);
        else if (choice === "invite") pasteInvite(session);
        else if (choice === "switch") switchSheet();
    } }, "⋯");
}

/** Lists saved servers to open, plus Add server. */
function switchSheet() {
    const close = () => overlay.remove();
    const { list, render } = serverList();
    const overlay = h("div", { class: "sheet-overlay", onclick: event => { if (event.target === overlay) close(); } },
        h("div", { class: "sheet", role: "dialog", "aria-modal": "true", "aria-labelledby": "switch-title" },
            h("h2", { class: "sheet-title", id: "switch-title" }, t("acct.switchServer")),
            h("p", { class: "sheet-body hint" }, t("acct.serversHint")),
            list,
            h("div", { class: "sheet-actions" },
                h("button", { class: "btn", type: "button", onclick: close }, t("acct.done")),
                h("button", { class: "btn btn-primary", type: "button", onclick: async () => { if (await addServerFlow()) render(); } }, t("acct.addServer")))));
    overlay.addEventListener("keydown", event => { if (event.key === "Escape") close(); });
    document.body.append(overlay);
    overlay.querySelector("button")?.focus();
}

/**
 * Subscribe or unsubscribe text channels. Unsubscribed channels are kept in
 * the unsubscribedChannels setting so the app does not subscribe them again
 * on the next snapshot (app.js).
 */
export function subscriptionsSheet(session) {
    const close = () => overlay.remove();
    const rows = session.channels().filter(c => c.chat).map(channel => {
        const input = h("input", { type: "checkbox", role: "switch", checked: session.subscriptions.has(channel.id), "aria-label": t("acct.subscribe", { name: channel.name }) });
        input.onchange = async () => {
            const subscribe = input.checked;
            input.disabled = true;
            try {
                await session.subscribe([channel.id], subscribe);
                const skip = new Set(setting("unsubscribedChannels") || []);
                if (subscribe) skip.delete(channel.id);
                else skip.add(channel.id);
                setSettings({ unsubscribedChannels: [...skip] });
            } catch (error) {
                input.checked = !subscribe;
                toast(t("common.error", { error: error.message }), "warn");
            } finally { input.disabled = false; }
        };
        return h("li", {}, h("label", { class: "toggle", style: `padding-left:${channel.depth * 14}px` },
            h("span", { class: "row-main" }, h("span", { class: "row-title" }, `# ${channel.name}`), channel.topic ? h("span", { class: "row-sub" }, channel.topic) : null),
            input));
    });
    const overlay = h("div", { class: "sheet-overlay", onclick: event => { if (event.target === overlay) close(); } },
        h("div", { class: "sheet", role: "dialog", "aria-modal": "true", "aria-labelledby": "subs-title" },
            h("h2", { class: "sheet-title", id: "subs-title" }, t("acct.subscriptions")),
            h("p", { class: "sheet-body hint" }, t("acct.subscriptionsHint")),
            h("ul", { class: "list" }, rows),
            h("div", { class: "sheet-actions" }, h("button", { class: "btn btn-primary", type: "button", onclick: close }, t("acct.done")))));
    overlay.addEventListener("keydown", event => { if (event.key === "Escape") close(); });
    document.body.append(overlay);
    overlay.querySelector("input, button")?.focus();
}

/** A lock badge for password-protected channels. */
export function lockBadge(channel) {
    if (!channel.hasPassword) return null;
    const badge = h("span", { class: "acct-lock", title: t("acct.locked"), role: "img", "aria-label": t("acct.locked") }, icon("lock"));
    return badge;
}

/** Joins a voice channel, asking for its password first when it has one. */
export async function joinWithPassword(channel) {
    const password = await channelPassword(channel);
    if (password === null) return false;
    await joinVoice(channel.id, password);
    return true;
}

// --- Server info -------------------------------------------------------------------

function duration(seconds) {
    const s = Math.max(0, Math.floor(seconds || 0));
    const d = Math.floor(s / 86400), hours = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
    return d ? `${d}d ${hours}h` : hours ? `${hours}h ${m}m` : `${m}m ${s % 60}s`;
}

function bytes(n) {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

/** #/server-info: the server's details and this connection. */
export function renderServerInfo(root) {
    const session = app.session;
    const fields = new Map();
    const row = (key, label) => {
        const value = h("dd", { "data-stat": key }, "—");
        fields.set(key, value);
        return [h("dt", {}, label), value];
    };
    const set = (key, value) => { const el = fields.get(key); if (el && el.textContent !== String(value)) el.textContent = String(value); };
    const motd = h("p", { class: "acct-motd" });
    const motdCard = h("section", { class: "card", hidden: "", "aria-labelledby": "motd-title" }, h("h2", { class: "row-title", id: "motd-title", style: "margin:0 0 8px;font-size:1rem" }, t("acct.info.motd")), motd);
    const status = h("p", { class: "error", role: "status" });
    root.replaceChildren(h("section", { class: "screen" },
        header(t("acct.info.title"), { back: "#/servers" }),
        h("div", { class: "screen-body" },
            h("section", { class: "card" }, h("dl", { class: "acct-info" },
                row("name", t("acct.info.name")), row("address", t("acct.info.address")), row("version", t("acct.info.version")),
                row("platform", t("acct.info.platform")), row("uptime", t("acct.info.uptime")), row("users", t("acct.info.users")),
                row("channels", t("acct.info.channels")))),
            h("p", { class: "section-label" }, t("acct.info.connection")),
            h("section", { class: "card", role: "status", "aria-live": "off" }, h("dl", { class: "acct-info" },
                row("ping", t("acct.info.ping")), row("traffic", t("acct.info.traffic")))),
            h("div", { style: "height:12px" }),
            motdCard,
            status,
            h("p", { class: "hint", style: "margin-top:12px" }, t("acct.info.refresh"))),
        nav("servers")));
    set("address", location.host);
    let stopped = false;
    const update = async () => {
        if (stopped || app.session !== session) return;
        try {
            const info = await session.serverInfo();
            set("name", info.name || app.info?.server_name || "—");
            set("version", info.version || "—");
            set("platform", info.platform || "—");
            set("uptime", duration(info.uptime_seconds));
            set("users", info.max_clients ? `${info.clients_online} / ${info.max_clients}` : String(info.clients_online ?? "—"));
            set("channels", String(info.channels_online ?? "—"));
            // The session's MOTD is opened with the global key; the reply's is plain.
            const text = (session.motd || info.motd || "").trim();
            motd.textContent = text;
            motdCard.hidden = !text;
            status.textContent = "";
        } catch (error) {
            status.textContent = t("acct.info.failed", { error: error.message });
        }
        try { set("ping", `${Math.round(await session.ping())} ms`); } catch { set("ping", "—"); }
        set("traffic", `${bytes(session.conn.bytesIn || 0)} / ${bytes(session.conn.bytesOut || 0)}`);
    };
    update();
    const timer = setInterval(() => { if (document.visibilityState === "visible") update(); }, 2000);
    return () => { stopped = true; clearInterval(timer); };
}
