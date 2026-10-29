// app.js — application state shared by the screens: server info, this
// device's identity, the signed-in chat session and reconnection.
import { ChatSession } from "./core/chat-session.js";
import { DirectMessages } from "./core/direct.js";
import { Calls } from "./core/calls.js";
import { Groups } from "./core/groups.js";
import { Voice } from "./core/voice.js";
import { SavedMessages } from "./core/saved.js";
import { Connection } from "./core/connection.js";
import { Msg } from "./core/frame.js";
import { signBundle } from "./core/olm-directory.js";
import { signReset } from "./core/olm-reset.js";
import { announcedBinding, verifyLinkBinding } from "./core/link-binding.js";
import { RatchetConsumer, connectionTransport } from "./core/ratchet-consumer.js";
import { generateIdentity } from "./core/crypto.js";
import { signIn } from "./core/device.js";
import { signKeyRecord } from "./core/dm-trust.js";
import { assertIdentityFence, beginIdentity, clearIdentity, loadIdentity, saveIdentity, updateMeta } from "./core/identity-store.js";
import { enqueue, flush as flushOutbox, showPending, waitingCount } from "./core/outbox.js";
import { announceRead, closeNotifications, reportVisibility, setBadge, syncPush } from "./core/push.js";
import { get as setting } from "./core/settings.js";
import { addServer } from "./core/servers.js";
import { notifier } from "./notifier.js";
import { activityShared, isMuted, syncMuted, watchCallLog, resetCallLog } from "./core/conversation-prefs.js";

export const app = new EventTarget();
notifier.install(app);

app.info = null;
app.identity = null;
app.identityFence = null;
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
/** Reconnect attempts since the page loaded (Diagnostics). */
app.reconnects = 0;
/** Saved message references, shared by the message menu and the Saved screen. */
app.saved = typeof indexedDB === "undefined" ? null : new SavedMessages();

const RECONNECT_STEPS = [1000, 2000, 5000, 10000, 20000, 30000];
let reconnectAttempt = 0;
let reconnectTimer = 0;
let connectionGeneration = 0;

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
    app.identityFence = stored?.fence || null;
    app.meta = stored?.meta || {};
    return stored;
}

/** A fresh identity for linking; it is kept only once a link is approved. */
export function newIdentity() {
    return generateIdentity();
}

/**
 * The account whose data this browser holds: the stored identity's account,
 * or, for an identity linked but not signed in yet, the account it replaced
 * (priorPrincipal). "" when nothing is stored.
 */
async function storedPrincipal() {
    let meta = app.meta || {};
    try { meta = (await loadIdentity())?.meta || {}; } catch { /* keep the in-memory view */ }
    return meta.uniqueID || meta.priorPrincipal || "";
}

/**
 * Keeps a newly linked or restored identity. Linking or restoring a different
 * account replaces ALL of the old account's data (conversations, pins,
 * activity, history-import state, outbox, ratchet stores, settings) in the
 * same transaction, so nothing of it is shown or sent as the new account.
 * The new account is known here from the desktop-signed link statement or the
 * backup; otherwise (typed code, passkey) the old account is remembered as
 * priorPrincipal and connect() replaces its data before the new session
 * loads or sends anything. Re-linking the same account keeps its data.
 */
export async function rememberLinkedIdentity(identity, meta, fence) {
    const previous = await storedPrincipal();
    const next = meta.uniqueID || meta.linkBinding?.principal || "";
    const replaceAccount = !!previous && !!next && previous !== next;
    const stored = { ...meta, linked: true, ...(previous && !next ? { priorPrincipal: previous } : {}) };
    const committed = await saveIdentity(identity, stored, { onboarding: true, fence, replaceAccount });
    connectionGeneration++;
    app.identity = identity;
    app.identityFence = committed;
    app.meta = stored;
    if (replaceAccount) await resetAccountMemory();
    return committed;
}

/** Drops the in-memory state of a replaced account (its stores are already cleared). */
async function resetAccountMemory() {
    clearReminders();
    await resetCallLog();
    await app.saved?.clear();
    app.unread.clear();
    unreadChanged();
    app.viewing = null;
    emit("account-replaced");
}

export async function forgetDevice(fence) {
    connectionGeneration++;
    stopRatchet();
    clearReminders();
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
    await resetCallLog();
    await app.saved?.clear();
    await clearIdentity(fence);
    app.identity = null;
    app.identityFence = null;
    app.meta = {};
    app.guestPassword = "";
    app.unread.clear();
    unreadChanged();
    app.viewing = null;
    emit("signedout");
}

