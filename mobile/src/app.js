// app.js — application state shared by the screens: server info, this
// device's identity, the signed-in chat session and reconnection.
import { ChatSession } from "./core/chat-session.js";
import { DirectMessages } from "./core/direct.js";
import { Calls } from "./core/calls.js";
import { Groups } from "./core/groups.js";
import { Voice } from "./core/voice.js";
import { Connection } from "./core/connection.js";
import { generateIdentity } from "./core/crypto.js";
import { signIn } from "./core/device.js";
import { clearIdentity, loadIdentity, saveIdentity, updateMeta } from "./core/identity-store.js";
import { enqueue, flush as flushOutbox, showPending, waitingCount } from "./core/outbox.js";
import { announceRead, closeNotifications, reportVisibility, setBadge, syncPush } from "./core/push.js";
import { get as setting } from "./core/settings.js";
import { addServer } from "./core/servers.js";
import { notifier } from "./notifier.js";

export const app = new EventTarget();
notifier.install(app);

app.info = null;
app.identity = null;
app.meta = {};
app.guestPassword = "";
app.session = null;
app.online = true;
/** Push availability and this device's preferences, after syncPush. */
app.push = { available: false, preferences: {} };
/** Direct messages of the signed-in session. */
app.dm = null;
/** Channel voice of the signed-in session. */
app.voice = null;
/** Private calls of the signed-in session. */
app.calls = null;
/** Private groups of the signed-in session. */
app.groups = null;
/** Conversation tag ("channel:4", "dm:<uid>") -> {count, mentions} unread. */
app.unread = new Map();
/** The conversation tag on screen, or null. */
app.viewing = null;
/** Messages waiting in the outbox. */
app.outboxWaiting = 0;

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
    // The first open of a server puts it on this browser's server list.
    try { addServer(location.origin, app.info.server_name || ""); } catch { /* not a web origin */ }
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
    app.groups?.close();
    app.groups = null;
    app.calls?.close();
    app.calls = null;
    app.voice?.close();
    app.voice = null;
    app.dm?.close();
    app.dm = null;
    const previousSession = app.session;
    app.session = null;
    previousSession?.close();
    await clearIdentity();
    app.identity = null;
    app.meta = {};
    app.guestPassword = "";
    emit("signedout");
}

