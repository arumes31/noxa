// screens.js — the signed-in screens of the Echo board: Home (02), Servers &
// channels (03), Channel chat (04), Voice (09, pending) and You (14/15).
import { app, forgetDevice } from "../app.js";
import { registerPasskey, passkeysSupported } from "../core/device.js";
import { disablePush, enablePush, needsInstallForPush, pushSupported, setPushPreferences } from "../core/push.js";
import { Msg } from "../core/frame.js";
import { t } from "../i18n.js";
import { avatar, clock, h, sheet, toast } from "./dom.js";
import { icon } from "./icons.js";

export function nav(active) {
    const item = (route, name, label) => h("a", { href: `#/${route}`, "aria-current": active === route ? "page" : null, "aria-label": label }, icon(name), h("span", {}, label));
    return h("nav", { class: "nav", "aria-label": "Main" },
        item("home", "home", t("nav.home")),
        item("servers", "servers", t("nav.servers")),
        item("voice", "voice", t("nav.voice")),
        item("you", "you", t("nav.you")));
}

function header(title, { back, sub } = {}) {
    return h("header", { class: "screen-header" },
        back ? h("a", { class: "icon-btn", href: back, "aria-label": t("common.back") }, icon("back")) : h("span"),
        h("h1", {}, title, sub ? h("span", { class: "sub" }, sub) : null),
        h("span"));
}

function scopeName(session, scope) {
    if (scope === 0) return t("channels.global");
    return `# ${session.channels().find(c => c.id === scope)?.name || scope}`;
}

/** Unread count badge; mentions are highlighted. */
function unreadBadge(scope) {
    const entry = app.unread.get(scope);
    if (!entry?.count) return null;
    return h("span", { class: entry.mentions ? "badge badge-mention" : "badge", "aria-label": t("home.unread", { count: entry.count }) },
        entry.mentions ? `@${entry.count}` : String(entry.count));
}

function lastMessage(session, scope) {
    const list = session.messages.get(scope) || [];
    return list[list.length - 1];
}

// --- Home (02) ----------------------------------------------------------------