// --- noxa-olm-v1 consumer ------------------------------------------------
//
// Every signed-in (non-guest) session uses noxa-olm-v1 for direct and group
// messages, with no legacy fallback: when it cannot run (old server, device
// without a desktop-signed binding, changed server certificate, store
// failure) sending is blocked with one explanation (app.ratchetError) and
// legacy history stays readable. The server binding is never supplied here:
// it comes only from the desktop-signed device-link statement stored with
// this device (link-binding.js).
const RATCHET_CAPABILITIES = ["noxa-olm-admission-v1", "noxa-olm-directory-v1"];
let ratchetOptions = { pollMS: 5000, modules: null };
app.ratchet = null;
app.ratchetError = null;
// App-level media-control handlers (Task 2): every session's consumer is
// wired to them before its first drain, so registration survives
// reconnects. Delivery is at most once (see RatchetConsumer.onControl).
const controlHandlers = [];
/** Registers fn for controls delivered to this device; returns unregister(). */
export function onSecureControl(fn) {
    controlHandlers.push(fn);
    return () => { const i = controlHandlers.indexOf(fn); if (i >= 0) controlHandlers.splice(i, 1); };
}

// Encrypted channel media (Task 2): media-key controls go to the current
// voice session only (never to chat, activity, notifications or history).
onSecureControl(control => {
    if (control?.kind === "media-key") app.voice?.receiveMediaKey(control);
});

const MEDIA_E2EE_CAPABILITY = "noxa-media-e2ee-v1";
const SECURE_WAIT_MS = 15000;

/**
 * The encrypted-media adapter of one signed-in session: its identity and the
 * authenticated control transport (RatchetConsumer.sendControl). Voice waits
 * briefly for the secure session that starts right after sign-in.
 */
export function mediaAdapter(session) {
    const current = () => app.session === session;
    return {
        async status() {
            const caps = session.capabilities || new Set();
            const base = { principal: session.uniqueID, device: session.deviceID, clientID: session.clientID };
            if (!caps.has(MEDIA_E2EE_CAPABILITY) || !RATCHET_CAPABILITIES.every(c => caps.has(c))) return { ...base, supported: false, reason: "server_outdated" };
            if (String(session.uniqueID || "").startsWith("guest:")) return { ...base, supported: true, reason: "keyless_guest" };
            if (!session.deviceID) return { ...base, supported: true, reason: "no_device" };
            const deadline = Date.now() + SECURE_WAIT_MS;
            while (current() && !secureMessagingStatus().active && Date.now() < deadline) await new Promise(resolve => setTimeout(resolve, 200));
            const secure = current() && secureMessagingStatus().active;
            return { ...base, supported: true, reason: secure ? "" : "secure_unavailable" };
        },
        async send({ logicalID, principal, device, body }) {
            const owner = app.ratchet;
            if (!current() || !owner || owner.consumer.closed) return { ok: false, code: "secure_unavailable" };
            const { receipt } = await owner.consumer.sendControl({ logicalID, principal, device, body });
            return { ok: receipt?.state === "accepted", code: receipt?.state || "unavailable" };
        },
    };
}

/** Test-only tuning: poll period and store/core factory injection. */
export function configureRatchetForTests(options = null) {
    ratchetOptions = { pollMS: options?.pollMS || 5000, modules: options?.modules || null };
}

/**
 * A guest's binding. A guest identity is self-rooted (no desktop vouches for
 * it), so its server binding is pinned on first use from the server-announced
 * certificate fingerprint and every later session must announce the same one;
 * a change blocks secure messaging (server_binding_rotated).
 */
function guestBinding(meta, auth) {
    const announced = announcedBinding(auth?.tls_fingerprint);
    if (!announced) throw Object.assign(new Error("the server announces no certificate for secure messaging"), { code: "server_binding_unavailable" });
    // A guest cannot re-link: it may accept the change only by an explicit
    // review of the old and new fingerprints (confirmGuestCertificateChange).
    if (meta?.guestBinding && meta.guestBinding !== announced) {
        throw Object.assign(new Error("the server's certificate changed since this guest identity first used it"),
            { code: "guest_binding_rotated", previous: meta.guestBinding, announced });
    }
    return announced;
}