/** Signs in with the stored device key and starts the chat session. */
export async function connect({ guest = null } = {}) {
    clearTimeout(reconnectTimer);
    app.paused = false;
    const reuseGuest = !!(guest && app.meta.guest && app.identity);
    const identity = guest ? (reuseGuest ? app.identity : newIdentity()) : app.identity;
    const conn = await Connection.open();
    let auth;
    try {
        auth = await signIn(conn, identity, {
            version: app.info?.version || "0.0.0",
            nickname: guest ? guest.nickname : app.meta.guest ? (app.meta.guestNickname || app.meta.nickname) : "",
            serverPassword: guest ? guest.serverPassword : app.guestPassword || "",
        });
    } catch (error) {
        conn.close();
        if (!guest && /signed out|link it again/i.test(error.message)) {
            await forgetDevice();
            error.revoked = true;
        }
        throw error;
    }
    const previousSession = app.session;
    const session = new ChatSession(conn, identity, auth);
    app.session = session;
    previousSession?.close();
    // Subscribing delivers a channel's chat and keys without joining it;
    // channels the user unsubscribed from in Servers stay unsubscribed.
    const subscribeMissing = () => {
        if (session.pendingRules) return;
        const skip = new Set(setting("unsubscribedChannels") || []);
        const missing = session.channels().filter(c => c.chat && !session.subscriptions.has(c.id) && !skip.has(c.id)).map(c => c.id);
        if (missing.length) session.subscribe(missing).catch(() => {});
    };
    session.addEventListener("snapshot", subscribeMissing);
    reconnectAttempt = 0;
    setOnline(true);
    app.meta = { ...(guest ? { guest: true, linked: false } : app.meta), deviceID: auth.device_id, uniqueID: auth.unique_id, nickname: auth.nickname };
    if (guest) {
        // Authentication has succeeded. Retain incoming channel frames while
        // replacing local data, and never erase a saved login on a bad password.
        app.identity = identity;
        app.guestPassword = guest.serverPassword;
        app.meta.guestNickname = guest.nickname;
        if (!reuseGuest) await clearIdentity();
        await saveIdentity(identity, app.meta);
    }
    const rejoin = app.voice?.channelID || 0;
    const previousVoice = app.voice;
    previousVoice?.close();
    // Mute, deafen and the last whisperer carry over to the new session.
    app.voice = new Voice(session, previousVoice);
    watchVoice(app.voice);
    // A dropped connection ends the call; rejoin the same channel.
    if (rejoin) joinVoice(rejoin).catch(() => {});
    app.dm?.close();
    const dm = new DirectMessages(session, app.meta);
    app.dm = dm;
    dm.addEventListener("messages", event => {
        const { peer, message, isNew } = event.detail;
        if (isNew && !message.self && !message.copied) noteUnread(`dm:${peer}`, true, { kind: "dm", from: peer, name: message.fromNickname, text: message.body });
        emit("dm", { peer });
    });
    app.groups?.close();
    app.groups = new Groups(session, dm);
    app.groups.addEventListener("incoming", event => noteUnread(`conversation:${event.detail.id}`, false, { kind: "group", name: app.groups?.list.get(event.detail.id)?.name }));
    app.groups.addEventListener("list", () => emit("groups"));
    app.groups.refresh().catch(() => {});
    app.calls?.close();
    app.calls = new Calls(session, dm);
    watchCalls(app.calls);
    dm.importHistory().catch(() => {});
    dm.ready.then(async () => {
        await showPending(session, dm);
        await flushOutbox(session, dm);
        await outboxChanged();
    }).catch(() => {});
    conn.addEventListener("close", () => {
        if (app.session !== session) return;
        setOnline(false);
        scheduleReconnect();
    });
    session.addEventListener("device", event => {
        if (event.detail?.event === "revoked" && event.detail.device?.device_id === session.deviceID) forgetDevice();
        if (event.detail?.event === "read" && event.detail.read) readElsewhere(event.detail.read);
        if (event.detail?.event === "history_ready") dm.importHistory().catch(() => {});
    });
    // Until the rules are accepted the server refuses chat requests; redo them.
    session.addEventListener("rules", () => {
        if (session.pendingRules || app.session !== session) return;
        subscribeMissing();
        app.groups?.refresh().catch(() => {});
        dm.importHistory().catch(() => {});
        flushOutbox(session, dm).then(outboxChanged).catch(() => {});
    });
    session.addEventListener("incoming", event => {
        const { scope, message } = event.detail;
        if (!message.own) noteUnread(`channel:${scope}`, message.mentioned, { kind: "channel", channelID: scope, from: message.fromUniqueID, name: message.from, text: message.text });
    });
    if (!app.meta.guest) reportVisibility(conn);
    if (!app.meta.guest) syncPush(conn).then(state => {
        app.push = state;
        emit("push", state);
    }).catch(() => {});
    emit("session", session);
    // Register every incoming handler before IndexedDB can yield to replayed DMs.
    await updateMeta({ deviceID: auth.device_id, uniqueID: auth.unique_id, nickname: auth.nickname });
    return session;
}

async function outboxChanged() {
    app.outboxWaiting = await waitingCount().catch(() => 0);
    emit("outbox", app.outboxWaiting);
}

/** Keeps a message that could not be sent for the outbox. */
export async function queueMessage(target, text, options) {
    await enqueue(target, text, options);
    await outboxChanged();
    if (app.session?.conn.open && app.dm) flushOutbox(app.session, app.dm).then(outboxChanged).catch(() => {});
}

/** Sends waiting messages, e.g. when the service worker asks. */
export async function flushNow() {
    if (!app.session?.conn.open || !app.dm) return;
    await flushOutbox(app.session, app.dm);
    await outboxChanged();
}

