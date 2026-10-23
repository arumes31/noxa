// app.js — application state shared by the screens: server info, this
// device's identity, the signed-in chat session and reconnection.
import { ChatSession } from "./core/chat-session.js";
import { Connection } from "./core/connection.js";
import { generateIdentity } from "./core/crypto.js";
import { signIn } from "./core/device.js";
import { clearIdentity, loadIdentity, saveIdentity, updateMeta } from "./core/identity-store.js";

export const app = new EventTarget();

app.info = null;
app.identity = null;
app.meta = {};
app.session = null;
app.online = true;

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
    });
    emit("session", session);
    return session;
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
    document.addEventListener("visibilitychange", () => { if (document.visibilityState === "visible") retryNow(); });
}