/**
 * A guest's secure-messaging preparation (connect and every retry): first its
 * pin check (a changed server certificate stops here, before this guest
 * publishes or registers anything on that server), then its confirmed key
 * record and its own device, then its first-use pinned binding, which must be
 * durable before any consumer opens (fail closed: an unstored pin is retried,
 * never used).
 */
async function prepareGuest(session, meta, auth, fence) {
    const binding = guestBinding(meta, auth);
    await publishGuestKey(session);
    meta.deviceID = await ensureGuestDevice(session);
    if (meta.guestBinding !== binding) {
        try { await updateMeta({ deviceID: meta.deviceID, guestBinding: binding }, fence); }
        catch (error) { throw Object.assign(new Error("this guest identity's server pin could not be stored"), { code: "secure_unavailable", cause: error }); }
        meta.guestBinding = binding;
    }
}

/**
 * Its key is its own account root: publish the signed key record (as a
 * desktop does) so contacts and its own device chain can verify it. The
 * server answers a publish only with a refusal, so the record is read back
 * (frames are handled in order): a refused or missing record is a retryable
 * secure_unavailable carrying the server's reason, never ignored.
 */
async function publishGuestKey(session) {
    const uid = session.uniqueID;
    const record = signKeyRecord(uid, session.identity.xPublic, session.identity.edSecret);
    let refusal = "";
    const off = session.conn.on(Msg.Error, error => { if (error?.origin_type === Msg.KeyPublish) refusal = String(error.message || "refused"); });
    try {
        let back;
        try {
            session.conn.send(Msg.KeyPublish, { public_key: record });
            back = await session.conn.request(Msg.KeyRequest, { unique_id: uid }, Msg.KeyResponse, { accept: r => r.unique_id === uid, timeout: 15000 });
        } catch (error) {
            throw Object.assign(new Error(`this guest identity's key could not be published: ${refusal || error.message}`), { code: "secure_unavailable", cause: error });
        }
        if (back?.public_key !== record) {
            throw Object.assign(new Error(`this guest identity's key could not be published: ${refusal || "the server does not hold it"}`), { code: "secure_unavailable" });
        }
    } finally { off(); }
}

/** A self-rooted guest registers its own key as its first device (once). */
async function ensureGuestDevice(session) {
    if (session.deviceID) return session.deviceID;
    const result = await session.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list", timeout: 15000 });
    const current = (result.devices || []).find(d => d.current);
    if (!current?.device_id) throw Object.assign(new Error("this guest identity could not register a device"), { code: "secure_unavailable" });
    session.deviceID = current.device_id;
    return session.deviceID;
}

async function openRatchet(session, fence, isCurrent, meta, auth = {}) {
    // Members: re-verified on every open against this device's keys and the
    // signed-in account (missing or mismatching statements block secure
    // messaging). Guests: the first-use pinned binding.
    const binding = meta?.guest ? guestBinding(meta, auth)
        : verifyLinkBinding(meta?.linkBinding, { identity: session.identity, principal: session.uniqueID, approverFingerprint: meta?.bindingAnchor || "" });
    // A server certificate that changed since the desktop signed the binding
    // (rotation) blocks too: the device must be linked again from a desktop
    // that accepted the new certificate. The announced value only detects.
    const announced = announcedBinding(auth?.tls_fingerprint);
    if (announced && announced !== binding) throw Object.assign(new Error("the server's certificate changed since this device was linked"), { code: "server_binding_rotated" });
    // modules: test-only injection of the store/core factories (lifecycle tests).
    const [{ openRatchetStore }, { createRatchetCore }] = ratchetOptions.modules
        ? await ratchetOptions.modules()
        : await Promise.all([import("./core/ratchet-store.js"), import("./core/ratchet-core.js")]);
    const namespace = { server: binding, principal: session.uniqueID, device: session.deviceID };
    const store = await openRatchetStore(namespace);
    const core = await createRatchetCore();
    const owner = { consumer: null, core, timer: 0, dm: null };
    const fenced = () => Object.assign(new Error("ratchet session fenced"), { code: "fenced" });
    owner.consumer = new RatchetConsumer({
        store, core, namespace, ...connectionTransport(session.conn, Msg),
        // noXa device keys come only from the pinned root/approval chain.
        deviceKey: async (principal, device) => {
            for (const refresh of [false, true]) {
                const entry = await owner.dm.devicesOf(principal, { refresh });
                // This account's own root is not the one the linking desktop
                // vouched for: block secure messaging (re-link), no own fanout.
                if (principal === session.uniqueID && entry.block) {
                    blockOwnRoot(owner, entry.block);
                    throw fenced();
                }
                if (entry.trust === "changed") throw Object.assign(new Error("identity changed"), { code: "identity_changed" });
                const found = entry.devices.find(d => d.deviceID === device);
                if (found) return found.signingKey;
            }
            throw Object.assign(new Error("untrusted device"), { code: "untrusted_device" });
        },
        signBundle: async bundle => { await assertIdentityFence(fence); return signBundle(bundle, session.identity.edSecret); },
        // Explicit secure-messaging recovery (Settings): the noXa key signs the reset.
        signReset: async reset => { await assertIdentityFence(fence); return signReset(reset, session.identity.edSecret); },
        ownKey: session.identity.edPublic,
        assertCurrent: () => { if (!isCurrent()) throw fenced(); },
        // Durable identity-change confirmations (stored with the contact pin).
        trustReset: principal => owner.dm ? owner.dm.ratchetResetPending(principal) : "",
        trustResetDone: (principal, token) => owner.dm?.clearRatchetReset(principal, token),
    });
    owner.consumer.onControl(async control => { for (const fn of [...controlHandlers]) await fn(control); });
    return owner;
}