/** Joins a voice channel (with its password, if any) and keeps the phone awake while connected. */
export async function joinVoice(channelID, password = "") {
    if (!app.voice) throw new Error("not connected");
    await app.voice.join(channelID, password);
}

/**
 * Leaves the server without forgetting this device, e.g. after declining
 * the rules. The Connect screen offers to sign in again.
 */
export function disconnect() {
    clearTimeout(reconnectTimer);
    const session = app.session;
    app.session = null;
    app.voice?.close();
    app.voice = null;
    app.calls?.close();
    app.calls = null;
    app.groups?.close();
    app.groups = null;
    app.dm?.close();
    app.dm = null;
    app.paused = true;
    session?.close();
    emit("disconnected");
}

export async function leaveVoice() {
    await app.voice?.leave();
}

let wakeLock = null;

async function holdWakeLock(active) {
    if (active && !wakeLock && document.visibilityState === "visible") {
        wakeLock = await navigator.wakeLock?.request("screen").catch(() => null);
        wakeLock?.addEventListener("release", () => { wakeLock = null; });
    } else if (!active && wakeLock) {
        await wakeLock.release().catch(() => {});
        wakeLock = null;
    }
}

/** Media Session controls and the wake lock follow the voice state. */
function watchVoice(voice) {
    voice.addEventListener("state", () => {
        emit("voice", voice);
        holdWakeLock(voice.active);
        const media = navigator.mediaSession;
        if (!media) return;
        if (!voice.active) {
            media.metadata = null;
            for (const action of ["hangup", "togglemicrophone"]) {
                try { media.setActionHandler(action, null); } catch { /* unsupported */ }
            }
            return;
        }
        const channel = app.session?.channels().find(c => c.id === voice.channelID);
        try {
            media.metadata = new MediaMetadata({ title: channel?.name || "noXa", artist: app.info?.server_name || "noXa" });
            media.setActionHandler("hangup", () => leaveVoice());
            media.setActionHandler("togglemicrophone", () => voice.setMuted(!voice.muted));
            media.setMicrophoneActive?.(!voice.muted);
        } catch {
            // Older browsers lack the conferencing actions.
        }
    });
    voice.addEventListener("speaking", () => emit("voice", voice));
}

let ringTimer = 0;

/** An incoming call opens the call screen and vibrates until answered. */
function watchCalls(calls) {
    calls.addEventListener("state", () => {
        emit("call", calls);
        clearInterval(ringTimer);
        if (calls.incoming) {
            if (!location.hash.startsWith(`#/call/${calls.call.id}`)) location.hash = `#/call/${calls.call.id}`;
            navigator.vibrate?.([400, 200, 400]);
            ringTimer = setInterval(() => navigator.vibrate?.([400, 200, 400]), 2000);
        }
        holdWakeLock(calls.active || !!app.voice?.active);
    });
    calls.addEventListener("ended", () => {
        clearInterval(ringTimer);
        navigator.vibrate?.(0);
        emit("call", calls);
    });
}

// --- Call-back reminders ---------------------------------------------------------

const REMINDERS_KEY = "noxa.callReminders";
export const REMIND_AFTER = 10 * 60 * 1000;

/** Pending "call back" reminders: [{uid, name, at}]. Per browser only. */
export function reminders() {
    try { return JSON.parse(localStorage.getItem(REMINDERS_KEY) || "[]"); } catch { return []; }
}

function saveReminders(list) {
    try { localStorage.setItem(REMINDERS_KEY, JSON.stringify(list)); } catch { /* private window */ }
    emit("reminders");
}

let reminderTimer = 0;

