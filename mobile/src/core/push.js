// push.js — Web Push for this device: permission, subscription, the
// server's preferences, foreground reporting and read sync.
import { Msg } from "./frame.js";

export function pushSupported() {
    return typeof navigator !== "undefined" && "serviceWorker" in navigator &&
        typeof window !== "undefined" && "PushManager" in window && "Notification" in window;
}

/** iOS delivers push only to apps added to the Home Screen. */
export function needsInstallForPush() {
    if (typeof navigator === "undefined") return false;
    const ios = /iPhone|iPad|iPod/.test(navigator.userAgent) || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
    const standalone = navigator.standalone === true || globalThis.matchMedia?.("(display-mode: standalone)").matches;
    return ios && !standalone;
}

function deviceRequest(conn, request) {
    return conn.request(Msg.DeviceRequest, request, Msg.DeviceResult, { accept: r => r.action === request.action });
}

function base64URLToBytes(value) {
    const b64 = value.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(value.length / 4) * 4, "=");
    return Uint8Array.from(atob(b64), c => c.charCodeAt(0));
}

function bytesToBase64URL(buffer) {
    let text = "";
    for (const byte of new Uint8Array(buffer)) text += String.fromCharCode(byte);
    return btoa(text).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

async function registration() {
    // ready never settles without a registration (plain-HTTP development).
    if (!await navigator.serviceWorker.getRegistration("/app/")) throw new Error("the app's service worker is not installed");
    return navigator.serviceWorker.ready;
}

/** The browser subscription for vapidKey, created when create is set. */
async function browserSubscription(vapidKey, create) {
    const reg = await registration();
    let sub = await reg.pushManager.getSubscription();
    const key = base64URLToBytes(vapidKey);
    if (sub) {
        const current = sub.options?.applicationServerKey;
        if (current && bytesToBase64URL(current) !== bytesToBase64URL(key)) {
            await sub.unsubscribe();
            sub = null;
        }
    }
    if (!sub && create) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
    return sub;
}

function subscriptionKeys(sub) {
    return { endpoint: sub.endpoint, p256dh: bytesToBase64URL(sub.getKey("p256dh")), auth: bytesToBase64URL(sub.getKey("auth")) };
}

/**
 * Reads the server's push settings and refreshes this device's subscription
 * when notifications are allowed. Returns {available, preferences}.
 */
export async function syncPush(conn) {
    const config = await deviceRequest(conn, { action: "push_config" });
    const state = { available: !!config.vapid_public_key && pushSupported(), vapidKey: config.vapid_public_key, preferences: config.preferences || {} };
    if (!state.available || Notification.permission !== "granted") return state;
    // Refresh on every connect: browsers rotate subscriptions silently.
    const sub = await browserSubscription(state.vapidKey, state.preferences.subscribed);
    if (sub) {
        const result = await deviceRequest(conn, { action: "push_subscribe", subscription: subscriptionKeys(sub) });
        state.preferences = result.preferences || state.preferences;
    }
    return state;
}

/** Asks for permission (needs a tap) and subscribes. */
export async function enablePush(conn, state, preferences = {}) {
    const permission = await Notification.requestPermission();
    if (permission !== "granted") throw Object.assign(new Error("notifications were not allowed"), { name: "NotAllowedError" });
    const sub = await browserSubscription(state.vapidKey, true);
    const result = await deviceRequest(conn, { action: "push_subscribe", subscription: subscriptionKeys(sub), preferences });
    return result.preferences;
}

export async function disablePush(conn) {
    const sub = await (await registration()).pushManager.getSubscription();
    await sub?.unsubscribe();
    const result = await deviceRequest(conn, { action: "push_unsubscribe" });
    return result.preferences;
}

export async function setPushPreferences(conn, preferences) {
    const result = await deviceRequest(conn, { action: "push_preferences", preferences });
    return result.preferences;
}

/** Tells the server whether the app is in the foreground (no push then). */
export function reportVisibility(conn, visible = document.visibilityState === "visible") {
    if (conn.open) conn.send(Msg.DeviceRequest, { action: "activity", active: visible });
}

/** Announces a read conversation to the account's other devices. */
export function announceRead(conn, scope, id) {
    if (conn.open) conn.send(Msg.DeviceRequest, { action: "read", read: { scope, id: String(id) } });
}

/** Closes shown notifications of a conversation tag (e.g. "channel:4"). */
export async function closeNotifications(tag) {
    if (!pushSupported()) return;
    try {
        const reg = await navigator.serviceWorker.getRegistration("/app/");
        for (const notification of await reg?.getNotifications({ tag }) || []) notification.close();
    } catch {
        // Notifications are best effort.
    }
}

// A notification's "Answer" in a closed app opens a fresh window. The worker
// leaves a short-lived, single-use intent here; the page auto-accepts only
// with it, never because of a URL parameter any site can link to.
export const INTENT_CACHE = "noxa-intents";
const ANSWER_INTENT_URL = "/app/__intents/answer.json";
const ANSWER_INTENT_MS = 45000;

/** Worker: records that the user chose Answer on call id. */
export async function saveAnswerIntent(id) {
    const cache = await caches.open(INTENT_CACHE);
    await cache.put(ANSWER_INTENT_URL, new Response(JSON.stringify({ id, until: Date.now() + ANSWER_INTENT_MS }), { headers: { "content-type": "application/json" } }));
}

/** Page: consumes the intent; true only when it names call id and is fresh. */
export async function takeAnswerIntent(id) {
    try {
        if (typeof caches === "undefined") return false;
        const cache = await caches.open(INTENT_CACHE);
        const response = await cache.match(ANSWER_INTENT_URL);
        if (!response) return false;
        await cache.delete(ANSWER_INTENT_URL);
        const intent = await response.json();
        return !!id && intent?.id === id && Number.isFinite(intent.until) && Date.now() <= intent.until;
    } catch {
        return false;
    }
}

/** Closes every shown notification of this app (sign-out, account replacement). */
export async function closeAllNotifications() {
    if (!pushSupported()) return;
    try {
        const reg = await navigator.serviceWorker.getRegistration("/app/");
        for (const notification of await reg?.getNotifications() || []) notification.close();
    } catch {
        // Notifications are best effort.
    }
}

/** Shows the unread count on the app icon where the Badging API exists. */
export function setBadge(count) {
    if (typeof navigator === "undefined") return;
    if (count > 0) navigator.setAppBadge?.(count).catch(() => {});
    else navigator.clearAppBadge?.().catch(() => {});
}