/**
 * The active secure-messaging path of this session, for labels and
 * diagnostics (never a static claim): {active, reason}. reason is a code
 * (e.g. "binding_missing", "recovery_required", "inactive").
 */
export function secureMessagingStatus() {
    const owner = app.ratchet;
    if (owner && !owner.consumer.closed && !owner.consumer.sticky) return { active: true, reason: "" };
    const error = app.ratchetError;
    const status = { active: false, reason: owner?.consumer.sticky?.code || error?.code || "inactive" };
    // A guest whose server certificate changed may review and accept it.
    if (status.reason === "guest_binding_rotated") {
        status.rotation = { previous: error.previous, announced: error.announced };
        status.confirm = () => confirmGuestCertificateChange(error.announced);
    }
    return status;
}

// Transient open failures (store contention, a failed lazy import, an
// IndexedDB error) are retried with bounded backoff in the same session; the
// outbox keeps its entries queued meanwhile. Configuration refusals
// (old server, binding) need a reconnect or re-link instead.
const RATCHET_RETRYABLE = new Set(["secure_unavailable", "conflict", "local_conflict", "unavailable", "busy"]);
const RATCHET_RETRY_MS = [5000, 15000, 30000, 60000, 120000, 300000];
let ratchetRetryTimer = 0;
function retryRatchet(session, fence, meta, auth, n, delay = RATCHET_RETRY_MS[n]) {
    clearTimeout(ratchetRetryTimer);
    if (n >= RATCHET_RETRY_MS.length) return;
    ratchetRetryTimer = setTimeout(async () => {
        const current = () => app.session === session && session.conn.open !== false && !app.ratchet;
        if (!current()) return;
        let owner;
        try {
            // The same preparation as connect (CodeRabbit b7): a guest whose
            // device list failed registers and pins before opening.
            if (meta.guest) await prepareGuest(session, meta, auth, fence);
            owner = await openRatchet(session, fence, () => app.session === session && session.conn.open !== false, meta, auth);
        } catch (error) {
            if (!current()) return;
            app.ratchetError = error.code ? error : Object.assign(error, { code: "secure_unavailable" });
            if (RATCHET_RETRYABLE.has(app.ratchetError.code)) retryRatchet(session, fence, meta, auth, n + 1);
            return;
        }
        if (!current()) { owner.consumer.close().finally(() => owner.core.close()); return; }
        app.ratchet = owner;
        app.ratchetError = null;
        owner.dm = app.dm;
        app.dm?.attachRatchet(owner.consumer).catch(() => {});
        owner.groups = app.groups;
        app.groups?.attachRatchet(owner.consumer).catch(() => {});
        pollRatchet(owner, session);
        guardOwnRoot(owner, session);
        emit("secure-messaging", { active: true, reason: "" });
        flushNow().catch(() => {});
    }, delay);
}

/**
 * The guest's explicit acceptance of a changed server certificate, after
 * the UI showed the previous and the announced fingerprint and the risk.
 * Never automatic: only this call re-pins, and only the exact announced
 * binding the review showed. Resolves true once secure messaging opens.
 */