/** Schedules the next due reminder; due ones notify and stay on Home. */
function scheduleReminders() {
    clearTimeout(reminderTimer);
    const pending = reminders().filter(r => !r.shown);
    if (!pending.length) return;
    const next = Math.min(...pending.map(r => r.at));
    reminderTimer = setTimeout(async () => {
        const now = Date.now();
        const list = reminders();
        for (const reminder of list.filter(r => !r.shown && r.at <= now)) {
            reminder.shown = true;
            navigator.vibrate?.([200, 100, 200]);
            const registration = await navigator.serviceWorker?.getRegistration("/app/").catch(() => null);
            await registration?.showNotification(reminder.name, { body: reminder.body, tag: `remind:${reminder.uid}`, data: { url: `/app/#/dm/${encodeURIComponent(reminder.uid)}` } }).catch(() => {});
        }
        saveReminders(list);
        scheduleReminders();
    }, Math.max(1000, next - Date.now()));
}

/** Declines a call and reminds to call back later. */
export function remindLater(uid, name, body) {
    saveReminders([...reminders().filter(r => r.uid !== uid), { uid, name, body, at: Date.now() + REMIND_AFTER }]);
    scheduleReminders();
}

export function dismissReminder(uid) {
    saveReminders(reminders().filter(r => r.uid !== uid));
    scheduleReminders();
}

if (typeof window !== "undefined") scheduleReminders();

/** Calls everyone in a private group. */
export async function startGroupCall(conversationID) {
    if (!app.calls) throw new Error("not connected");
    if (app.voice?.active) await leaveVoice();
    const call = await app.calls.startGroup(conversationID);
    location.hash = `#/call/${call.id}`;
}

/** Starts a private call and opens the call screen. */
export async function startCall(uid, options) {
    if (!app.calls) throw new Error("not connected");
    if (app.voice?.active) await leaveVoice();
    const call = await app.calls.start(uid, options);
    location.hash = `#/call/${call.id}`;
}

function unreadChanged() {
    let total = 0;
    for (const entry of app.unread.values()) total += entry.count;
    setBadge(total);
    emit("unread");
}

/** Counts an incoming message unless its conversation is on screen; notifier.decide applies the notification settings. */
function noteUnread(tag, mention, info = {}) {
    const viewing = app.viewing === tag && document.visibilityState === "visible";
    const decision = notifier.decide(tag, { ...info, mention, viewing });
    if (viewing) {
        markRead(tag);
        return;
    }
    if (!decision.count) return;
    const entry = app.unread.get(tag) || { count: 0, mentions: 0 };
    entry.count++;
    if (mention) entry.mentions++;
    app.unread.set(tag, entry);
    unreadChanged();
}

/** Marks a conversation read here and on the account's other devices. */
export function markRead(tag) {
    closeNotifications(tag);
    notifier.read(tag);
    const had = app.unread.delete(tag);
    const split = tag.indexOf(":");
    if (app.session) announceRead(app.session.conn, tag.slice(0, split), tag.slice(split + 1));
    if (had) unreadChanged();
}

/** Another device of the account read a conversation. */
function readElsewhere({ scope, id }) {
    const tag = `${scope}:${id}`;
    closeNotifications(tag);
    notifier.read(tag);
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
        if (!app.identity || app.paused) return;
        connect().catch(error => {
            if (!error.revoked) scheduleReconnect();
        });
    }, delay);
}

// Reconnect promptly when the phone comes back online or to the foreground.
if (typeof window !== "undefined") {
    const retryNow = () => {
        if (!app.identity || app.paused) return;
        if (!app.online) {
            reconnectAttempt = 0;
            scheduleReconnect();
        } else {
            // A suspended WebSocket may still report OPEN. A failed probe closes
            // it and lets the regular close handler reconnect this session.
            app.session?.conn.probe().catch(() => {});
        }
    };
    window.addEventListener("online", retryNow);
    window.addEventListener("pageshow", event => { if (event.persisted) retryNow(); });
    document.addEventListener("visibilitychange", () => {
        if (app.session && !app.meta.guest) reportVisibility(app.session.conn);
        if (document.visibilityState !== "visible") return;
        retryNow();
        if (app.viewing !== null) markRead(app.viewing);
        if (app.voice?.active) holdWakeLock(true);
    });
}
