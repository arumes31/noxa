import test from "node:test";
import assert from "node:assert/strict";

// Minimal globals for native-toast.js: the Wails bindings, runtime events and
// the app state the notification destination is captured from.
const calls = [];
const toasts = [];
const listeners = new Map();
globalThis.window = globalThis;
window.addEventListener = (name, callback) => listeners.set(name, callback);
window.runtime = { EventsOn: (name, callback) => listeners.set(name, callback) };
let available = true;
let showError = "";
window.go = { main: { App: {
    NativeToastsAvailable: async () => available,
    ShowNativeToast: async req => { calls.push(["show", req]); return showError; },
    ClearNativeToasts: async key => { calls.push(["clear", key]); },
    Notify: async (title, text) => { calls.push(["notify", title, text]); },
} } };
window.__noxa = {
    state: { activeTabID: "tab-a", myUniqueID: "me", serverGeneration: 1, lastConnect: { addr: "a.test" }, settings: {} },
    toast: (text, kind) => toasts.push([kind, text]),
};

const mod = await import("../src/native-toast.js");
const { showNativeNotification, handleActivation, conversationKey, toastText, viewed, clearAll, initNativeToasts, __test } = mod;

const dmCtx = { uid: "peer", reference: { kind: "dm", peer_id: "peer", client_message_id: "m1", local_seq: 0 } };
const reset = () => { calls.length = 0; toasts.length = 0; __test.reset(); available = true; showError = ""; window.__noxa.state.settings = {}; };

test("a DM becomes a replyable toast keyed by tab and conversation, with a random token", async () => {
    reset();
    assert.equal(await showNativeNotification("dm", "Alice: see you at 8", dmCtx), true);
    const [[kind, req]] = calls;
    assert.equal(kind, "show");
    assert.match(req.token, /^[0-9a-f]{32}$/);
    assert.equal(req.conversation, "tab-a|dm:peer");
    assert.equal(req.title, "Alice");
    assert.equal(req.body, "see you at 8");
    assert.equal(req.replyable, true);
    assert.ok(req.reply_label && req.read_label && req.placeholder);
    await showNativeNotification("dm", "Alice: again", dmCtx);
    assert.notEqual(calls[1][1].token, req.token, "every toast gets a fresh token");
});

test("hidden previews show neither sender nor text; Reply still works", async () => {
    reset();
    window.__noxa.state.settings = { native_notification_previews: false };
    await showNativeNotification("dm", "Alice: secret plans", dmCtx);
    const req = calls[0][1];
    assert.equal(req.title, "noXa");
    assert.ok(!JSON.stringify(req).includes("secret") && !JSON.stringify(req).includes("Alice"));
    assert.equal(req.replyable, true);
});

test("switched off, unavailable or failing toasts fall back to the tray balloon", async () => {
    reset();
    window.__noxa.state.settings = { windows_notifications: false };
    assert.equal(await showNativeNotification("dm", "Alice: hi", dmCtx), false);
    assert.deepEqual(calls, [["notify", "noXa dm", "Alice: hi"]]);
    reset();
    available = false;
    await showNativeNotification("dm", "Alice: hi", dmCtx);
    assert.equal(calls[0][0], "notify");
    reset();
    showError = "native notification failed";
    window.__noxa.state.settings = { native_notification_previews: false };
    await showNativeNotification("dm", "Alice: secret", dmCtx);
    assert.equal(calls[1][0], "notify");
    assert.ok(!calls[1][2].includes("secret"), "the fallback keeps previews hidden too");
    assert.equal(__test.issued.size, 0, "a failed toast leaves no usable token");
});

test("threads and destination-less events cannot be replied to", async () => {
    reset();
    await showNativeNotification("channel_message", "thread news", { reference: { kind: "thread", channel_id: 3, thread_id: 9, message_id: 1 } });
    await showNativeNotification("poke", "you were poked", {});
    assert.equal(calls[0][1].replyable, false);
    assert.equal(calls[0][1].conversation, "tab-a|thread:3:9");
    assert.equal(calls[1][1].replyable, false);
    assert.equal(calls[1][1].conversation, "event:poke");
});

test("conversation keys and toast text", () => {
    assert.equal(conversationKey({ tabID: "t", reference: { kind: "group", group_id: "g1" } }), "t|group:g1");
    assert.equal(conversationKey({ tabID: "t", reference: { kind: "channel", channel_id: 0 } }), "t|ch:0");
    assert.equal(conversationKey(null), "");
    assert.deepEqual(toastText("dm", "no separator here", true), { title: "noXa", body: "no separator here" });
});