export async function confirmGuestCertificateChange(expected) {
    const ctx = app.ratchetContext, error = app.ratchetError;
    if (!ctx || error?.code !== "guest_binding_rotated" || error.announced !== expected || !ctx.meta.guest || app.session !== ctx.session) return false;
    // Fails closed: an unstored pin is never used. The caller (the review
    // sheet) shows the failure; the blocked state stays as it was.
    try { await updateMeta({ guestBinding: expected }, ctx.fence); }
    catch (error) { throw Object.assign(new Error("the accepted server certificate could not be stored"), { code: "secure_unavailable", cause: error }); }
    ctx.meta.guestBinding = expected;
    app.ratchetError = Object.assign(new Error("secure messaging is starting"), { code: "secure_unavailable" });
    retryRatchet(ctx.session, ctx.fence, ctx.meta, ctx.auth, 0, 0);
    return true;
}

/**
 * Blocks secure messaging of this session when the server's key for this
 * account does not chain to the desktop that linked this device (direct.js
 * #ownRoot): nothing is sealed for the account's devices; sends stay queued
 * with the re-link explanation until the device is linked again.
 */
function blockOwnRoot(owner, error) {
    if (app.ratchet !== owner) return;
    stopRatchet();
    app.ratchetError = error;
    if (owner.dm?.ratchet === owner.consumer) owner.dm.ratchet = null;
    if (owner.groups?.ratchet === owner.consumer) owner.groups.ratchet = null;
    emit("secure-messaging", { active: false, reason: error.code });
}

/** Checks this account's own root as soon as a secure session starts. */
function guardOwnRoot(owner, session) {
    Promise.resolve().then(() => owner.dm?.devicesOf(session.uniqueID)).then(entry => {
        if (entry?.block && app.session === session) blockOwnRoot(owner, entry.block);
    }).catch(() => {});
}

function stopRatchet() {
    clearTimeout(ratchetRetryTimer);
    const owner = app.ratchet;
    app.ratchet = null;
    if (!owner) return;
    clearTimeout(owner.timer);
    owner.stopDoorbell?.();
    owner.consumer.close().finally(() => owner.core.close());
}

const DOORBELL_DRAIN_MS = 250;

function pollRatchet(owner, session) {
    const current = () => app.ratchet === owner && app.session === session && !owner.consumer.closed;
    let draining = false, again = false;
    const tick = () => {
        clearTimeout(owner.timer);
        if (draining) { again = true; return; }
        draining = true;
        return drain().finally(() => { draining = false; if (again && current()) { again = false; tick(); } });
    };
    // The server's content-free "olm_pending" doorbell (Task 2a review A):
    // drain now instead of polling fast; the poll below is only a fallback.
    // Doorbell drains are spaced so rings can never use up this session's
    // own admission budget.
    let lastRing = 0, ringTimer = 0;
    const onEvent = event => {
        if (event.detail?.type !== "olm_pending" || !current() || ringTimer) return;
        const wait = Math.max(0, lastRing + DOORBELL_DRAIN_MS - Date.now());
        ringTimer = setTimeout(() => { ringTimer = 0; lastRing = Date.now(); if (current()) tick(); }, wait);
    };
    session.addEventListener("server-event", onEvent);
    owner.stopDoorbell = () => { clearTimeout(ringTimer); session.removeEventListener("server-event", onEvent); };
    const drain = () => owner.consumer.drain().then(result => {
        // Unreadable authenticated-protocol copies stay pending on the server
        // and are shown as locked placeholders, never dropped or ACKed.
        if (!current()) return;
        owner.dm?.showQuarantined(result?.quarantined || []).catch(() => {});
        owner.groups?.showQuarantined(result?.quarantined || []);
    }).catch(() => {}).finally(() => {
        if (current()) owner.timer = setTimeout(tick, ratchetOptions.pollMS);
        else owner.stopDoorbell?.();
    });
    tick();
}

