// native-toast.js — (501) Windows Action Center notifications with quick
// reply and "Mark as read".
//
// A toast only carries a random token. This module remembers which
// notification destination (tab, signed-in identity, conversation) each token
// stands for, and on activation hands the destination to the same navigator
// the notification center uses: it revalidates the tab and identity before
// anything happens. Quick-reply text is then put through that conversation's
// normal composer, so it is encrypted and delivered exactly like typed text.
import { t } from "./i18n.js";
import { captureNotificationDestination } from "./notification-navigation.js";

const V = () => window.__noxa;
const App = () => window.go.main.App;

const MAX_TOKENS = 200;
const TOKEN_TTL_MS = 24 * 60 * 60 * 1000;
const issued = new Map(); // token -> { destination, conversation, at }
const shownConversations = new Set();
let available = null;

/** Conversation key of a destination: one Windows toast per key. */
export function conversationKey(destination) {
    const reference = destination?.reference;
    if (!reference) return "";
    const prefix = destination.tabID + "|";
    switch (reference.kind) {
    case "dm": return prefix + "dm:" + reference.peer_id;
    case "group": return prefix + "group:" + reference.group_id;
    case "channel": return prefix + "ch:" + reference.channel_id;
    case "thread": return prefix + "thread:" + reference.channel_id + ":" + reference.thread_id;
    default: return "";
    }
}

/** Splits "Sender: text" notification text into a title and body. */
export function toastText(event, text, previews) {
    if (!previews) return { title: "noXa", body: t("nativeToast.hidden") };
    const value = String(text || "");
    const split = value.indexOf(": ");
    if (split > 0 && split <= 64) return { title: value.slice(0, split), body: value.slice(split + 2) };
    return { title: "noXa", body: value };
}

function newToken() {
    const bytes = new Uint8Array(16);
    crypto.getRandomValues(bytes);
    return Array.from(bytes, byte => byte.toString(16).padStart(2, "0")).join("");
}

function remember(token, entry, now = Date.now()) {
    for (const [key, value] of issued) if (now - value.at >= TOKEN_TTL_MS) issued.delete(key);
    while (issued.size >= MAX_TOKENS) issued.delete(issued.keys().next().value);
    issued.set(token, { ...entry, at: now });
}

function take(token, now = Date.now()) {
    const entry = issued.get(token);
    issued.delete(token);
    return entry && now - entry.at < TOKEN_TTL_MS ? entry : null;
}

// Only a positive answer is cached: registration finishes asynchronously at
// startup, so an early "unavailable" must not disable toasts for the session.
function nativeAvailable() {
    if (available) return available;
    return Promise.resolve().then(() => App().NativeToastsAvailable()).then(Boolean, () => false).then(ok => {
        if (ok) available = Promise.resolve(true);
        return ok;
    });
}

function legacyNotify(event, text, previews) {
    App().Notify("noXa " + event, previews ? String(text).slice(0, 200) : t("nativeToast.hidden"));
}

// Conversations whose toast is being submitted, and those viewed meanwhile.
const pendingConversations = new Set();
const viewedWhilePending = new Set();

/**
 * Shows a native notification for event: a Windows toast when available and
 * enabled, otherwise the classic tray balloon. ctx is notify()'s context.
 */
export async function showNativeNotification(event, text, ctx = {}) {
    // Capture first: the active tab may change while availability is checked.
    const destination = captureNotificationDestination(event, ctx, V().state);
    const settings = V().state.settings || {};
    const previews = settings.native_notification_previews !== false;
    if (settings.windows_notifications === false || !await nativeAvailable()) {
        legacyNotify(event, text, previews);
        return false;
    }
    const conversation = conversationKey(destination) || "event:" + event;
    const replyable = !!destination && ["dm", "group", "channel"].includes(destination.reference.kind);
    const { title, body } = toastText(event, text, previews);
    const token = newToken();
    remember(token, { destination, conversation });
    pendingConversations.add(conversation);
    let error;
    try {
        error = await App().ShowNativeToast({
            token, conversation, title, body, replyable,
            reply_label: t("nativeToast.reply"), read_label: t("nativeToast.markRead"), placeholder: t("nativeToast.placeholder"),
        });
    } catch (err) { error = String(err); }
    pendingConversations.delete(conversation);
    if (error) {
        issued.delete(token);
        viewedWhilePending.delete(conversation);
        legacyNotify(event, text, previews);
        return false;
    }
    // The replacement is shown: tokens of the toast it replaced are retired.
    for (const [key, entry] of issued) if (key !== token && entry.conversation === conversation) issued.delete(key);
    shownConversations.add(conversation);
    if (viewedWhilePending.delete(conversation)) viewed(conversation);
    return true;
}

/**
 * The conversation with this key is on screen: remove its toast. Tokens stay
 * until used or expired, so an activation already on its way (a click that
 * also focused the window) still arrives; Go keeps each token single-use.
 */
export function viewed(conversation) {
    if (!conversation) return;
    if (pendingConversations.has(conversation)) viewedWhilePending.add(conversation);
    if (!shownConversations.delete(conversation)) return;
    Promise.resolve().then(() => App().ClearNativeToasts(conversation)).catch(() => {});
}

/** Removes every noXa toast (Windows notifications switched off). */
export function clearAll() {
    issued.clear();
    shownConversations.clear();
    Promise.resolve().then(() => App().ClearNativeToasts("")).catch(() => {});
}

/** Removes the toasts of an identity that is deleted or reset. */
export function clearIdentity(identityUID) {
    if (!identityUID) return;
    const conversations = new Set();
    for (const [token, entry] of issued) {
        if (entry.destination?.identityUID !== identityUID) continue;
        conversations.add(entry.conversation);
        issued.delete(token);
    }
    for (const conversation of conversations) {
        shownConversations.delete(conversation);
        Promise.resolve().then(() => App().ClearNativeToasts(conversation)).catch(() => {});
    }
}