export function renderHome(root) {
    const session = app.session;
    const live = session.channels().filter(c => !c.textOnly && c.clients.length);
    const scopes = session.chatScopes()
        .map(scope => ({ scope, last: lastMessage(session, scope) }))
        .filter(entry => entry.last)
        .sort((a, b) => b.last.sentAt - a.last.sentAt);
    root.replaceChildren(h("section", { class: "screen" },
        header(t("home.title")),
        h("div", { class: "screen-body" },
            live.map(channel => h("a", { class: "card row", href: "#/voice", style: "border-color:var(--border-strong)" },
                h("div", { class: "row-main" },
                    h("span", { class: "row-title" }, t("home.live", { channel: channel.name })),
                    h("span", { class: "row-sub" }, channel.clients.map(c => c.nickname).join(", "))),
                h("span", { class: "badge" }, String(channel.clients.length)))),
            h("p", { class: "section-label" }, t("home.recent")),
            scopes.length ? h("ul", { class: "list" }, scopes.map(({ scope, last }) => h("li", {},
                h("a", { class: "row", href: `#/chat/${scope}` },
                    scope === 0 ? h("span", { class: "hash" }, "◎") : h("span", { class: "hash" }, "#"),
                    h("span", { class: "row-main" },
                        h("span", { class: "row-title" }, scopeName(session, scope).replace(/^# /, "#")),
                        h("span", { class: "row-sub" }, `${last.from}: ${last.text ?? t("chat.locked")}`)),
                    unreadBadge(scope) || h("span", { class: "row-meta" }, clock(last.sentAt))))))
                : h("p", { class: "hint" }, t("home.empty")))),
        nav("home"));
}

// --- Servers & channels (03) ----------------------------------------------------

export function renderServers(root) {
    const session = app.session;
    const channels = session.channels();
    root.replaceChildren(h("section", { class: "screen" },
        header(app.info?.server_name || "noXa", { sub: `${session.clients().length} online` }),
        h("div", { class: "screen-body" },
            h("p", { class: "section-label" }, t("channels.text")),
            h("ul", { class: "list" },
                session.current.has(0) ? h("li", {}, h("a", { class: "row", href: "#/chat/0" }, h("span", { class: "hash" }, "◎"), h("span", { class: "row-main" }, h("span", { class: "row-title" }, t("channels.global"))), unreadBadge(0))) : null,
                channels.filter(c => c.chat).map(c => h("li", {}, h("a", { class: "row", href: `#/chat/${c.id}`, style: `padding-left:${10 + c.depth * 14}px` },
                    h("span", { class: "hash" }, c.restricted ? "🔒" : "#"),
                    h("span", { class: "row-main" }, h("span", { class: "row-title" }, c.name), c.topic ? h("span", { class: "row-sub" }, c.topic) : null),
                    unreadBadge(c.id))))),
            h("p", { class: "section-label" }, t("channels.voice")),
            h("ul", { class: "list" }, channels.filter(c => !c.textOnly).map(c => h("li", {},
                h("div", { class: "row", style: `padding-left:${10 + c.depth * 14}px` },
                    icon("voice"),
                    h("span", { class: "row-main" }, h("span", { class: "row-title" }, c.name)),
                    c.clients.length ? h("span", { class: "row-meta" }, String(c.clients.length)) : null),
                c.clients.length ? h("ul", { class: "list", style: "padding-left:44px" }, c.clients.map(client => h("li", { class: "row", style: "min-height:36px" },
                    avatar(client.nickname, "sm"), h("span", { class: "row-sub" }, client.nickname)))) : null))))),
        nav("servers"));
}

// --- Channel chat (04) ------------------------------------------------------------

export function renderChat(root, scope) {
    const session = app.session;
    const name = scopeName(session, scope);
    const log = h("div", { class: "chat-log", role: "log", "aria-live": "polite", "aria-label": name });
    const typing = h("div", { class: "typing" });
    const input = h("textarea", { rows: "1", "aria-label": t("chat.placeholder", { channel: name }), placeholder: t("chat.placeholder", { channel: name }), enterkeyhint: "send" });
    const sendButton = h("button", { class: "send", type: "submit", "aria-label": t("chat.send") }, icon("send"));
    let hasOlder = true;
    let lastTyping = 0;

    const older = h("button", { class: "btn", style: "align-self:center;margin:8px 0" }, t("chat.loadOlder"));
    older.onclick = async () => {
        older.disabled = true;
        try { hasOlder = await session.loadHistory(scope); } catch (error) { toast(t("common.error", { error: error.message }), "warn"); }
        older.disabled = false;
        render(false);
    };

    function line(message) {
        const classes = ["msg", message.own ? "own" : "", message.mentioned ? "mention" : "", message.pending ? "pending" : "", message.failed ? "failed" : ""].join(" ");
        let body;
        if (message.deleted) body = h("span", { class: "msg-deleted" }, t("chat.deleted"));
        else if (message.text === null || message.text === undefined) body = h("span", { class: "msg-locked" }, t("chat.locked"));
        else body = message.text;
        const reactions = Object.entries(message.reactions || {}).filter(([, n]) => n > 0);
        return h("div", { class: classes },
            h("span", { class: "msg-time" }, clock(message.sentAt)),
            avatar(message.from, "sm"),
            h("div", { class: "msg-body" },
                h("span", { class: "msg-from" }, message.from),
                body,
                message.edited ? h("span", { class: "msg-edited" }, t("chat.edited")) : null,
                reactions.length ? h("span", { class: "reactions" }, reactions.map(([emoji, n]) => h("span", { class: "reaction" }, `${emoji} ${n}`))) : null,
                message.failed ? h("div", { class: "error" }, t("chat.failed")) : null));
    }

    function render(stick = true) {
        const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
        const messages = session.messages.get(scope) || [];
        log.replaceChildren(hasOlder ? older : h("span"), ...messages.map(line));
        if (stick && (atBottom || messages.at(-1)?.own)) log.scrollTop = log.scrollHeight;
    }

    function renderTyping() {
        const entry = session.typing.get(scope);
        typing.textContent = entry && entry.until > Date.now() ? t("chat.typing", { name: entry.nickname }) : "";
    }

    const form = h("form", { class: "composer", onsubmit: async event => {
        event.preventDefault();
        const text = input.value.trim();
        if (!text) return;
        input.value = "";
        input.style.height = "";
        try { await session.send(scope, text); } catch (error) { toast(t("chat.sendFailed", { error: error.message }), "warn"); }
    } }, input, sendButton);
    input.addEventListener("keydown", event => {
        if (event.key === "Enter" && !event.shiftKey) {
            event.preventDefault();
            form.requestSubmit();
        }
    });
    input.addEventListener("input", () => {
        input.style.height = "";
        input.style.height = `${Math.min(input.scrollHeight, 140)}px`;
        if (Date.now() - lastTyping > 3000) {
            lastTyping = Date.now();
            session.sendTyping(scope);
        }
    });

    const onMessages = event => { if (event.detail.scope === scope) render(); };
    const onTyping = event => { if (event.detail.scope === scope) { renderTyping(); setTimeout(renderTyping, 5100); } };
    session.addEventListener("messages", onMessages);
    session.addEventListener("typing", onTyping);
    root.replaceChildren(h("section", { class: "screen chat" },
        header(name, { back: "#/servers" }),
        log, typing, form));
    render();
    if (!(session.messages.get(scope) || []).length) older.click();
    return () => {
        session.removeEventListener("messages", onMessages);
        session.removeEventListener("typing", onTyping);
    };
}

// --- Voice (09) ----------------------------------------------------------------

export function renderVoice(root) {
    root.replaceChildren(h("section", { class: "screen" },
        header(t("voice.title")),
        h("div", { class: "screen-body" }, h("div", { class: "card" }, h("p", { class: "hint" }, t("voice.later"))))),
        nav("voice"));
}

// --- You: settings, passkeys, devices (14/15) -----------------------------------

async function devices(session) {
    return session.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
}

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
        await registerPasskey(app.session.conn, navigator.platform || "Passkey");
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
    const refresh = async () => {
        try {
            const result = await devices(session);
            list.replaceChildren(...(result.devices || []).map(device => h("li", { class: "row" },
                h("span", { class: "row-main" },
                    h("span", { class: "row-title" }, device.name),
                    h("span", { class: "row-sub" }, [t(`settings.kind.${device.kind}`), device.current ? t("settings.current") : null].filter(Boolean).join(" · ")),
                    h("span", { class: "row-sub mono" }, device.fingerprint),
                    device.expiry_warned_at ? h("span", { class: "row-sub", style: "color:var(--amber)" }, t("settings.expiry", { date: new Date((device.last_seen_at + 180 * 86400) * 1000).toLocaleDateString() })) : null),
                device.current ? null : h("button", { class: "btn btn-danger", onclick: async () => {
                    const ok = await sheet(t("settings.revoke"), device.name, [{ label: t("settings.revoke"), value: true, danger: true }, { label: t("link.cancel"), value: false }]);
                    if (!ok) return;
                    await session.conn.request(Msg.DeviceRequest, { action: "revoke", device_id: device.device_id }, Msg.DeviceResult).catch(e => toast(e.message, "warn"));
                    refresh();
                } }, t("settings.revoke")))));
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
            h("p", { class: "section-label" }, t("settings.notifications")),
            notifications.box,
            h("p", { class: "section-label" }, t("settings.passkeys")),
            passkeys,
            passkeysSupported() ? h("button", { class: "btn btn-primary btn-block", onclick: async () => { await savePasskey(); refresh(); } }, icon("passkey"), " ", t("settings.savePasskey")) : null,
            h("p", { class: "section-label" }, t("settings.devices")),
            list,
            h("div", { style: "height:16px" }),
            h("button", { class: "btn btn-danger btn-block", onclick: async () => {
                const ok = await sheet(t("settings.signOut"), t("settings.signOutConfirm"), [{ label: t("settings.revoke"), value: true, danger: true }, { label: t("link.cancel"), value: false }]);
                if (!ok) return;
                await session.conn.request(Msg.DeviceRequest, { action: "revoke", device_id: session.deviceID }, Msg.DeviceResult).catch(() => {});
                await forgetDevice();
            } }, t("settings.signOut")))),
        nav("you"));
    refresh();
}