/** Signs in with the stored device key and starts the chat session. */
export async function connect({ guest = null, fence } = {}) {
    const attempt = ++connectionGeneration;
    const checkAttempt = () => {
        if (attempt !== connectionGeneration) throw Object.assign(new Error("connection operation fenced"), { code: "fenced" });
    };
    clearTimeout(reconnectTimer);
    app.paused = false;
    const previousIdentity = app.identity, previousFence = app.identityFence, previousMeta = app.meta;
    fence = fence || (!guest && previousFence) || await beginIdentity();
    checkAttempt();
    const reuseGuest = !!(guest && previousMeta.guest && previousIdentity && fence?.active && fence.generation === previousFence?.generation);
    const identity = guest ? (reuseGuest ? previousIdentity : newIdentity()) : previousIdentity;
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
            checkAttempt();
            await forgetDevice(fence);
            error.revoked = true;
        }
        throw error;
    }
    const session = new ChatSession(conn, identity, auth, { deferIncoming: true });
    let activated = false;
    let ratchet = null, ratchetError = null;
    // Subscribing delivers a channel's chat and keys without joining it;
    // channels the user unsubscribed from in Servers stay unsubscribed.
    const subscribeMissing = () => {
        if (!activated || session.pendingRules) return;
        const skip = new Set(setting("unsubscribedChannels") || []);
        const missing = session.channels().filter(c => c.chat && !session.subscriptions.has(c.id) && !skip.has(c.id)).map(c => c.id);
        if (missing.length) session.subscribe(missing).catch(() => {});
    };
    session.addEventListener("snapshot", subscribeMissing);
    // Re-entering the guest form with the same identity keeps its pinned
    // server binding (never silently re-pinned).
    const guestPin = reuseGuest && previousMeta?.guestBinding ? { guestBinding: previousMeta.guestBinding } : {};
    const meta = { ...(guest ? { guest: true, linked: false, guestNickname: guest.nickname, ...guestPin } : app.meta), deviceID: auth.device_id, uniqueID: auth.unique_id, nickname: auth.nickname };
    try {
        checkAttempt();
        if (guest) {
            // Retain early channel frames locally. Replacement and its original
            // flow generation check commit atomically before publishing a session.
            fence = await saveIdentity(identity, meta, { onboarding: !reuseGuest, replaceAccount: !reuseGuest, fence });
        } else if (meta.priorPrincipal) {
            // Linked without knowing the account (typed code, passkey): if it
            // is not the account whose data is stored, that data is replaced
            // before this session loads, shows or sends any of it.
            const replaceAccount = meta.priorPrincipal !== auth.unique_id;
            delete meta.priorPrincipal;
            if (replaceAccount) {
                fence = await saveIdentity(identity, meta, { onboarding: true, replaceAccount: true, fence });
                if (attempt === connectionGeneration) { app.identityFence = fence; app.meta = { ...meta }; }
                await resetAccountMemory();
                checkAttempt();
            }
        }
        await assertIdentityFence(fence);
        if (session.initializationError) throw session.initializationError;
        // Every session with an identity key (members, linked devices and
        // key-identity guests) uses noxa-olm-v1.
        // Iterable from any realm (never instanceof: tests run app.js in a vm).
        const capabilities = new Set(session.capabilities || []);
        if (!RATCHET_CAPABILITIES.every(c => capabilities.has(c))) {
            ratchetError = Object.assign(new Error("this server needs an update for secure messaging"), { code: "server_outdated" });
        } else {
            try {
                if (meta.guest) await prepareGuest(session, meta, auth, fence);
                let published = false;
                ratchet = await openRatchet(session, fence, () => attempt === connectionGeneration && (!published || app.session === session) && session.conn.open !== false, meta, auth);
                ratchet.publish = () => { published = true; };
            } catch (error) {
                // Blocks secure sending with an explanation; the session
                // (channels, legacy history) continues. Transient failures
                // are retried below.
                checkAttempt();
                ratchetError = error.code ? error : Object.assign(error, { code: "secure_unavailable" });
            }
        }
        if (conn.open === false) throw new Error("connection closed during session initialization");
        checkAttempt();
    } catch (error) {
        if (ratchet) ratchet.consumer.close().finally(() => ratchet.core.close());
        session.close();
        conn.close();
        throw error;
    }
    stopRatchet();
    const previousSession = app.session;
    app.session = session;
    previousSession?.close();
    if (ratchet) {
        ratchet.publish();
        app.ratchet = ratchet;
    }
    app.ratchetError = ratchetError;
    // What an explicit guest certificate review needs (confirmGuestCertificateChange).
    app.ratchetContext = { session, fence, meta, auth };
    // One in-app explanation of the upgrade (shown once by the UI).
    emit("secure-messaging", { active: !!ratchet, reason: ratchetError?.code || "" });
    app.identityFence = fence;
    app.meta = meta;
    activated = true;
    subscribeMissing();
    reconnectAttempt = 0;
    setOnline(true);
    if (guest) {
        app.identity = identity;
        app.guestPassword = guest.serverPassword;
    }
    const rejoin = app.voice?.channelID || 0;
    const previousVoice = app.voice;
    previousVoice?.close();
    // Mute, deafen and the last whisperer carry over to the new session.
    app.voice = new Voice(session, previousVoice, { media: mediaAdapter(session) });
    watchVoice(app.voice);
    // A dropped connection ends the call; rejoin the same channel.
    if (rejoin) joinVoice(rejoin).catch(() => {});
    app.dm?.close();
    const dm = new DirectMessages(session, app.meta);
    dm.shareActivity = activityShared;
    // No legacy send for a signed-in session: without a consumer, sends fail
    // with the blocking reason.
    dm.secureRequired = () => app.ratchetError || Object.assign(new Error("secure messaging is not ready"), { code: "secure_unavailable" });
    if (ratchet) {
        // Attached before dm/groups are published, so no send can go legacy.
        ratchet.dm = dm;
        dm.attachRatchet(ratchet.consumer).catch(() => {});
    }
    app.dm = dm;
    dm.addEventListener("messages", event => {
        const { peer, message, isNew } = event.detail;
        if (isNew && !message.self && !message.copied) noteUnread(`dm:${peer}`, true, { kind: "dm", from: peer, name: message.fromNickname, text: message.body });
        emit("dm", { peer });
    });
    app.groups?.close();
    app.groups = new Groups(session, dm);
    app.groups.shareActivity = activityShared;
    app.groups.secureRequired = dm.secureRequired;
    if (ratchet) {
        ratchet.groups = app.groups;
        app.groups.attachRatchet(ratchet.consumer).catch(() => {});
        pollRatchet(ratchet, session);
        guardOwnRoot(ratchet, session);
    } else if (ratchetError && RATCHET_RETRYABLE.has(ratchetError.code)) {
        retryRatchet(session, fence, meta, auth, 0);
    }
    app.groups.addEventListener("incoming", event => noteUnread(`conversation:${event.detail.id}`, false, { kind: "group", name: app.groups?.list.get(event.detail.id)?.name }));
    app.groups.addEventListener("list", () => emit("groups"));
    app.groups.refresh().catch(() => {});
    app.calls?.close();
    app.calls = new Calls(session, dm);
    watchCalls(app.calls);
    watchCallLog(app.calls);
    syncMuted();
    dm.importHistory().catch(() => {});
    dm.ready.then(async () => {
        await showPending(session, dm, app.groups);
        await flushOutbox(session, dm, app.groups);
        await outboxChanged();
    }).catch(() => {});
    conn.addEventListener("close", () => {
        if (app.session !== session) return;
        stopRatchet();
        setOnline(false);
        scheduleReconnect();
    });
    session.addEventListener("device", event => {
        if (event.detail?.event === "revoked" && event.detail.device?.device_id === session.deviceID && app.session === session) forgetDevice(fence).catch(() => {});
        if (event.detail?.event === "read" && event.detail.read) readElsewhere(event.detail.read);
        if (event.detail?.event === "history_ready") dm.importHistory().catch(() => {});
    });
    // Until the rules are accepted the server refuses chat requests; redo them.
    session.addEventListener("rules", () => {
        if (session.pendingRules || app.session !== session) return;
        subscribeMissing();
        app.groups?.refresh().catch(() => {});
        dm.importHistory().catch(() => {});
        flushOutbox(session, dm, app.groups).then(outboxChanged).catch(() => {});
    });
    session.addEventListener("incoming", event => {
        const { scope, message } = event.detail;
        if (!message.own) noteUnread(`channel:${scope}`, message.mentioned || !!message.keyword, { kind: "channel", channelID: scope, from: message.fromUniqueID, name: message.from, text: message.text });
    });
    if (!app.meta.guest) reportVisibility(conn);
    if (!app.meta.guest) syncPush(conn).then(state => {
        app.push = state;
        emit("push", state);
    }).catch(() => {});
    emit("session", session);
    // Register every incoming handler before IndexedDB can yield to replayed DMs.
    try {
        session.activateIncoming();
        await updateMeta({ deviceID: session.deviceID || auth.device_id, uniqueID: auth.unique_id, nickname: auth.nickname, priorPrincipal: undefined }, fence);
        checkAttempt();
    } catch (error) {
        if (app.session === session) disconnect();
        else session.close();
        conn.close();
        throw error;
    }
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
    if (app.session?.conn.open && app.dm) flushOutbox(app.session, app.dm, app.groups).then(outboxChanged).catch(() => {});
}