async function issueDM() {
    calls.length = 0;
    await showNativeNotification("dm", "Alice: hi", dmCtx);
    return calls.find(call => call[0] === "show")[1];
}

test("reply navigates first, then uses the conversation's own composer", async () => {
    reset();
    const order = [];
    window.__noxaPolish = { navigateNotification: async destination => { order.push(["navigate", destination.tabID, destination.reference.peer_id]); return true; } };
    window.__noxaChat = { quickReplyToActive: (key, text) => { order.push(["reply", key, text]); return "sent"; } };
    const req = await issueDM();
    assert.equal(await handleActivation({ token: req.token, action: "reply", conversation: req.conversation, reply: "  on my way " }), "sent");
    assert.deepEqual(order, [["navigate", "tab-a", "peer"], ["reply", "dm:peer", "on my way"]]);
    assert.equal(await handleActivation({ token: req.token, action: "reply", conversation: req.conversation, reply: "again" }), "ignored", "tokens act once");
});

test("a draft is never overwritten and a vanished conversation reports the reply as unsent", async () => {
    reset();
    window.__noxaPolish = { navigateNotification: async () => true };
    window.__noxaChat = { quickReplyToActive: () => "draft" };
    let req = await issueDM();
    assert.equal(await handleActivation({ token: req.token, action: "reply", conversation: req.conversation, reply: "x" }), "draft");
    assert.equal(toasts.at(-1)[0], "info");
    window.__noxaPolish = { navigateNotification: async () => false };
    req = await issueDM();
    assert.equal(await handleActivation({ token: req.token, action: "reply", conversation: req.conversation, reply: "x" }), "unavailable");
    assert.equal(toasts.at(-1)[0], "warn");
});

test("forged, mismatched and unknown activations do nothing", async () => {
    reset();
    let navigated = 0;
    window.__noxaPolish = { navigateNotification: async () => { navigated++; return true; } };
    const req = await issueDM();
    assert.equal(await handleActivation({ token: "f".repeat(32), action: "open", conversation: req.conversation }), "ignored");
    assert.equal(await handleActivation({ token: req.token, action: "open", conversation: "tab-b|dm:peer" }), "ignored");
    assert.equal(navigated, 0);
    const next = await issueDM();
    assert.equal(await handleActivation({ token: next.token, action: "delete", conversation: next.conversation }), "ignored");
    assert.equal(await handleActivation({}), "ignored");
});

test("mark as read only touches the account on screen and sends no receipt", async () => {
    reset();
    const seen = [];
    window.__noxaChat = { markConversationSeen: reference => { seen.push(reference.peer_id); return true; } };
    let req = await issueDM();
    assert.equal(await handleActivation({ token: req.token, action: "read", conversation: req.conversation }), "read");
    req = await issueDM();
    window.__noxa.state.activeTabID = "tab-b";
    assert.equal(await handleActivation({ token: req.token, action: "read", conversation: req.conversation }), "unavailable");
    window.__noxa.state.activeTabID = "tab-a";
    assert.deepEqual(seen, ["peer"]);
    window.__noxaGroups = { markPrivateGroupRead: async id => { seen.push("group:" + id); return true; } };
    calls.length = 0;
    await showNativeNotification("dm", "Group: new", { reference: { kind: "group", group_id: "g1", message_id: 4 } });
    req = calls[0][1];
    assert.equal(await handleActivation({ token: req.token, action: "read", conversation: req.conversation }), "read");
    assert.deepEqual(seen, ["peer", "group:g1"]);
});

test("viewing a conversation removes its toast once; switching off removes all", async () => {
    reset();
    const req = await issueDM();
    calls.length = 0;
    viewed(req.conversation);
    viewed(req.conversation);
    viewed("tab-a|dm:never-shown");
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.deepEqual(calls, [["clear", "tab-a|dm:peer"]]);
    assert.equal(__test.issued.size, 0);
    clearAll();
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.deepEqual(calls.at(-1), ["clear", ""]);
});

test("init subscribes to activations and clears the viewed conversation on focus", async () => {
    reset();
    initNativeToasts();
    assert.equal(typeof listeners.get("native_toast_activation"), "function");
    assert.equal(typeof window.__noxaNativeToast.viewed, "function");
    window.__noxaGroups = { viewedConversationID: () => "" };
    window.__noxaChat = { activeConversationKey: () => "dm:peer" };
    await issueDM();
    calls.length = 0;
    listeners.get("focus")();
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.deepEqual(calls, [["clear", "tab-a|dm:peer"]]);
});
