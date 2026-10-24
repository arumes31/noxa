// app.js — application state shared by the screens: server info, this
// device's identity, the signed-in chat session and reconnection.
import { ChatSession } from "./core/chat-session.js";
import { DirectMessages } from "./core/direct.js";
import { Connection } from "./core/connection.js";
import { generateIdentity } from "./core/crypto.js";
import { signIn } from "./core/device.js";
import { clearIdentity, loadIdentity, saveIdentity, updateMeta } from "./core/identity-store.js";
import { announceRead, closeNotifications, reportVisibility, setBadge, syncPush } from "./core/push.js";

export const app = new EventTarget();

app.info = null;
app.identity = null;
app.meta = {};
app.session = null;
app.online = true;
/** Push availability and this device's preferences, after syncPush. */
app.push = { available: false, preferences: {} };
/** Direct messages of the signed-in session. */
app.dm = null;
/** Conversation tag ("channel:4", "dm:<uid>") -> {count, mentions} unread. */
app.unread = new Map();
/** The conversation tag on screen, or null. */
app.viewing = null;

const RECONNECT_STEPS = [1000, 2000, 5000, 10000, 20000, 30000];
let reconnectAttempt = 0;
let reconnectTimer = 0;

function emit(type, detail) {
    app.dispatchEvent(new CustomEvent(type, { detail }));
}

export async function loadInfo(fetchImpl = fetch) {
    const response = await fetchImpl("/api/v1/web/info", { cache: "no-store" });
    if (!response.ok) throw new Error(`server answered ${response.status}`);
    app.info = await response.json();
    return app.info;
}

export async function loadDevice() {
    const stored = await loadIdentity();
    app.identity = stored?.identity || null;
    app.meta = stored?.meta || {};
    return stored;
}

/** A fresh identity for linking; it is kept only once a link is approved. */
export function newIdentity() {
    return generateIdentity();
}

export async function rememberLinkedIdentity(identity, meta) {
    await saveIdentity(identity, { ...meta, linked: true });
    app.identity = identity;
    app.meta = { ...meta, linked: true };
}

export async function forgetDevice() {
    app.dm?.close();
    app.dm = null;
    app.session?.close();
    app.session = null;
    await clearIdentity();
    app.identity = null;
    app.meta = {};
    emit("signedout");
}

/** Signs in with the stored device key and starts the chat session. */
export async function connect() {
    clearTimeout(reconnectTimer);
    const conn = await Connection.open();
    let auth;
    try {
        auth = await signIn(conn, app.identity, { version: app.info?.version || "0.0.0" });
    } catch (error) {
        conn.close();
        if (/signed out|link it again/i.test(error.message)) {
            await forgetDevice();
            error.revoked = true;
        }
        throw error;
    }
    app.session?.close();
    const session = new ChatSession(conn, app.identity, auth);
    app.session = session;
    reconnectAttempt = 0;
    setOnline(true);
    await updateMeta({ deviceID: auth.device_id, uniqueID: auth.unique_id, nickname: auth.nickname });
    app.meta = { ...app.meta, deviceID: auth.device_id, uniqueID: auth.unique_id, nickname: auth.nickname };
    app.dm?.close();
    const dm = new DirectMessages(session, app.meta);
    app.dm = dm;
    dm.addEventListener("messages", event => {
        const { peer, message, isNew } = event.detail;
        if (isNew && !message.self && !message.copied) noteUnread(`dm:${peer}`, true);
        emit("dm", { peer });
    });
    dm.importHistory().catch(() => {});
    conn.addEventListener("close", () => {
        if (app.session !== session) return;
        setOnline(false);
        scheduleReconnect();
    });
    session.addEventListener("snapshot", () => {
        // Subscribing delivers a channel's chat and keys without joining it.
        const missing = session.channels().filter(c => c.chat && !session.subscriptions.has(c.id)).map(c => c.id);
        if (missing.length) session.subscribe(missing).catch(() => {});
    });
    session.addEventListener("device", event => {
        if (event.detail?.event === "revoked" && event.detail.device?.device_id === session.deviceID) forgetDevice();
        if (event.detail?.event === "read" && event.detail.read) readElsewhere(event.detail.read);
        if (event.detail?.event === "history_ready") dm.importHistory().catch(() => {});
    });
    session.addEventListener("incoming", event => {
        const { scope, message } = event.detail;
        if (!message.own) noteUnread(`channel:${scope}`, message.mentioned);
    });
    reportVisibility(conn);
    syncPush(conn).then(state => {
        app.push = state;
        emit("push", state);
    }).catch(() => {});
    emit("session", session);
    return session;
}

function unreadChanged() {
    let total = 0;
    for (const entry of app.unread.values()) total += entry.count;
    setBadge(total);
    emit("unread");
}

/** Counts an incoming message unless its conversation is on screen. */
function noteUnread(tag, mention) {
    if (app.viewing === tag && document.visibilityState === "visible") {
        markRead(tag);
        return;
    }
    const entry = app.unread.get(tag) || { count: 0, mentions: 0 };
    entry.count++;
    if (mention) entry.mentions++;
    app.unread.set(tag, entry);
    unreadChanged();
}

/** Marks a conversation read here and on the account's other devices. */
export function markRead(tag) {
    closeNotifications(tag);
    const had = app.unread.delete(tag);
    const split = tag.indexOf(":");
    if (app.session) announceRead(app.session.conn, tag.slice(0, split), tag.slice(split + 1));
    if (had) unreadChanged();
}

/** Another device of the account read a conversation. */
function readElsewhere({ scope, id }) {
    const tag = `${scope}:${id}`;
    closeNotifications(tag);
    if (app.unread.delete(tag)) unreadChanged();
}

function setOnline(online) {
    if (app.online === online) return;
    app.online = online;
    emit("online", online);
}

function scheduleReconnect() {
    const delay = RECONNECT_STEPS[Math.min(reconnectAttempt, RECONNECT_STEPS.length - 1)];
    reconnectAttempt++;
    clearTimeout(reconnectTimer);
    reconnectTimer = setTimeout(() => {
        if (!app.identity) return;
        connect().catch(error => {
            if (!error.revoked) scheduleReconnect();
        });
    }, delay);
}

// Reconnect promptly when the phone comes back online or to the foreground.
if (typeof window !== "undefined") {
    const retryNow = () => {
        if (app.identity && !app.online) {
            reconnectAttempt = 0;
            scheduleReconnect();
        }
    };
    window.addEventListener("online", retryNow);
    document.addEventListener("visibilitychange", () => {
        if (app.session) reportVisibility(app.session.conn);
        if (document.visibilityState !== "visible") return;
        retryNow();
        if (app.viewing !== null) markRead(app.viewing);
    });
}