/** Sends waiting messages, e.g. when the service worker asks. */
export async function flushNow() {
    if (!app.session?.conn.open || !app.dm) return;
    await flushOutbox(app.session, app.dm, app.groups);
    await outboxChanged();
}

/** Joins a voice channel (with its password, if any) and keeps the phone awake while connected. */
export async function joinVoice(channelID, password = "") {
    if (!app.voice) throw new Error("not connected");
    return app.voice.join(channelID, password);
}

/**
 * Leaves the server without forgetting this device, e.g. after declining
 * the rules. The Connect screen offers to sign in again.
 */
export function disconnect() {
    connectionGeneration++;
    clearTimeout(reconnectTimer);
    stopRatchet();
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
    try {
        const list = JSON.parse(localStorage.getItem(REMINDERS_KEY) || "[]");
        return Array.isArray(list) ? list.filter(r => r && typeof r.uid === "string" && typeof r.name === "string" && Number.isFinite(r.at)) : [];
    } catch { return []; }
}

function saveReminders(list) {
    try { localStorage.setItem(REMINDERS_KEY, JSON.stringify(list)); } catch { /* private window */ }
    emit("reminders");
}

let reminderTimer = 0;
let reminderGeneration = 0;

function clearReminders() {
    reminderGeneration++;
    clearTimeout(reminderTimer);
    for (const reminder of reminders()) closeNotifications(`remind:${reminder.uid}`);
    try { localStorage.removeItem(REMINDERS_KEY); } catch { /* private window */ }
    emit("reminders");
}