function onCurrentAccount(destination) {
    const state = V().state;
    return destination.tabID === state.activeTabID && destination.identityUID === state.myUniqueID;
}

function chatKey(reference) {
    if (reference.kind === "dm") return "dm:" + reference.peer_id;
    if (reference.kind === "channel") return "ch:" + reference.channel_id;
    return "";
}

function conversationOnly(destination) {
    const reference = destination.reference;
    const plain = reference.kind === "dm" ? { kind: "dm", peer_id: reference.peer_id, client_message_id: "", local_seq: 0 }
        : { ...reference, message_id: 0 };
    return Object.freeze({ ...destination, reference: Object.freeze(plain) });
}

// waitFor polls check until it holds (the view switch after navigation can
// finish a little later than the navigator resolves) or the time runs out.
async function waitFor(check, timeoutMs = 3000, stepMs = 100) {
    for (let waited = 0; ; waited += stepMs) {
        if (check()) return true;
        if (waited >= timeoutMs) return false;
        await new Promise(resolve => setTimeout(resolve, stepMs));
    }
}

async function deliverReply(destination, reply) {
    const reference = destination.reference;
    if (reference.kind === "group") {
        if (!await waitFor(() => onCurrentAccount(destination) && window.__noxaGroups?.viewedConversationID?.() === reference.group_id)) return false;
        return await window.__noxaGroups?.quickReplyToPrivateGroup?.(reference.group_id, reply) || false;
    }
    const key = chatKey(reference);
    if (!key || !await waitFor(() => onCurrentAccount(destination) && window.__noxaChat?.activeConversationKey?.({ visible: false }) === key)) return false;
    // Revalidated right before delivery: a server switch meanwhile drops it.
    if (!onCurrentAccount(destination)) return false;
    return await window.__noxaChat?.quickReplyToActive?.(key, reply) || false;
}

async function markRead(destination) {
    const reference = destination.reference;
    if (onCurrentAccount(destination)) {
        if (reference.kind === "group") return await window.__noxaGroups?.markPrivateGroupRead?.(reference.group_id) || false;
        return window.__noxaChat?.markConversationSeen?.(reference) || false;
    }
    // Another server tab: only a private group's server-side read marker can
    // be advanced without that tab's stores, and only for the same identity.
    // Direct-message and channel unread state live in that tab's view and are
    // rebuilt when it is shown.
    if (reference.kind !== "group" || !(reference.message_id > 0)) return false;
    try {
        const owner = await App().DMHistoryContextForTab(destination.tabID);
        if (owner?.tab_id !== destination.tabID || owner.identity_uid !== destination.identityUID) return false;
        await App().ConversationForTab(destination.tabID, { action: "mark_read", id: reference.group_id, read_message_id: reference.message_id });
        return true;
    } catch {
        return false;
    }
}

/**
 * Handles a validated activation from Go. Unknown tokens (another run, used
 * already, or not issued here) and mismatching conversations do nothing.
 */
export async function handleActivation(activation = {}) {
    const entry = take(String(activation.token || ""));
    if (!entry || entry.conversation !== activation.conversation) return "ignored";
    shownConversations.delete(entry.conversation);
    const destination = entry.destination;
    if (!destination) return "ignored";
    const navigate = window.__noxaPolish?.navigateNotification;
    switch (activation.action) {
    case "open":
        return await navigate?.(destination) ? "opened" : "unavailable";
    case "read":
        return await markRead(destination) ? "read" : "unavailable";
    case "reply": {
        const reply = String(activation.reply || "").trim();
        if (!reply || !navigate) return "ignored";
        // A reply needs the conversation, not the message: a message that was
        // deleted or scrolled out of history must not stop the reply.
        const result = await navigate(conversationOnly(destination)) ? await deliverReply(destination, reply) : false;
        if (result === "draft") V().toast(t("nativeToast.replyDraft"), "info", "alert", { record: false });
        else if (result === "kept") V().toast(t("nativeToast.replyKept"), "warn", "alert", { record: false, bypassDND: true });
        else if (!result) V().toast(t("nativeToast.replyFailed"), "warn", "alert", { record: false, bypassDND: true });
        return result || "unavailable";
    }
    default:
        return "ignored";
    }
}

// The conversation currently on screen (and visible), as a toast key suffix.
function visibleConversation() {
    const thread = window.__noxaDiscussions?.viewedDiscussionThread?.();
    if (thread) return "thread:" + thread.channelID + ":" + thread.threadID;
    const group = window.__noxaGroups?.viewedConversationID?.();
    if (group) return "group:" + group;
    return window.__noxaChat?.activeConversationKey?.() || "";
}

/** Subscribes to activations and exposes the hooks other modules call. */
export function initNativeToasts() {
    window.__noxaNativeToast = { viewed, clearAll, clearIdentity };
    window.runtime?.EventsOn?.("native_toast_activation", activation => { void handleActivation(activation); });
    // Switching Windows notifications off takes effect once it is saved.
    window.runtime?.EventsOn?.("settings_update", settings => {
        if (settings?.windows_notifications === false && (shownConversations.size || issued.size)) clearAll();
    });
    // Returning to the window clears the toast of the conversation in view.
    window.addEventListener("focus", () => {
        const tabID = V()?.state?.activeTabID;
        const key = tabID && visibleConversation();
        if (key) viewed(tabID + "|" + key);
    });
}

// Test hooks.
export const __test = {
    issued, shownConversations, remember, take,
    reset: () => { issued.clear(); shownConversations.clear(); pendingConversations.clear(); viewedWhilePending.clear(); available = null; },
};