/** Schedules the next due reminder; due ones notify and stay on Home. */
function scheduleReminders() {
    const generation = ++reminderGeneration;
    clearTimeout(reminderTimer);
    const pending = reminders().filter(r => !r.shown);
    if (!pending.length) return;
    const next = Math.min(...pending.map(r => r.at));
    reminderTimer = setTimeout(async () => {
        const now = Date.now();
        const list = reminders();
        for (const reminder of list.filter(r => !r.shown && r.at <= now)) {
            reminder.shown = true;
            const registration = await navigator.serviceWorker?.getRegistration("/app/").catch(() => null);
            if (generation !== reminderGeneration) return;
            navigator.vibrate?.([200, 100, 200]);
            await registration?.showNotification(reminder.name, { body: reminder.body, tag: `remind:${reminder.uid}`, data: { url: `/app/#/dm/${encodeURIComponent(reminder.uid)}` } }).catch(() => {});
            if (generation !== reminderGeneration) {
                closeNotifications(`remind:${reminder.uid}`);
                return;
            }
        }
        if (generation !== reminderGeneration) return;
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
    for (const [tag, entry] of app.unread) if (!isMuted(tag)) total += entry.count;
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
    // Muted conversations (conversation-prefs.js) are not counted either.
    if (!decision.count || isMuted(tag)) return;
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
    // The chat screen places its "New messages" divider from this.
    if (app.unread.has(tag)) app.lastUnread = { tag, ...app.unread.get(tag) };
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
    app.reconnects++;
    clearTimeout(reconnectTimer);
    reconnectTimer = setTimeout(() => {
        if (!app.identity || app.paused) return;
        connect().catch(error => {
            // Revoked devices and servers that need an update never loop.
            if (!error.revoked && error.kind !== "serverOutdated") scheduleReconnect();
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

/**
 * Explicit secure-messaging recovery after restoring a backup or losing this
 * browser's message store (Settings). Resolves true, or rejects with a coded
 * error (not_needed, invalid_evidence, authority-required, unavailable...).
 */
export async function recoverSecureMessaging() {
    const owner = app.ratchet;
    if (!owner || owner.consumer.closed) throw Object.assign(new Error("secure messaging is not active"), { code: "unavailable" });
    return owner.consumer.recover("restored");
}

/**
 * Explicit exit from a recovery that stays pending. Resolves "completed" (the
 * server had applied it) or "cancelled" (it was not applied); rejects with
 * recovery_unknown when the server's record shows neither.
 */
export async function cancelSecureMessagingRecovery() {
    const owner = app.ratchet;
    if (!owner || owner.consumer.closed) throw Object.assign(new Error("secure messaging is not active"), { code: "unavailable" });
    return owner.consumer.cancelRecovery();
}
