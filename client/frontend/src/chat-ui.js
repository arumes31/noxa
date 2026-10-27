import { previewConversation, previewConversationFor, setPreviewConversation } from "./preview-privacy.js";
import { createActivityHistory } from "./activity-history.js";
import { blockedPreviewPlaceholder, resolvePreviewImage } from "./preview-source-policy.js";
// chat-ui.js — wave-5b rich chat UI: markdown messages (91-93), link preview
// cards (94), emoji picker + custom server emoji (95/96), reactions (97), file
// paste/drop/upload + inline rendering (98-100), edit/delete (101/102),
// history scrollback (103), unread badges (104), mentions (105/106), reply-to
// (107), thread panels (108), pins (109), search (110), topic/description
// header (111-113), typing indicators (120), read state (121), PM tabs +
// offline badge + receipts (122-124), export (125), display prefs (126-129),
// join/leave collapsing (131), announcement banner (132), MOTD (133), scroll
// lock (134) and the Ctrl+K quick switcher (135).
//
// Replies and direct recipients use explicit protocol identities.
import { resolveParent, directPeer } from "./chat-relations.js";
import { fileCardText, previewLine } from "./dm-model.js";
import { manageChatGIFPlayback } from "./chat-gif-playback.js";
import { openChatMediaLightbox } from "./chat-media-lightbox.js";
import { createChatUploadQueue } from "./chat-upload-queue.js";
import { createUnreadTabEffects } from "./chat-unread.js";
import { createChatTabs } from "./chat-tabs.js";
import { voiceMessageButton, renderVoiceMessage } from "./voice-messages.js";
import { initMessageTools, saveMessageReference, openHistorySearch } from "./message-tools.js";
import { roleMentionChoices, roleMentionLabel, mentionFlags } from "./role-mentions.js";
import { memberTarget } from "./member-target.js";
import { mountContextMenu, closeContextMenu, contextMenuKey } from "./context-menu.js";
import { createPoll, renderPoll } from "./polls.js";
import { parsePoll } from "./poll-state.js";
import { closeDiscussions, initDiscussions, openDiscussions, renderForumChannel, resetDiscussions } from "./discussions.js";
import { closeConversations, initConversations, isPrivateGroupActive, listedPrivateGroups, openConversationsAt, resetConversations, viewedConversationID } from "./conversations.js";
import { mergeReadMarker } from "./read-markers.js";
import { QUICK_SWITCH_SETTINGS } from "./shell-model.js";
import { createChatDelivery } from "./chat-delivery.js";
import { sessionUserID } from "./session-identity.js";
import { RATCHET_PROTOCOL, ratchetDMMessage, ratchetIsIncoming, sameDMMessage } from "./ratchet-projection.js";
import { DOWNGRADED, encModeFrom, encryptionBadge, UNOPENED_BODIES } from "./message-encryption.js";
import { chatEnglish } from "./chat-messages.js";
import { renderMarkdown, escapeHTML, EMOJI } from "./markdown.js";
import { pickIcon } from "./image-tools.js";
import { closeDialog, confirmDialog, isCurrentServerDialog, mountServerDialog, promptDialog } from "./modal.js";
import { imageDataURL } from "./safe-media.js";
import { parseRuntimeObject } from "./runtime-json.js";
import { parseFileRef, transformCustomEmoji } from "./chat-parsers.js";
import { captureScope, scopeIsCurrent } from "./scoped-actions.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { copyToClipboard } from "./clipboard.js";
import { avatarColor, renderWorkspace } from "./workspace-ui.js";
import { youtubeVideo, renderYouTubePreviews } from "./youtube-previews.js";
import { xPost, renderXPostPreviews } from "./x-previews.js";
import { videoPortal, renderVideoPortalPreviews } from "./video-portal-previews.js";
import { createSearchWorkspace } from "./search-workspace.js";
import { conceptScore } from "./search-synonyms.js";
import "./unified-search.css";
import { buildMemberCandidates, openMemberPicker } from "./member-picker.js";
import { channelPath } from "./ui-format.js";

const V = () => window.__noxa;
const $ = (id) => document.getElementById(id);

let composerLimit = null;
function updateComposerCount() {
    const input = $("chat-text"), counter = $("chat-length");
    if (!input || !counter) return;
    const text = input.value.trim();
    const bytes = new TextEncoder().encode(text).length;
    const limit = composerLimit && chatServerIsCurrent(composerLimit.scope) && $("chat-scope").value !== "direct" ? composerLimit.bytes : 0;
    counter.hidden = text.length === 0;
    counter.textContent = t(limit > 0 ? "wins.messageLimit" : "wins.characters", { count: [...text].length, bytes, limit });
    counter.title = t("wins.limitNote");
    counter.classList.toggle("warn", limit > 0 && bytes >= limit * 0.9);
}
const app = () => window.go.main.App;

const PAGE = 50; // history page size (103)

// ---------------------------------------------------------------------------
// Module state
// ---------------------------------------------------------------------------

// view: {kind:"channel"} (follows state.myChannelID) | {kind:"global"} |
// {kind:"dm", uid} | {kind:"chan", id} (a subscribed channel I am not in,
// 312/311). store key: "ch:<id>" ("ch:0" = global) or "dm:<uid>".
let view = { kind: "channel" };
// subscriptions is the server's authoritative set (312): every id it lists,
// including the channel I stand in, which the server adds implicitly. It is
// replaced wholesale, never merged, so the client cannot accumulate drift.
let subscriptions = [];
const store = new Map(); // key -> {msgs, hasMore, end, loading, loaded}
const unread = new Map(); // channelID -> {n, mention, unreadAt}
let unreadEffects = null;
let chatTabs = null;
let tabLayoutOwner = null;
const pmTabs = new Map(); // uid -> {uid, nick, unread, unreadAt, offline, pendingRead}
// Private groups appear in the same tab strip as DMs. Unread counts come from
// the server's per-member read state, so a tab is never double counted when a
// live event and a list refresh both report the same message.
const groupTabs = new Map(); // groupID -> {id, name, unread, unreadAt, latest}
const closedGroupTabs = new Map(); // tabID:groupID -> newest message id when closed
const GROUP_TAB = "grp:";
const chanTabs = new Map(); // channelID -> {id}; derived from SubscriptionState
let pendingChannelTab = 0; // activate only after the server confirms subscription
let channelTabRequest = 0;
const receipts = new Map(); // clientMsgID -> "delivered" | "read"
const myReactions = new Map(); // messageID -> Set(emoji) I toggled this session.
// NOTE (97): history only carries reaction COUNTS, not who reacted, so the
// own-reaction highlight only knows about toggles from this session.

let atBottom = true;
let newCount = 0;
let replyTo = null; // message object being replied to (107)
let replyRevision = 0;
let draftRevision = 0;
let chatDelivery = null;
let activeSend = null;
let attachmentQueue = null;
let pendingFiles = []; // {name, dataBase64, isImage, dataURL} staged for send (98/100)
let emojiPanel = null; // open emoji panel element (95)
let pinsPanel = null; // open pins panel element (109)
let pinsRequest = 0;
let searchQ = ""; // live search filter (110)
let chatViewGeneration = 0;
let activeSearch = null;
let historySearchTimer = null;
let searchResultsPanel = null;
let searchChannel = "current";
let linkPreviewPreference;
let activeExport = null;
let exportAttempt = 0;
let threadPanel = null; // open thread panel element (108)
let threadRootID = 0; // root message id the thread panel is showing (108)

// Pinned message ids per store key (109). Loaded once per scope alongside
// history so the 📌 hover button can actually toggle instead of only pinning.
const pinnedIDs = new Map(); // store key -> Set(messageID)
const pendingPinChanges = new Map(); // per-ID events received during an initial pin read

// Typing indicators (120): store key -> Map(uniqueID -> {nick, expires}).
const typers = new Map();
let typingTimer = null; // single expiry sweep for every scope
let typingSentAt = 0; // last outgoing ping (throttle)
let typingScope = ""; // scope of that ping, so a scope switch re-pings at once
let typingIdle = null; // idle timer that ends the local composing session
let typingDebounce = null;
const sentHistory = [];
let sentHistoryPos = 0;
let sentHistoryDraft = "";

// Custom server emoji (95): list + data-URL cache.
let customEmoji = [];
let customDirty = true;
const emojiURLs = new Map(); // name -> dataURL | "" (failed)
const builtInEmoji = [...new Set(Object.values(EMOJI))];
const EMOJI_CATS = [
    ["chat.faces", builtInEmoji.slice(0, 30)],
    ["chat.reactions", builtInEmoji.slice(30, 49)],
    ["chat.objectsSymbols", builtInEmoji.slice(49)],
];

// Offline-DM toast batching (123).
const offlineBatch = new Map(); // uid -> {n, nick, timer}
const reconnectAnnouncementBatches = new Map(); // scope key -> {n, names, event, ctx, timer}
let reconnectAnnouncementUntil = 0;

// Local DM history (122). The sealed on-disk log is a DM's ONLY history — the
// server is E2EE-blind and stores nothing — so a peer's log is replayed into
// its store once per session, and a write failure is reported once rather than
// per message.
let dmPersistWarned = false;
let dmGeneration = 0;
let dmRestoreAttempt = null;
let dmOwner = null;
let dmIdentityRevision = 0n;
const closedDMPeers = new Set();
// Only explicit closes belong here; server removals never become undo actions.
const recentlyClosedChats = new Map();
const tabSubscriptionChanges = new Map();

// Restore a small recent working set in addition to explicitly pinned peers.
const DM_RESTORE_TABS = 6;

// Persist debounce for settings writes (last-read, dismissed announcement).
let persistTimer = null;

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

function getStore(key) {
    if (!store.has(key)) {
        // truncated (103): the server capped a page's key bundle, so some rows
        // in it stayed sealed for a reason that is transient, not permanent.
        store.set(key, { msgs: [], hasMore: true, end: false, loading: false, loaded: false, truncated: false });
    }
    return store.get(key);
}

function activeKey() {
    const st = V().state;
    if (view.kind === "global") return "ch:0";
    if (view.kind === "dm") return "dm:" + view.uid;
    if (view.kind === "chan") return "ch:" + view.id;
    return "ch:" + (st.myChannelID || 0);
}

// activeChannelID returns the channel the active view reads, or null for the
// global and DM views.
export function rememberDisconnectedChannel() {
    if (view.kind === "channel" && V().state.myChannelID > 0) view = { kind: "chan", id: V().state.myChannelID };
}
export function activeChannelID() {
    if (view.kind === "chan") return view.id;
    if (view.kind === "channel") return V().state.myChannelID || null;
    return null;
}

// Export may open two dialogs before it talks to the backend. Preserve the
// exact scope selected at the first click so a channel move cannot export a
// different conversation than the one the user confirmed.
function readExportScope() {
    const exportView = view;
    return {
        generation: V().state.serverGeneration,
        tabID: V().state.activeTabID,
        viewGeneration: chatViewGeneration,
        kind: exportView.kind,
        channelID: exportView.kind === "global" ? 0 : (activeChannelID() || 0),
        uid: exportView.kind === "dm" ? exportView.uid : "",
    };
}

function exportScopeIsCurrent(scope) {
    return scopeIsCurrent(scope, readExportScope);
}

function readChatServerScope() {
    return { generation: V().state.serverGeneration, tabID: V().state.activeTabID };
}

function chatServerIsCurrent(scope) {
    return scopeIsCurrent(scope, readChatServerScope);
}

function readDMHistoryScope() {
    return { ...readChatServerScope(), dmGeneration };
}

function dmHistoryScopeIsCurrent(scope) {
    return scopeIsCurrent(scope, readDMHistoryScope);
}

function dmRevision(value) {
    if (typeof value !== "string" || !/^(0|[1-9]\d{0,19})$/.test(value)) return null;
    const revision = BigInt(value);
    return revision <= 18446744073709551615n ? revision : null;
}

function dmOwnerIsCurrent(owner) {
    return owner === dmOwner && dmHistoryScopeIsCurrent(owner.scope);
}

function getDMOwner() {
    if (dmOwner && dmOwnerIsCurrent(dmOwner)) return dmOwner;
    const owner = { scope: captureScope(readDMHistoryScope), revision: dmIdentityRevision, context: null };
    dmOwner = owner;
    // Handle acquisition failure immediately, even if no conversation is open.
    owner.ready = (async () => {
        try {
            const context = await app().DMHistoryContextForTab(owner.scope.tabID || "");
            if (!dmOwnerIsCurrent(owner)) throw new Error("DM history owner changed");
            const revision = dmRevision(context?.identity_revision);
            if (!context || context.tab_id !== (owner.scope.tabID || "") || typeof context.identity_uid !== "string" || !context.identity_uid ||
                dmRevision(context.activation) === null || revision === null) throw new Error("DM history context unavailable");
            if (revision !== owner.revision) {
                // A getter can enter native code after a switch but before its
                // notification. Start a new owner; never bless its old actions.
                if (revision > dmIdentityRevision) onDMIdentityChanged(context.identity_revision);
                throw new Error("DM history owner changed");
            }
            owner.context = Object.freeze({ ...context });
            V().state.myUniqueID = context.identity_uid;
            return { context: owner.context };
        } catch (error) {
            owner.failed = true;
            return { error };
        }
    })();
    return owner;
}

function callDMHistory(owner, method, ...args) {
    const invoke = context => {
        if (!dmOwnerIsCurrent(owner)) throw new Error("DM history owner changed");
        return app()[method](context, ...args);
    };
    if (owner.context) {
        try { return Promise.resolve(invoke(owner.context)); } catch (error) { return Promise.reject(error); }
    }
    return owner.ready.then(result => {
        if (result.error) throw result.error;
        return invoke(result.context);
    });
}

function resetDMOwner() {
    selectDirectRecipient("");
    dmGeneration++;
    dmOwner = null;
    tabLayoutOwner = null;
    dmRestoreAttempt = null;
    dmPersistWarned = false;
    closedDMPeers.clear();
    recentlyClosedChats.clear();
    for (const request of tabSubscriptionChanges.values()) request.finish(false);
}

function onDMIdentityChanged(value) {
    const revision = dmRevision(value);
    if (revision === null || revision <= dmIdentityRevision) return;
    dmIdentityRevision = revision;
    resetDMHistoryView();
}

function resetDMHistoryView() {
    resetDMOwner();
    pmTabs.clear();
    for (const key of store.keys()) if (key.startsWith("dm:")) store.delete(key);
    for (const key of typers.keys()) if (key.startsWith("dm:")) typers.delete(key);
    for (const batch of offlineBatch.values()) if (batch.timer) clearTimeout(batch.timer);
    offlineBatch.clear();
    receipts.clear();
    closeQS();
    if (view.kind === "dm") {
        $("chat-scope").value = "channel";
        V().setDirectTargetVisible(false);
        resetNewCount();
        setView({ kind: "channel" });
    } else renderTabs();
    void restorePMTabs();
}

function cancelChatSearch() {
    clearTimeout(historySearchTimer);
    historySearchTimer = null;
    activeSearch?.unsubscribe?.();
    activeSearch = null;
    const button = $("chat-search-server");
    if (button) { button.disabled = false; button.textContent = t("messages.run"); }
    if (searchResultsPanel) closeDialog(searchResultsPanel);
}

function invalidateChatViewWork() {
    closeLinkCard();
    $("chat-send-error").classList.add("hidden");
    chatViewGeneration++;
    exportAttempt++;
    cancelChatSearch();
    activeExport?.progress.close();
    closePinsPanel();
    closeReactStrip();
    activeSend = null;
    $("chat-send").disabled = false;
    pendingFiles = [];
    attachmentQueue?.reset();
    renderFilePreview();
    clearReply();
    resetTypingOut();
}

function scanProgressCount(data, requestID) {
    const progress = parseRuntimeObject(data);
    return progress?.request_id === requestID && Number.isSafeInteger(progress.scanned) && progress.scanned >= 0
        ? progress.scanned : null;
}

function readAttachmentSaveScope(channelID) {
    return {
        generation: V().state.serverGeneration,
        tabID: V().state.activeTabID,
        viewGeneration: chatViewGeneration,
        channelID,
        viewKey: activeKey(),
    };
}

function attachmentSaveScopeIsCurrent(scope) {
    return scopeIsCurrent(scope, () => readAttachmentSaveScope(scope.channelID));
}

// Inline attachment previews are scoped to the same immutable chat identity
// as native saves. A delayed encrypted download must never populate a view the
// user has since left (or a freshly connected server using the same channel).
function readInlineAttachmentScope(channelID) {
    return readAttachmentSaveScope(channelID);
}

function inlineAttachmentScopeIsCurrent(scope, wrap) {
    return wrap.isConnected && scopeIsCurrent(scope, () => readInlineAttachmentScope(scope.channelID));
}

function chatSurfaceVisible(key) {
    const workspace = document.getElementById("app");
    const chatTab = document.getElementById("tab-chat");
    const chatPane = document.getElementById("chat-pane");
    return !isPrivateGroupActive() && key === activeKey() && document.visibilityState === "visible" && document.hasFocus() &&
        workspace && !workspace.classList.contains("hidden") && workspace.getAttribute("aria-hidden") !== "true" &&
        chatTab?.getAttribute("aria-selected") === "true" && !chatPane?.hidden;
}

function chatAnnouncementAllowed(event, ctx, key) {
    if (!chatSurfaceVisible(key)) return false;
    return window.__noxaNotify?.notificationOutputAllowed?.(event, ctx, "toast") ?? true;
}

function chatAnnouncementText(message) {
    return `${message.from || "Someone"}: ${String(message.text || "message").slice(0, 160)}`;
}

export function beginReconnectAnnouncementBatch(durationMs = 12000) {
    reconnectAnnouncementUntil = Math.max(reconnectAnnouncementUntil, Date.now() + Math.max(0, durationMs));
}

export function cancelReconnectAnnouncementBatch() {
    reconnectAnnouncementUntil = 0;
    for (const batch of reconnectAnnouncementBatches.values()) {
        if (batch.timer) clearTimeout(batch.timer);
    }
    reconnectAnnouncementBatches.clear();
}

function queueReconnectAnnouncement(key, message, event, ctx) {
    if (Date.now() >= reconnectAnnouncementUntil) return false;
    const batch = reconnectAnnouncementBatches.get(key) || {
        n: 0, names: new Set(), event, ctx, timer: null,
    };
    batch.n++;
    if (message.from) batch.names.add(message.from);
    batch.event = event;
    batch.ctx = ctx;
    if (batch.timer) clearTimeout(batch.timer);
    batch.timer = setTimeout(() => {
        reconnectAnnouncementBatches.delete(key);
        if (!chatAnnouncementAllowed(batch.event, batch.ctx, key)) return;
        const from = batch.names.size === 1 ? ` from ${[...batch.names][0]}` : "";
        V().announceLive(`${batch.n} message${batch.n === 1 ? "" : "s"}${from} received after reconnect`);
    }, 700);
    reconnectAnnouncementBatches.set(key, batch);
    return true;
}

function eligibleChatAnnouncement(message, key, event, ctx, allowed) {
    if (message.self || message.offline || !allowed) return "";
    if (queueReconnectAnnouncement(key, message, event, ctx)) return "";
    return chatAnnouncementText(message);
}

// channelName resolves a channel id to its tree name, falling back to the id
// so a tab for a channel the snapshot has not caught up with still reads.
function channelName(id) {
    const ch = V().state.channels.find((c) => c.ChannelID === id);
    return ch ? ch.Name : String(id);
}

function subscribed(channelID) {
    return subscriptions.includes(Number(channelID));
}

// normalize converts a live ChatBroadcast or a ChatHistoryEntry into the
// message shape the renderer uses.
function normalize(d, chID) {
    let ts = Date.now();
    if (typeof d.sent_at === "number") ts = d.sent_at < 1e12 ? d.sent_at * 1000 : d.sent_at;
    else if (typeof d.sent_at === "string" && d.sent_at) ts = Date.parse(d.sent_at) || ts;
    const channelID = chID !== undefined ? chID
        : (d.channel_id !== undefined && d.channel_id !== null && d.channel_id !== "" ? Number(d.channel_id) : null);
    return {
        id: Number(d.id) || 0,
        from: d.from ?? d.from_nickname ?? "?",
        fromUID: d.from_unique_id || "",
        text: d.text ?? d.body ?? "",
        ts,
        edited: !!(d.edited_at || d.edited),
        deleted: !!d.deleted,
        reactions: d.reactions || null,
        mentions: d.mentions || [],
        role_mentions: d.role_mentions || [],
        e2e: !!d.e2e,
        direct: !!d.direct || !!d.e2e,
        // enc_verified is set by the Go layer after it opened the body itself:
        // without it a history message would draw no badge at all and look
        // exactly like one the server handed over in the clear (91-135).
        enc: !!d.enc || !!d.enc_verified,
        encVerified: !!d.enc_verified,
        encMode: encModeFrom(!!d.enc_verified, d.enc_mode),
        offline: !!d.offline,
        clientMsgID: d.client_msg_id || "",
        replyToID: Number(d.reply_to_id) || 0,
        version: Number(d.version) || 1,
        channelID,
        self: false,
        mentioned: false,
    };
}

// attribute fills the two flags the live path derives from the broadcast but
// a history entry cannot: it carries no mentions list and no "is this mine".
// Without this, own messages replayed from history lose edit/delete and every
// mention of me loses its highlight the moment it is reloaded (106).
function attribute(m) {
    const st = V().state;
    m.self = m.fromUID ? m.fromUID === sessionUserID(st) : m.from === st.myNickname;
    m.mentioned = !m.self && !m.deleted && mentionsMe(m.text);
    return m;
}

function findMsg(id) {
    for (const st of store.values()) {
        const m = st.msgs.find((x) => x.id === Number(id));
        if (m) return m;
    }
    return null;
}

function fmtTime(ts) {
    const mode = V().state.settings?.chat_timestamps || "absolute";
    if (mode === "off") return "";
    const d = new Date(ts);
    if (mode === "relative") {
        const s = Math.max(0, Math.floor((Date.now() - ts) / 1000));
        if (s < 60) return "now";
        if (s < 3600) return Math.floor(s / 60) + "m";
        if (s < 86400) return Math.floor(s / 3600) + "h";
        return Math.floor(s / 86400) + "d";
    }
    const hm = String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0");
    if (d.toDateString() === new Date().toDateString()) return hm;
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")} ${hm}`;
}

function fmtFull(ts) {
    const d = new Date(ts);
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")} ` +
        `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

// persistSettings saves state.settings (debounced) after silent mutations
// like last-read pointers (121) or a dismissed announcement (132).
function persistSettings() {
    if (persistTimer) clearTimeout(persistTimer);
    persistTimer = setTimeout(() => {
        persistTimer = null;
        const s = V().state.settings;
        if (s) app().SaveSettings(s);
    }, 1200);
}

function hashStr(s) {
    let h = 5381;
    for (let i = 0; i < s.length; i++) h = ((h << 5) + h + s.charCodeAt(i)) >>> 0;
    return h.toString(16);
}

// ---------------------------------------------------------------------------
// Mentions (105/106)
// ---------------------------------------------------------------------------

// MENTION_ALL_FORMS are the mass-mention keywords. @channel is listed with
// @here/@everyone because it reaches every member of the scope exactly like
// they do, so the same gate applies to all three.
const MENTION_ALL_FORMS = ["channel", "here", "everyone"];

// The server evaluates the role capability when the message is sent. The
// completer stays discoverable because snapshots intentionally do not expose
// the caller's full effective capability set.
function canMentionAll() {
    return true;
}

function reEscape(s) {
    return s.replace(/[.*+?^${}()|[\]\\-]/g, "\\$&");
}

// mentionRe builds the token matcher for my own nickname plus the mass forms.
// It is anchored on a non-word character so "@dan" does not fire on
// "@daniela", which is exactly the substring bug the server side has (105).
function mentionRe() {
    const nick = V().state.myNickname || "";
    const alts = MENTION_ALL_FORMS.slice();
    if (nick) alts.unshift(reEscape(nick));
    return new RegExp("@(" + alts.join("|") + ")(?![\\w-])", "gi");
}

// mentionsMe reports whether a body mentions me. History entries carry no
// mentions list (the server resolves mentions only for the live broadcast),
// so a replayed message is matched locally instead of losing its highlight
// (106). A mass form counts here even if the sender turned out to lack the
// permission — the server dropped it from the live event, and this is only a
// local accent.
function mentionsMe(text) {
    if (!text) return false;
    return mentionRe().test(text);
}

// markMentions wraps @tokens in the rendered body. It walks text nodes only,
// so it can never introduce markup into user text.
function markMentions(container) {
    const members = V().state.clients || [];
    const names = [...new Set(members.map(c => c.nickname).filter(Boolean))].sort((a, b) => b.length - a.length);
    const memberPattern = names.length ? "|@(?:" + names.map(reEscape).join("|") + ")(?![\\w-])" : "";
    const re = new RegExp(mentionRe().source + "|<@&([1-9][0-9]{0,18})>" + memberPattern, "gi");
    const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT);
    const nodes = [];
    while (walker.nextNode()) nodes.push(walker.currentNode);
    while (nodes.length) {
        const node = nodes.shift();
        if (node.parentNode?.closest?.("a, .md-code, .md-pre, .mention-tok")) continue;
        re.lastIndex = 0;
        const hit = re.exec(node.nodeValue);
        if (!hit) continue;
        const token = node.splitText(hit.index);
        const tail = token.splitText(hit[0].length);
        const span = document.createElement("span");
        span.className = "mention-tok";
        span.textContent = hit[2] ? roleMentionLabel(V().state.clients, hit[2]) : token.nodeValue;
        if (hit[2]) span.title = token.nodeValue;
        else if (!MENTION_ALL_FORMS.some(name => `@${name}`.toLocaleLowerCase() === token.nodeValue.toLocaleLowerCase())) {
            const matches = members.filter(c => `@${c.nickname}`.toLocaleLowerCase() === token.nodeValue.toLocaleLowerCase());
            if (matches.length === 1) memberTarget(span, matches[0].unique_id, matches[0].nickname);
        }
        token.parentNode.replaceChild(span, token);
        nodes.unshift(tail); // a body can mention more than one name
    }
}

// ---------------------------------------------------------------------------
// Custom server emoji (95)
// ---------------------------------------------------------------------------

async function ensureCustomEmoji() {
    if (!customDirty) return;
    const generation = V().state.serverGeneration;
    const tabID = V().state.activeTabID;
    customDirty = false;
    try {
        const r = await app().EmojiListForTab(tabID);
        if (generation !== V().state.serverGeneration) return;
        customEmoji = r.emojis || [];
    } catch {
        if (generation !== V().state.serverGeneration) return;
        customEmoji = [];
    }
}

async function emojiURL(name) {
    if (emojiURLs.has(name)) return emojiURLs.get(name);
    const generation = V().state.serverGeneration;
    const tabID = V().state.activeTabID;
    emojiURLs.set(name, ""); // marks "loading/failed until proven otherwise"
    try {
        const d = await app().EmojiGetForTab(tabID, name);
        if (generation !== V().state.serverGeneration) return "";
        const u = imageDataURL(d);
        if (!u) return "";
        emojiURLs.set(name, u);
        return u;
    } catch {
        return "";
    }
}

// applyCustomEmoji swaps :name: tokens for cached custom-emoji images. A name
// without a cached URL triggers a background load; the emoji shows up on the
// next render (messages render often enough that this is acceptable).
function applyCustomEmoji(html) {
    if (customDirty) ensureCustomEmoji();
    if (typeof html !== "string" || html.indexOf(":") === -1) return html;

    // Load image bytes only for emoji tokens present in this message.
    for (const e of customEmoji) {
        if (html.includes(`:${e.name}:`)) {
            const url = emojiURLs.get(e.name);
            if (url === undefined) emojiURL(e.name);
        }
    }
    return transformCustomEmoji(html, customEmoji, emojiURLs);
}

// ---------------------------------------------------------------------------
// Message rendering (91-93, 97, 99, 101/102, 106, 107)
// ---------------------------------------------------------------------------

// encryptionBadgeElement draws the badge every displayed message carries
// (message-encryption.js): lock, shield or warning.
export function encryptionBadgeElement(badge) {
    if (!badge.kind) return null;
    const lock = document.createElement("span");
    lock.className = "msg-lock";
    lock.dataset.encryption = badge.kind;
    lock.innerHTML = icon(badge.kind);
    lock.setAttribute("role", "img");
    lock.setAttribute("aria-label", t(badge.label));
    lock.title = t(badge.label);
    return lock;
}

// CHAT_ENCRYPTION_HELP is the single source for what the shield actually
// promises, so the channel info panel and the README cannot drift apart.
export const CHAT_ENCRYPTION_HELP = chatEnglish["chat.encryptionHelp"];

function renderMsg(m) {
    const el = document.createElement("div");
    el.className = "msg rich";
    if (m.deleted) el.classList.add("deleted");
    if (m.mentioned) el.classList.add("mentioned");
    if (m.self) el.classList.add("own");
    if (m.direct && m.offline) el.classList.add("offline-direct");
    if (m.id) el.dataset.msgId = m.id;
    m.renderKey ||= m.id ? "message-" + m.id : m.clientMsgID ? "client-" + m.clientMsgID : crypto.randomUUID();
    el.dataset.scrollKey = m.renderKey;
    el.dataset.ts = m.ts;
    const badge = encryptionBadge(m);
    el.dataset.authorGroup = m.deleted || !m.fromUID ? "" : JSON.stringify([m.fromUID, m.from, m.self, m.direct, m.channelID, badge.kind]);
    const avatar = document.createElement("span");
    avatar.className = "msg-avatar";
    avatar.setAttribute("aria-hidden", "true");
    avatar.textContent = (m.from || "?").slice(0, 1).toUpperCase();
    avatar.style.setProperty("--avatar-color", avatarColor(m.fromUID || m.from || ""));
    el.appendChild(avatar);

    const time = document.createElement("span");
    time.className = "msg-time mono";
    const stamp = document.createElement("span");
    stamp.className = "msg-timestamp";
    stamp.textContent = fmtTime(m.ts);
    time.appendChild(stamp);
    time.title = fmtFull(m.ts);
    el.appendChild(time);

    const from = document.createElement("span");
    from.className = "msg-from" + (m.self ? " self" : "");
    from.textContent = m.from + ":";
    memberTarget(from, m.fromUID, m.from);
    if (m.fromUID) { avatar.dataset.memberUid = m.fromUID; avatar.dataset.memberName = m.from; }
    el.appendChild(from);

    const tag = document.createElement("span");
    tag.className = "msg-tag";
    tag.textContent = t(m.direct ? (m.offline ? "polish.offlineDirect" : "polish.direct") : (m.channelID ? "polish.channel" : "polish.global"));
    el.appendChild(tag);

    // (4b/91-135) Every message shows its encryption: lock (end-to-end),
    // shield (client→server→client) or warning. A body that stayed sealed or
    // arrived as plaintext is never rendered: its refusal text replaces it.
    if (badge.kind === "warning") el.classList.add("missing-key");
    const lock = encryptionBadgeElement(badge);

    const body = document.createElement("span");
    body.className = "msg-text";
    if (m.deleted) {
        body.classList.add("tombstone");
        body.textContent = t("polish.deleted");
    } else if (badge.bodyKey) {
        body.textContent = t(badge.bodyKey);
    } else {
        renderBody(body, m);
    }
    if (lock) time.appendChild(lock);
    el.appendChild(body);

    if (m.edited && !m.deleted) {
        const em = document.createElement("span");
        em.className = "msg-edited";
        em.textContent = t("polish.edited");
        el.appendChild(em);
    }

    // (124) delivery/read receipt tick on my outgoing DMs.
    if (m.self && m.direct && m.clientMsgID) {
        const tick = document.createElement("span");
        tick.className = "msg-receipt";
        tick.dataset.cmid = m.clientMsgID;
        const r = receipts.get(m.clientMsgID);
        tick.textContent = r === "read" ? "✓✓" : r === "delivered" ? "✓" : "";
        if (r === "read") tick.classList.add("read");
        el.appendChild(tick);
    }

    if (!m.deleted && m.id) {
        el.appendChild(renderReacts(m));
        const actions = renderActions(m);
        el.appendChild(actions);
        el.tabIndex = 0;
        el.setAttribute("aria-haspopup", "menu");
        const openMenu = event => {
            if (event.target.closest('[data-member-uid], input, textarea, a, video')) return;
            event.preventDefault(); event.stopPropagation();
            closeReactStrip();
            const menu = document.createElement("div"); menu.className = "ctx-menu msg-context-menu";
            const scope = captureScope(readExportScope);
            menu.appendChild(quickReactionBar(m, menu, el, scope));
            for (const action of actions.querySelectorAll("button")) {
                const item = document.createElement("button"); item.className = "ctx-action";
                const reaction = action.dataset.action === "react";
                item.textContent = reaction ? t("chat.moreReactions") : action.getAttribute("aria-label") || action.title;
                item.disabled = action.disabled;
                item.onclick = event => {
                    // Closing removes the menu's propagation guard. Keep this
                    // click from immediately dismissing the newly opened picker.
                    event.stopPropagation();
                    const current = menu.isConnected && el.isConnected && exportScopeIsCurrent(scope);
                    closeContextMenu(menu, true);
                    if (!current) return;
                    if (reaction) openReactStrip(m, el);
                    else action.click();
                };
                menu.append(item);
            }
            mountContextMenu(menu, { trigger: el, x: event.clientX, y: event.clientY });
        };
        el.oncontextmenu = openMenu;
        el.onkeydown = event => { if (contextMenuKey(event)) openMenu(event); };
    }
    if (!m.deleted && m.direct && (m.clientMsgID || m.localSeq) && view.kind === "dm") {
        const peer = view.uid, owner = getDMOwner(), scope = captureScope(readExportScope);
        const save = document.createElement("button");
        save.type = "button";
        save.className = "msg-save";
        save.textContent = "☆";
        save.title = t("messages.save");
        save.setAttribute("aria-label", t("messages.save"));
        save.onclick = async () => {
            const ready = await owner.ready;
            if (ready.error || !dmOwnerIsCurrent(owner) || !exportScopeIsCurrent(scope) || !save.isConnected) return;
            await saveMessageReference({ kind: "dm", peer_id: peer, client_message_id: m.clientMsgID || "", local_seq: m.localSeq || 0 }, ready.context);
        };
        time.append(save);
    }
    if (!m.deleted && m.direct && m.self && m.clientMsgID && !m.id && view.kind === "dm") el.appendChild(renderDMActions(m, view.uid));
    return el;
}

// --- direct-message edit / delete (dm_devices_v1) ------------------------------
// Own direct messages are changed by per-device copies the Go layer seals; the
// change comes back as a "dm_changed" event (the sender's echo included), and
// only that event updates the conversation.

function renderDMActions(m, peer) {
    const scope = captureScope(readExportScope);
    const acts = document.createElement("span");
    acts.className = "msg-actions";
    const mk = (glyph, title, fn) => {
        const b = document.createElement("button");
        b.innerHTML = icon(glyph);
        b.title = title;
        b.setAttribute("aria-label", title);
        b.onclick = (e) => {
            e.stopPropagation();
            if (exportScopeIsCurrent(scope)) fn();
        };
        acts.appendChild(b);
    };
    // Attachments and voice messages carry tokens that an edit would break.
    if (!encryptionBadge(m).bodyKey && !/\[(file|voice):/.test(m.text || "")) mk("edit", t("chat.action.edit"), () => startDMEdit(m, peer));
    mk("trash", t("chat.action.delete"), () => deleteDM(m, peer));
    return acts;
}

function dmMsgEl(m) {
    return m.renderKey ? document.querySelector(`#chat-log .msg[data-scroll-key="${CSS.escape(m.renderKey)}"]`) : null;
}

function refreshDMEl(m) {
    const log = $("chat-log");
    const el = dmMsgEl(m);
    if (!el) return;
    const anchor = captureScrollAnchor(log);
    el.replaceWith(renderMsg(m));
    applySearchFilter();
    restoreScrollAnchor(log, anchor);
}

function startDMEdit(m, peer) {
    const scope = captureScope(readExportScope);
    const el = dmMsgEl(m);
    const body = el?.querySelector(".msg-text");
    if (!body) return;
    const input = document.createElement("input");
    input.className = "msg-edit-input";
    input.value = m.text;
    body.replaceWith(input);
    input.focus();
    input.setSelectionRange(input.value.length, input.value.length);
    const finish = () => {
        input.onblur = null;
        if (exportScopeIsCurrent(scope) && input.isConnected) refreshDMEl(m);
    };
    input.onkeydown = async (e) => {
        e.stopPropagation();
        if (!exportScopeIsCurrent(scope) || !input.isConnected) return;
        if (e.key === "Escape") finish();
        if (e.key !== "Enter") return;
        const text = input.value.trim();
        finish(); // the dm_changed echo refreshes the text
        if (text && text !== m.text) {
            await runChatMutation(scope, () => app().EditDirectMessageForTab(scope.tabID, peer, m.clientMsgID, text), t("chat.action.edit"));
        }
    };
    input.onblur = finish;
}

async function deleteDM(m, peer) {
    const scope = captureScope(readExportScope);
    const confirmed = await confirmDialog({
        title: t("chat.deleteTitle"),
        message: t("chat.dmDeleteMessageHelp"),
        confirmLabel: t("chat.deleteConfirm"),
        danger: true,
        serverScoped: true,
    });
    if (!confirmed) return;
    await runChatMutation(scope, () => app().DeleteDirectMessageForTab(scope.tabID, peer, m.clientMsgID), t("chat.action.delete"));
}

// Changes whose message is not in memory yet (an offline spool can deliver a
// change before its original) wait briefly; the Go layer persists them alike.
const pendingDMChanges = new Map();
const DM_CHANGE_WAIT_MS = 10 * 60 * 1000;

function applyDMChange(m, d) {
    if (m.deleted || m.fromUID !== d.from_unique_id) return false;
    if (d.action === "edit") {
        m.text = d.body ?? "";
        m.edited = true;
        m.encVerified = true;
        m.enc = true;
        m.encMode = "e2e";
        m.localUnverified = false;
    } else if (d.action === "delete") {
        m.deleted = true;
        m.text = "";
    } else {
        return false;
    }
    return true;
}

// onDMChanged applies an edit or deletion of a direct message. Only the
// original's sender may change it; anything else is ignored.
export function onDMChanged(d) {
    const peer = d?.peer || "";
    const ref = d?.ref_client_msg_id || "";
    if (!peer || !ref || !d.from_unique_id) return;
    const st = store.get("dm:" + peer);
    const m = st?.msgs.find((x) => x.clientMsgID === ref && x.fromUID === d.from_unique_id && !x.protocol);
    if (!m) {
        const now = Date.now();
        for (const [key, waiting] of pendingDMChanges) if (waiting.at + DM_CHANGE_WAIT_MS < now) pendingDMChanges.delete(key);
        if (pendingDMChanges.size >= 256) pendingDMChanges.clear();
        const key = peer + "|" + ref;
        pendingDMChanges.set(key, { at: now, changes: [...(pendingDMChanges.get(key)?.changes || []), d] });
        return;
    }
    if (!applyDMChange(m, d)) return;
    if (view.kind === "dm" && view.uid === peer) refreshDMEl(m);
    window.dispatchEvent(new CustomEvent("noxa-direct-changed"));
}

// onRatchetMessage projects one authoritative noxa-olm-v1 record (Go
// "ratchet_message"). Go already merged it into the sealed DM log, so nothing
// is re-recorded here and legacy receipts are not sent. Only a durable NEW
// receive (is_new, not own, not a linked copy) counts as unread or notifies.
// One stable operation ID per user operation (attachment, text sent with
// attachments, voice message): a retry after an uncertain new-protocol send
// passes the SAME ID, so the original resolves instead of a second send.
const sendOperations = new Map();
function operationIDFor(key) {
    let id = sendOperations.get(key);
    if (!id) {
        if (sendOperations.size >= 512) sendOperations.clear();
        id = crypto.randomUUID();
        sendOperations.set(key, id);
    }
    return id;
}
function sendChatOperation(tabID, scope, target, text, parent, operationID) {
    const api = app();
    if (typeof api.SendChatOperationForTab === "function") return api.SendChatOperationForTab(tabID, scope, target, text, parent || 0, operationID);
    return parent ? api.SendChatReplyForTab(tabID, scope, target, text, parent) : api.SendChatForTab(tabID, scope, target, text);
}

export function onRatchetMessage(d) {
    if (!d || d.protocol !== RATCHET_PROTOCOL) return false;
    if (d.scope === "group") {
        window.dispatchEvent(new CustomEvent("noxa-ratchet-group", { detail: d }));
        return false;
    }
    const peer = d.peer || "";
    if (d.scope !== "direct" || !peer) return false;
    const key = "dm:" + peer;
    const st = getStore(key);
    if (d.purpose === "edit" || d.purpose === "delete") {
        // An own change shows only once accepted; a linked copy carries no status.
        if (d.self && d.status && d.status !== "accepted") return false;
        const change = { action: d.purpose, from_unique_id: d.sender, body: d.body?.text ?? "", peer, ref_client_msg_id: d.reference };
        const target = st.msgs.find((x) => x.protocol === RATCHET_PROTOCOL && x.fromUID === d.sender && x.clientMsgID === d.reference);
        if (!target) {
            const waitKey = peer + "|olm|" + d.reference;
            if (pendingDMChanges.size >= 256) pendingDMChanges.clear();
            pendingDMChanges.set(waitKey, { at: Date.now(), changes: [...(pendingDMChanges.get(waitKey)?.changes || []), change] });
            return false;
        }
        if (applyDMChange(target, change) && view.kind === "dm" && view.uid === peer) refreshDMEl(target);
        window.dispatchEvent(new CustomEvent("noxa-direct-changed"));
        return false;
    }
    if (d.purpose !== "chat") return false;
    const tab = pmTabs.get(peer) || { uid: peer, nick: knownPeerName(peer, ""), unread: 0, offline: false, pendingRead: "" };
    if (!pmTabs.has(peer)) pmTabs.set(peer, tab);
    const m = ratchetDMMessage(d, d.self ? V().state.myNickname : knownPeerName(peer, ""));
    st.msgs = st.msgs.filter((x) => x.clientMsgID !== "quarantine:" + (d.transport?.delivery_id || ""));
    const existing = st.msgs.find((x) => sameDMMessage(x, m));
    if (existing) {
        // The committed original is authoritative: only its status moves.
        Object.assign(existing, { pending: m.pending, failed: m.failed });
        if (view.kind === "dm" && view.uid === peer) refreshDMEl(existing);
        return false;
    }
    if (pushMsg(key, m) === "duplicate") return false;
    const waitKey = peer + "|olm|" + m.clientMsgID;
    for (const change of pendingDMChanges.get(waitKey)?.changes || []) applyDMChange(m, change);
    pendingDMChanges.delete(waitKey);
    const incoming = ratchetIsIncoming(d);
    if (incoming) {
        window.__noxaNotify?.notify("dm", (m.from || "someone") + ": " + previewLine(m),
            { uid: peer, className: "messages", kind: "info", announce: false,
                reference: { kind: "dm", peer_id: peer, client_message_id: m.clientMsgID, local_seq: 0 } });
    }
    const live = key === activeKey() && !isPrivateGroupActive();
    if (live) appendLive(m);
    else if (incoming) {
        tab.unread++;
        if (!V().state.replayingTabID) tab.unreadAt = Date.now();
    }
    renderTabs();
    return true;
}

// onRatchetQuarantine shows truthful locked placeholders for new-protocol
// copies that could not be opened. They are never unread and disappear once
// the copy opens or is no longer quarantined.
export function onRatchetQuarantine(d) {
    const list = Array.isArray(d?.placeholders) ? d.placeholders : [];
    const current = new Set();
    for (const q of list) {
        if (q?.scope !== "direct" || !q.peer || !q.id) continue;
        const id = "quarantine:" + q.id;
        current.add(id);
        const st = getStore("dm:" + q.peer);
        if (st.msgs.some((x) => x.clientMsgID === id)) continue;
        pushMsg("dm:" + q.peer, { id: 0, localSeq: 0, protocol: RATCHET_PROTOCOL, from: knownPeerName(q.sender, "") || q.sender || "?", fromUID: q.sender || "",
            text: "[encrypted message — key unavailable]", ts: Math.round((Number(q.seen_at) || Date.now() / 1000) * 1000), edited: false, deleted: false,
            reactions: null, mentions: [], e2e: false, direct: true, enc: false, encVerified: false, encMode: "", quarantined: q.code || "",
            offline: false, clientMsgID: id, channelID: null, self: false, mentioned: false });
    }
    for (const [key, st] of store) {
        if (!key.startsWith("dm:")) continue;
        const keep = st.msgs.filter((x) => !x.quarantined || current.has(x.clientMsgID));
        if (keep.length !== st.msgs.length) st.msgs = keep;
    }
    if (view.kind === "dm") renderView();
    window.dispatchEvent(new CustomEvent("noxa-ratchet-quarantine", { detail: list }));
}

function applyPendingDMChanges(peer, m) {
    if (!m.clientMsgID || !pendingDMChanges.size) return;
    const key = peer + "|" + m.clientMsgID;
    const waiting = pendingDMChanges.get(key);
    if (!waiting) return;
    pendingDMChanges.delete(key);
    if (waiting.at + DM_CHANGE_WAIT_MS < Date.now()) return;
    for (const d of waiting.changes) applyDMChange(m, d);
}

// renderBody renders markdown text plus [file:<name>] attachment refs (99)
// and the leading reply quote (107). All user text passes through
// renderMarkdown (which escapes first); file names only ever go into
// textContent or DOM properties, never raw HTML.
function renderBody(container, m) {
    const conversation = previewConversation(m);
    setPreviewConversation(container, conversation);
    const voiceScope = captureScope(() => readInlineAttachmentScope(m.isDM ? 0 : Number(m.channelID) || 0));
    if (!m.deleted && renderVoiceMessage(container, m.text, { tabID: voiceScope.tabID, channelID: voiceScope.channelID, isCurrent: () => inlineAttachmentScopeIsCurrent(voiceScope, container) })) return;
    if (renderPoll(container, m)) return;
    const text = m.text;
    const quoteNick = m.replyToID ? (findMsg(m.replyToID)?.from || t("chat.replyMessage")) : null;
    const parts = text.split(/\[file:([^\]]+)\]/);
    let html = "";
    for (let i = 0; i < parts.length; i += 2) html += renderMarkdown(parts[i]);
    container.innerHTML = applyCustomEmoji(html);
    markMentions(container); // (106) accent the @token itself, not only the row
    if (quoteNick) {
        const q = document.createElement("span");
        q.className = "msg-quote";
        q.textContent = "↪ " + quoteNick + ":";
        q.title = t("chat.quoteJump");
        container.insertBefore(q, container.firstChild);
    }
    for (let i = 1; i < parts.length; i += 2) attachFileRef(container, m, parts[i]);
    const previewScope = captureScope(readExportScope);
    const previewOptions = {
        conversation,
        enabled: V().state.settings?.link_previews !== false,
        scope: previewScope,
        isCurrent: () => exportScopeIsCurrent(previewScope) && V().state.settings?.link_previews !== false,
    };
    renderYouTubePreviews(container, previewOptions);
    renderXPostPreviews(container, previewOptions);
    renderVideoPortalPreviews(container, previewOptions);
}

const IMAGE_EXTS = ["png", "jpg", "jpeg", "gif", "webp"];
// (99) inline video. Only container formats the webview can actually decode
// are listed — a .mov would render a dead player, so it stays a download chip.
const VIDEO_EXTS = ["mp4", "webm", "ogv"];
const VIDEO_MIME = { mp4: "video/mp4", webm: "video/webm", ogv: "video/ogg" };

// (98/100) staging and inline-preview caps. Everything crosses the Wails
// bridge base64-encoded and is held in memory twice (blob + data URL), so an
// unbounded attachment takes the renderer down with it.
const ATTACH_MAX_BYTES = 25 * 1024 * 1024;
const INLINE_MAX_B64 = 8 * 1024 * 1024; // ~6 MB of media rendered inline

// attachFileRef takes the RAW capture and parses it here rather than in
// renderBody, so the file key never crosses the split boundary. The key goes
// straight into the Go call: never into textContent, an attribute or a src.
function attachFileRef(container, m, cap, source = null) {
    const ref = parseFileRef(cap);
    if (!ref.valid) {
        container.appendChild(document.createTextNode(`[file:${cap}]`));
        return;
    }
    if (!source) {
        const isDM = m.isDM || !m.channelID || m.channelID === "0" || m.channelID === 0;
        source = chatAttachmentSource(isDM ? 0 : Number(m.channelID));
    }
    const name = ref.name;
    const ext = (name.split(".").pop() || "").toLowerCase();
    const wrap = document.createElement("span");
    wrap.className = "msg-file";
    const previewIsCurrent = () => source.previewCurrent(wrap);
    const inlineImage = IMAGE_EXTS.includes(ext);
    const inlineVideo = VIDEO_EXTS.includes(ext);
    if (inlineImage || inlineVideo) {
        wrap.textContent = t(inlineVideo ? "chat.loadingVideo" : "chat.loadingImage", { name });
        Promise.resolve().then(() => source.download(ref)).then((b64) => {
            // Check before touching the wrapper or constructing a renderer data
            // URL: a completed request belongs to the scope that launched it.
            if (!previewIsCurrent()) return;
            wrap.textContent = "";
            if (!b64) {
                wrap.textContent = "📎 " + name + t("desktop.unavailable");
                return;
            }
            // A data: URL costs ~4/3 of the payload again in the renderer, so
            // oversized media stays a download chip rather than wedging the
            // webview (100).
            if (b64.length > INLINE_MAX_B64) {
                wrap.appendChild(downloadChip(source, ref, name, t("chat.tooLargePreview")));
                return;
            }
            const el = inlineVideo
                ? document.createElement("video")
                : document.createElement("img");
            if (inlineVideo) {
                el.className = "msg-video";
                el.controls = true;
                el.preload = "none";
                el.src = `data:${VIDEO_MIME[ext]};base64,${b64}`;
            } else {
                el.className = "msg-img";
                el.alt = name;
                el.loading = "lazy";
                el.decoding = "async";
                el.src = `data:image/${ext === "jpg" ? "jpeg" : ext};base64,${b64}`;
            }
            if (!previewIsCurrent()) return;
            el.title = inlineVideo ? name : name + t("desktop.click.to.zoom");
            wrap.appendChild(el);
            const zoom = document.createElement("button");
            zoom.className = "media-zoom";
            zoom.textContent = "⤢";
            zoom.title = t("chat.openFull", { name });
            zoom.onclick = () => { if (previewIsCurrent()) openLightbox(el); };
            wrap.appendChild(zoom);
            if (!inlineVideo) el.onclick = () => { if (previewIsCurrent()) openLightbox(el); }; // controls own the click on a video
        }).catch((err) => {
            if (!previewIsCurrent()) return;
            wrap.textContent = "";
            if (String(err).includes("too large to preview")) {
                wrap.appendChild(downloadChip(source, ref, name, t("chat.tooLargePreview")));
            } else {
                wrap.textContent = "📎 " + name + t("chat.downloadFailed");
            }
        });
    } else {
        wrap.appendChild(downloadChip(source, ref, name, ""));
    }
    container.appendChild(wrap);
}

// chatAttachmentSource binds channel and DM attachments (scope 0) to the chat
// view that rendered them. Private groups supply their own source with the
// same shape, bound to their folder-scoped storage and workspace view.
function chatAttachmentSource(chID) {
    const previewScope = captureScope(() => readInlineAttachmentScope(chID));
    const saveScope = captureScope(() => readAttachmentSaveScope(chID));
    return {
        download: ref => app().DownloadChatAttachmentForTab(previewScope.tabID, chID, ref.storage, ref.key),
        previewCurrent: wrap => inlineAttachmentScopeIsCurrent(previewScope, wrap),
        save: (ref, name) => app().SaveChatAttachmentForTab(saveScope.tabID, chID, ref.storage, ref.key, name),
        saveCurrent: () => attachmentSaveScopeIsCurrent(saveScope),
    };
}

// renderSharedRichText renders a message body through the exact pipeline of
// channel and DM rows: escaped markdown with autolinks and remote GIF/image
// embeds, server emoji, mentions and encrypted [file:...] attachments with
// inline image/GIF/video previews. Callers handle voice messages first.
export function renderSharedRichText(container, text, source) {
    setPreviewConversation(container, "private");
    const parts = String(text ?? "").split(/\[file:([^\]]+)\]/);
    let html = "";
    for (let i = 0; i < parts.length; i += 2) html += renderMarkdown(parts[i]);
    container.innerHTML = applyCustomEmoji(html);
    markMentions(container);
    for (let i = 1; i < parts.length; i += 2) attachFileRef(container, null, parts[i], source);
}

// bindSharedLinks gives another message surface the chat log's link policy:
// links open through the native browser, never by navigating the webview, and
// hovering shows the same privacy-preserving preview card.
export function bindSharedLinks(root) {
    root.addEventListener("click", (e) => {
        const link = e.target.closest(".md-link");
        if (!link || !root.contains(link)) return;
        e.preventDefault();
        const url = link.getAttribute("data-url");
        if (url) window.runtime.BrowserOpenURL(url);
    });
    bindLinkCardHover(root);
}

// downloadChip builds the "📎 name" button. ref carries the attachment key,
// which is read straight into the Go call and never written to the DOM. Native
// Go saves the decrypted bytes, so an attachment never becomes a base64 data
// URL or plaintext buffer in the JavaScript download path.
function downloadChip(source, ref, name, suffix) {
    const b = document.createElement("button");
    b.className = "file-chip";
    // The Messages view (arctic-dm.css) draws this as a file card; the meta
    // line stays display:none elsewhere, so the accessible name is unchanged.
    const glyph = document.createElement("span");
    glyph.className = "file-chip-icon";
    glyph.textContent = "📎";
    const label = document.createElement("span");
    label.className = "file-chip-name";
    label.textContent = name + suffix;
    // The size comes from the sealed reference; references without metadata
    // (older senders) show none. Outside the Messages view it follows the
    // name; inside, the card's meta line carries it.
    const text = fileCardText(name, ref.meta);
    const meta = document.createElement("span");
    meta.className = "file-chip-meta";
    meta.textContent = text.meta;
    b.append(glyph, " ", label);
    if (text.size) {
        const size = document.createElement("span");
        size.className = "file-chip-size";
        size.textContent = " · " + text.size;
        b.append(size);
    }
    b.append(meta);
    b.title = text.title;
    b.onclick = async () => {
        if (!b.isConnected || b.disabled || !source.saveCurrent()) return;
        b.disabled = true;
        try {
            const path = await source.save(ref, name);
            if (!source.saveCurrent()) return;
            if (path) V().toast(t("chat.saved", { name }));
        } catch (e) {
            if (source.saveCurrent()) V().toast(t("chat.saveFailed", { error: String(e) }), "warn");
        } finally {
            if (source.saveCurrent() && b.isConnected) b.disabled = false;
        }
    };
    return b;
}

// Media owns the single-player lifecycle; attachment loading stays here.
function openLightbox(node) {
    return openChatMediaLightbox(node);
}

// --- reactions (97) -----------------------------------------------------------

const QUICK_REACTS = ["👍", "❤️", "😂", "😮", "😢", "🎉", "🔥", "👀"];

function quickReactionBar(m, menu, trigger, scope) {
    const bar = document.createElement("div");
    bar.className = "msg-quick-reactions";
    bar.setAttribute("role", "group");
    bar.setAttribute("aria-label", t("chat.reactions"));
    for (const emoji of QUICK_REACTS) {
        const button = document.createElement("button");
        button.type = "button";
        button.textContent = emoji;
        button.setAttribute("aria-label", t("chat.reactWith", { emoji }));
        button.onclick = event => {
            event.stopPropagation();
            if (!menu.isConnected || !trigger.isConnected || !exportScopeIsCurrent(scope)) return;
            closeContextMenu(menu, true);
            void toggleReaction(m, emoji);
        };
        bar.appendChild(button);
    }
    bar.onkeydown = event => {
        if (!["ArrowLeft", "ArrowRight"].includes(event.key)) return;
        event.preventDefault(); event.stopPropagation();
        const buttons = [...bar.children], index = buttons.indexOf(document.activeElement);
        buttons[(index + (event.key === "ArrowRight" ? 1 : -1) + buttons.length) % buttons.length].focus();
    };
    return bar;
}

function renderReacts(m) {
    const scope = captureScope(readExportScope);
    const row = document.createElement("span");
    row.className = "msg-reacts";
    const rx = m.reactions || {};
    for (const [emoji, count] of Object.entries(rx)) {
        if (!count) continue;
        const chip = document.createElement("button");
        chip.className = "react-chip";
        if (myReactions.get(m.id)?.has(emoji)) chip.classList.add("own");
        chip.textContent = `${emoji} ${count}`;
        chip.title = t("chat.reactionToggle");
        chip.setAttribute("aria-label", t("midnight.reactionCount", { emoji, count }));
        chip.setAttribute("aria-pressed", String(!!myReactions.get(m.id)?.has(emoji)));
        chip.onclick = (e) => {
            e.stopPropagation();
            if (!exportScopeIsCurrent(scope)) return;
            toggleReaction(m, emoji);
        };
        row.appendChild(chip);
    }
    return row;
}

async function toggleReaction(m, emoji) {
    const scope = captureScope(readExportScope);
    if (!await runChatMutation(scope, () => app().ChatReactForTab(scope.tabID, m.id, emoji), "reaction")) return;
    if (!exportScopeIsCurrent(scope)) return;
    // The authoritative event supplies counts and ownership, before or after
    // the acknowledgement.
}

function refreshReacts(m) {
    const el = document.querySelector(`#chat-log .msg[data-msg-id="${m.id}"]`);
    if (!el) return;
    const old = el.querySelector(".msg-reacts");
    if (old) old.replaceWith(renderReacts(m));
}

let reactStrip = null;

function closeReactStrip() {
    if (reactStrip) {
        reactStrip.remove();
        reactStrip = null;
    }
}

function openReactStrip(m, anchorEl) {
    closeReactStrip();
    reactStrip = document.createElement("div");
    const strip = reactStrip;
    const scope = captureScope(readExportScope);
    const currentStrip = () => reactStrip === strip && exportScopeIsCurrent(scope);
    reactStrip.className = "react-strip";
    for (const emoji of QUICK_REACTS) {
        const b = document.createElement("button");
        b.textContent = emoji;
        b.onclick = () => {
            if (!currentStrip()) return;
            closeReactStrip();
            toggleReaction(m, emoji);
        };
        reactStrip.appendChild(b);
    }
    // Custom server emoji join the strip once loaded.
    ensureCustomEmoji().then(async () => {
        if (!currentStrip()) return;
        for (const e of customEmoji.slice(0, 8)) {
            if (!currentStrip()) return;
            const url = await emojiURL(e.name);
            if (!currentStrip()) return;
            if (!url) continue;
            const b = document.createElement("button");
            const img = document.createElement("img");
            img.src = url;
            img.alt = ":" + e.name + ":";
            b.appendChild(img);
            b.onclick = () => {
                if (!currentStrip()) return;
                closeReactStrip();
                toggleReaction(m, ":" + e.name + ":");
            };
            strip.appendChild(b);
        }
    });
    const r = anchorEl.getBoundingClientRect();
    reactStrip.style.left = Math.min(r.left, window.innerWidth - 320) + "px";
    reactStrip.style.top = Math.max(8, r.top - 44) + "px";
    reactStrip.onclick = (e) => e.stopPropagation();
    document.body.appendChild(reactStrip);
}

// --- hover actions (97/101/102/107/109) --------------------------------------

function renderActions(m) {
    const scope = captureScope(readExportScope);
    const acts = document.createElement("span");
    acts.className = "msg-actions";
    const mk = (glyph, title, fn) => {
        const b = document.createElement("button");
        b.innerHTML = icon(glyph);
        b.title = title;
        b.setAttribute("aria-label", title);
        b.onclick = (e) => {
            e.stopPropagation();
            if (!exportScopeIsCurrent(scope)) return;
            fn();
        };
        acts.appendChild(b);
        return b;
    };
    const copy = mk("copy", t("wins.copyMessage"), async () => {
        const current = () => acts.isConnected && exportScopeIsCurrent(scope);
        if (await copyToClipboard(m.text || "", { success: t("wins.messageCopied"), isCurrent: current }) && current()) {
            copy.textContent = t("wins.messageCopied");
            copy.setAttribute("aria-label", t("wins.messageCopied"));
            const feedback = document.createElement("span");
            feedback.className = "sr-only";
            feedback.setAttribute("role", "status");
            acts.append(feedback);
            feedback.textContent = t("wins.messageCopied");
            setTimeout(() => { feedback.remove(); if (copy.isConnected) { copy.innerHTML = icon("copy"); copy.setAttribute("aria-label", t("wins.copyMessage")); } }, 2500);
        }
    });
    mk("smile", t("chat.action.react"), () => openReactStrip(m, acts)).dataset.action = "react";
    mk("reply", t("chat.action.reply"), () => setReply(m));
    if (!m.direct && m.id) mk("pin", t("messages.save"), () => saveMessageReference({ kind: "channel", channel_id: scope.channelID, message_id: m.id }));
    // (108) only messages that are actually part of a chain get the affordance
    // — on everything else a thread button would open a panel of one.
    const th = threadIndex(activeKey());
    const root = th.rootOf.get(m.id) || m.id;
    if (th.replies.get(root)) mk("thread", t("chat.action.thread"), () => openThread(root));
    if (!m.direct && activeChannelID() > 0) mk("thread", t("discussion.start"), () => openDiscussions(activeChannelID(), m));
    if (m.self && !m.direct && !parsePoll(m.text)) mk("edit", t("chat.action.edit"), () => startEdit(m));
    const canModerate = !m.direct && ["owner", "administrator"].includes(V().state.ownAuthority);
    if (m.self || canModerate) mk("trash", t("chat.action.delete"), () => deleteMsg(m));
    if (!m.direct) {
        const pinned = isPinned(m);
        mk("pin", pinned ? t("chat.action.unpin") : t("chat.action.pin"), () => pinMsg(m));
    }
    return acts;
}

function refreshMsgEl(m) {
    const log = $("chat-log");
    const anchor = captureScrollAnchor(log);
    const el = document.querySelector(`#chat-log .msg[data-msg-id="${m.id}"]`);
    if (el) el.replaceWith(renderMsg(m));
    applySearchFilter();
    restoreScrollAnchor(log, anchor);
}

// --- edit / delete (101/102) ---------------------------------------------------

async function runChatMutation(scope, action, label, stillCurrent = () => true) {
    const current = () => exportScopeIsCurrent(scope) && stillCurrent();
    if (!current()) return false;
    let error;
    try { error = await action(); }
    catch (e) { error = String(e); }
    if (!current()) return false;
    if (error) V().toast(t("chat.mutationFailed", { action: label, error }), "warn");
    return !error;
}

function startEdit(m) {
    const scope = captureScope(readExportScope);
    const el = document.querySelector(`#chat-log .msg[data-msg-id="${m.id}"]`);
    if (!el) return;
    const body = el.querySelector(".msg-text");
    const input = document.createElement("input");
    input.className = "msg-edit-input";
    input.value = m.text;
    body.replaceWith(input);
    input.focus();
    input.setSelectionRange(input.value.length, input.value.length);
    const finish = () => {
        input.onblur = null;
        if (exportScopeIsCurrent(scope) && input.isConnected) refreshMsgEl(m);
    };
    input.onkeydown = async (e) => {
        e.stopPropagation();
        if (!exportScopeIsCurrent(scope) || !input.isConnected) return;
        if (e.key === "Escape") finish();
        if (e.key !== "Enter") return;
        const text = input.value.trim();
        finish(); // the chat_edited broadcast refreshes the text
        if (text && text !== m.text) {
            await runChatMutation(scope, () => app().ChatEditMessageForTab(scope.tabID, m.channelID ?? 0, m.id, text, m.version || 1), t("chat.action.edit"));
        }
    };
    input.onblur = finish;
}

async function deleteMsg(m) {
    const scope = captureScope(readExportScope);
    const confirmed = await confirmDialog({
        title: t("chat.deleteTitle"),
        message: t("chat.deleteHelp"),
        confirmLabel: t("chat.deleteConfirm"),
        danger: true,
        serverScoped: true,
    });
    if (!confirmed) return;
    await runChatMutation(scope, () => app().ChatDeleteMessageForTab(scope.tabID, m.id), t("chat.action.delete"));
    // The chat_deleted broadcast renders the tombstone.
}

// --- pin state (109) -----------------------------------------------------------

function pinSet(key) {
    if (!pinnedIDs.has(key)) pinnedIDs.set(key, new Set());
    return pinnedIDs.get(key);
}

function isPinned(m) {
    return pinSet("ch:" + (m.channelID ?? 0)).has(m.id);
}

// pinMsg toggles. The hover button used to always send pinned=true, so it
// could pin but never unpin despite saying so (109).
async function pinMsg(m) {
    const scope = captureScope(readExportScope);
    const chID = m.channelID ?? 0;
    await runChatMutation(scope, () => app().ChatPinMessageForTab(scope.tabID, chID, m.id, !isPinned(m)), "pin");
    // The chat_pinned/chat_unpinned broadcast updates the set and the button.
}

// ensurePins loads a scope's pin set once, so every message knows whether it
// is pinned without the panel being open.
async function ensurePins(key) {
    if (pinnedIDs.has(key) || !key.startsWith("ch:")) return;
    const scope = captureScope(readChatServerScope);
    const set = pinSet(key);
    const changes = new Map();
    pendingPinChanges.set(key, changes);
    try {
        const resp = await app().ChatPinsForTab(scope.tabID, Number(key.slice(3)));
        if (!chatServerIsCurrent(scope) || pinnedIDs.get(key) !== set) return;
        set.clear();
        for (const p of resp.pins || []) set.add(Number(p.message_id));
        for (const [id, pinned] of changes) {
            if (pinned) set.add(id);
            else set.delete(id);
        }
    } catch { /* no membership / old server: pins stay unknown, button pins */ }
    finally {
        if (pendingPinChanges.get(key) === changes) pendingPinChanges.delete(key);
    }
}

// --- reply-to (107) ------------------------------------------------------------
// New replies carry a stable message id. Quote-prefix parsing below keeps old
// stored messages and mixed-version servers readable.

function setReply(m) {
    replyRevision++;
    replyTo = m;
    const bar = $("reply-bar");
    bar.innerHTML = "";
    const label = document.createElement("span");
    label.className = "reply-label";
    label.textContent = "↪ " + m.from + ": " + m.text.slice(0, 80);
    const x = document.createElement("button");
    x.textContent = "✕";
    x.title = t("chat.replyCancel");
    x.onclick = clearReply;
    bar.appendChild(label);
    bar.appendChild(x);
    bar.classList.remove("hidden");
    $("chat-text").focus();
}

function clearReply() {
    replyRevision++;
    replyTo = null;
    $("reply-bar").classList.add("hidden");
}

function scrollToQuote(quoteEl) {
    const msgEl = quoteEl.closest(".msg");
    const id = Number(msgEl?.dataset.msgId) || 0;
    const st = getStore(activeKey());
    const self = st.msgs.find((x) => x.id === id);
    const target = self ? resolveParent(st.msgs, self) : null;
    if (!target) {
        V().toast(t("chat.quoteUnavailable"), "info", "alert");
        return;
    }
    flashMsg(target.id);
}

// flashMsg scrolls a message into view and pulses it. Returns false when the
// message is not in the rendered log (scrolled out of the loaded window).
function flashMsg(id) {
    const el = document.querySelector(`#chat-log .msg[data-msg-id="${id}"]`);
    if (!el) return false;
    el.scrollIntoView({ block: "center" });
    el.classList.add("flash");
    setTimeout(() => el.classList.remove("flash"), 1200);
    return true;
}

// ---------------------------------------------------------------------------
// Threads (108)
// ---------------------------------------------------------------------------
// A thread is a reply chain reconstructed from the 107 quote prefix: rootOf
// maps every message to the head of its chain, and replies counts descendants
// per root. The index is memoized per store key and invalidated whenever that
// store's message list changes, because renderActions asks for it once per
// rendered message.

const threadCache = new Map(); // store key -> {stamp, rootOf, replies}

function threadStamp(st) {
    return st.msgs.length + ":" + (st.msgs.length ? st.msgs[0].id + "-" + st.msgs[st.msgs.length - 1].id : "");
}

function threadIndex(key) {
    const st = getStore(key);
    const stamp = threadStamp(st);
    const hit = threadCache.get(key);
    if (hit && hit.stamp === stamp) return hit;

    const rootOf = new Map(), replies = new Map();
    for (const m of st.msgs) {
        if (!m.id) continue;
        const p = resolveParent(st.msgs, m);
        if (!p) {
            rootOf.set(m.id, m.id);
            continue;
        }
        const root = rootOf.get(p.id) || p.id;
        rootOf.set(m.id, root);
        replies.set(root, (replies.get(root) || 0) + 1);
    }
    const idx = { stamp, rootOf, replies };
    threadCache.set(key, idx);
    return idx;
}

function closeThreadPanel() {
    if (threadPanel) {
        threadPanel.remove();
        threadPanel = null;
    }
    threadRootID = 0;
}

function openThread(rootID) {
    closeThreadPanel();
    threadRootID = Number(rootID) || 0;
    threadPanel = document.createElement("div");
    threadPanel.className = "chat-pop thread-panel";
    $("chat-wrap").appendChild(threadPanel);
    renderThreadPanel();
}

// renderThreadPanel draws the chain for threadRootID. Rows reuse renderMsg so
// reactions, locks and the hover actions behave identically; the live refresh
// helpers are scoped to #chat-log, so these copies are a snapshot that this
// function redraws instead.
function renderThreadPanel() {
    if (!threadPanel) return;
    const key = activeKey();
    const st = getStore(key);
    const th = threadIndex(key);
    threadPanel.innerHTML = "";

    const head = document.createElement("div");
    head.className = "chat-pop-head";
    head.textContent = t("chat.threadCount", { count: (th.replies.get(threadRootID) || 0) + 1 });
    const x = document.createElement("button");
    x.textContent = "✕";
    x.title = t("chat.threadClose");
    x.onclick = closeThreadPanel;
    head.appendChild(x);
    threadPanel.appendChild(head);

    const chain = st.msgs.filter((m) => m.id && (th.rootOf.get(m.id) || m.id) === threadRootID);
    if (chain.length === 0) {
        const hint = document.createElement("div");
        hint.className = "set-hint";
        hint.textContent = t("chat.missingRoot");
        threadPanel.appendChild(hint);
        return;
    }
    for (const m of chain) {
        const row = document.createElement("div");
        row.className = "thread-row" + (m.id === threadRootID ? " root" : "");
        row.appendChild(renderMsg(m));
        row.onclick = () => flashMsg(m.id);
        threadPanel.appendChild(row);
    }
    const reply = document.createElement("button");
    reply.className = "thread-reply";
    reply.textContent = t("chat.replyThread");
    reply.title = t("chat.threadReply");
    reply.onclick = () => setReply(chain[chain.length - 1]);
    threadPanel.appendChild(reply);
}

// refreshThreadFor redraws the panel only when the changed message is part of
// the open chain. A redraw re-runs renderMsg, which re-downloads any
// attachment in the chain, so it must not fire on every unrelated message.
function refreshThreadFor(id) {
    if (!threadPanel || !id) return;
    if ((threadIndex(activeKey()).rootOf.get(Number(id)) || 0) !== threadRootID) return;
    renderThreadPanel();
}

// ---------------------------------------------------------------------------
// Views, PM tabs (122) and rendering
// ---------------------------------------------------------------------------

function setView(v) {
    closeDiscussions();
    closeConversations();
    invalidateChatViewWork();
    view = v;
    searchChannel = "current";
    closeThreadPanel(); // a chain belongs to one scope (108)
    resetTypingOut(); // (120) the next keystroke pings the new scope at once
    const key = activeKey();
    if (key.startsWith("ch:")) {
        const chID = Number(key.slice(3));
        if (unread.delete(chID)) V().renderTree();
        reportRead("channel", chID);
    }
    if (v.kind === "dm") {
        const tab = pmTabs.get(v.uid);
        if (tab) {
            if (tab.unread) reportRead("dm", v.uid);
            tab.unread = 0;
            tab.unreadAt = undefined;
            tab.offline = false;
        }
    }
    renderTabs();
    renderView();
    sendPendingReads();
    window.dispatchEvent(new CustomEvent("noxa-chat-view-changed", { detail: { kind: v.kind, uid: v.uid || "" } }));
}

export function resumeChatView() { setView(view); }

let selectedDirectRecipient = null;

function knownPeerName(uid, supplied = "") {
    const live = V().state.clients?.find(client => client.unique_id === uid)?.nickname;
    const names = [live, supplied, pmTabs.get(uid)?.nick, tabLayoutOwner?.layout?.names?.["dm:" + uid]];
    const history = store.get("dm:" + uid)?.msgs || [];
    for (let i = history.length - 1; i >= 0; i--) {
        if (!history[i].self && history[i].from && history[i].from !== "?") {
            names.push(history[i].from);
            break;
        }
    }
    return (names.find(name => typeof name === "string" && name.trim() && name !== uid && name !== "?" && name !== t("chat.unknownMember")) || "").trim();
}

function peerName(uid, supplied) {
    return knownPeerName(uid, supplied) || t("chat.unknownMember");
}

function selectDirectRecipient(uid, nick) {
    const input = $("chat-target");
    const label = uid ? peerName(uid, nick) : "";
    selectedDirectRecipient = uid ? { uid, label } : null;
    if (input) input.value = label;
}

function directRecipientID() {
    const value = $("chat-target")?.value.trim() || "";
    if (!value) return "";
    if (selectedDirectRecipient?.label === value) return selectedDirectRecipient.uid;
    // Typed names must resolve unambiguously. A label is never a protocol ID.
    const matches = new Set();
    for (const client of V().state.clients || []) {
        if (client.unique_id && (client.unique_id === value || client.nickname === value)) matches.add(client.unique_id);
    }
    for (const tab of pmTabs.values()) {
        if (tab.uid === value || knownPeerName(tab.uid) === value) matches.add(tab.uid);
    }
    if (matches.size) return matches.size === 1 ? [...matches][0] : "";
    // Preserve deliberate identity entry for a new/offline peer. noXa IDs
    // are canonical double-base64 SHA-256 digests, never arbitrary names.
    try {
        const digest = atob(atob(value));
        if (digest.length === 32 && btoa(btoa(digest)) === value) return value;
    } catch { /* search text is not an identity */ }
    return "";
}

export function openPM(uid, nick) {
    window.__noxaActivity?.close();
    if (!uid) return;
    // A new user action can recover failed acquisition. Pending descendants
    // retain their old owner and are never transparently retried.
    if (dmOwner?.failed && dmOwnerIsCurrent(dmOwner)) resetDMHistoryView();
    closedDMPeers.delete(uid);
    recentlyClosedChats.delete("dm:" + uid);
    const name = knownPeerName(uid, nick);
    if (!pmTabs.has(uid)) pmTabs.set(uid, { uid, nick: name, unread: 0, offline: false, pendingRead: "" });
    if (name) pmTabs.get(uid).nick = name;
    activatePM(uid);
}

function activatePM(uid) {
    // Keep the scope selector in sync so sendChat routes to the tab's user.
    $("chat-scope").value = "direct";
    selectDirectRecipient(uid);
    V().setDirectTargetVisible(true);
    setView({ kind: "dm", uid });
}

async function openE2EEDiagnostics() {
    const generation = V().state.serverGeneration;
    const peer = view.kind === "dm" ? view.uid : "";
    let d;
    try { d = await app().E2EEDiagnostics(peer); }
    catch (err) {
        if (generation === V().state.serverGeneration) V().toast(t("chat.diagnosticsFailed", { error: String(err) }), "warn");
        return;
    }
    if (generation !== V().state.serverGeneration) return;
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    const verified = peer && d.safety_number && V().state.settings?.e2ee_verified?.[peer] === d.safety_number;
    const changed = peer && V().state.settings?.e2ee_verified?.[peer] && !verified;
    overlay.innerHTML = `<div class="dlg e2ee-diagnostics">
        <h3>${t("chat.e2ee")}</h3>
        <div class="e2ee-status ${changed ? "warn" : verified ? "ok" : ""}">${changed ? t("chat.safetyChanged") : verified ? t("chat.safetyVerifiedStatus") : peer ? t("chat.safetyUnverified") : t("chat.channelDiagnostics")}</div>
        <dl>
            <dt>${t("chat.cipher")}</dt><dd>${escapeHTML(d.cipher || t("chat.unknown"))}</dd>
            <dt>${t("chat.cachedKeys")}</dt><dd>${d.cached_peers || 0}</dd>
            <dt>${t("chat.channelGenerations")}</dt><dd>${d.scope_keys || 0}</dd>
            <dt>${t("chat.missingKeys")}</dt><dd>${d.refused_keys || 0}</dd>
            <dt>${t("chat.pendingHandshakes")}</dt><dd>${d.pending_key_pulls || 0}</dd>
        </dl>
        ${peer ? `<label>${t("chat.safetyNumber")}</label><pre class="safety-number">${escapeHTML(d.safety_number || t("chat.keyUnavailable"))}</pre>` : ""}
        <div class="dlg-buttons">${peer && d.safety_number ? `<button class="dlg-ok verify-safety">${t("chat.markVerified")}</button>` : ""}<button class="dlg-cancel">${t("chat.close")}</button></div>
    </div>`;
    overlay.querySelector(".dlg-cancel").onclick = () => overlay.remove();
    overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
    overlay.querySelector(".verify-safety")?.addEventListener("click", async () => {
        const settings = V().state.settings;
        const previous = settings.e2ee_verified;
        settings.e2ee_verified = { ...(previous || {}), [peer]: d.safety_number };
        try {
            const err = await app().SaveSettings(settings);
            if (err) throw new Error(err);
        } catch (err) {
            settings.e2ee_verified = previous;
            if (!isCurrentServerDialog(overlay)) return;
            V().toast(t("chat.safetySaveFailed", { error: String(err) }), "warn");
            return;
        }
        if (!isCurrentServerDialog(overlay)) return;
        overlay.remove();
        V().toast(t("chat.safetyVerified"));
    });
    mountServerDialog(overlay);
}

function activateChannel(channelID) {
    const id = Number(channelID);
    if (!id || !subscribed(id)) return;
    recentlyClosedChats.delete("ch:" + id);
    $("chat-scope").value = "channel";
    V().setDirectTargetVisible(false);
    setView(id === V().state.myChannelID ? { kind: "channel" } : { kind: "chan", id });
}

export function isSubscribed(channelID) {
    return subscribed(channelID);
}

export async function setChannelSubscription(channelID, subscribe) {
    const id = Number(channelID);
    if (!id) return false;
    if (!subscribe && id === V().state.myChannelID) {
        V().toast(t("chat.joinedSubscribed"), "info", "alert");
        return false;
    }
    const scope = captureScope(readChatServerScope);
    let err;
    try {
        err = await app().SubscribeChannelsForTab(scope.tabID, [id], !!subscribe);
    } catch (error) {
        err = String(error);
    }
    if (!chatServerIsCurrent(scope)) return false;
    if (err) {
        V().toast(t("chat.subscriptionFailed", { error: String(err) }), "warn");
        return false;
    }
    return true;
}

export async function openChannelTab(channelID) {
    window.__noxaActivity?.close();
    if (V().state.channels.some(ch => ch.ChannelID === channelID && ch.SystemKind === "home")) return;
    const id = Number(channelID);
    if (!id) return;
    if (subscribed(id)) {
        activateChannel(id);
        return;
    }
    pendingChannelTab = id;
    const request = ++channelTabRequest;
    const scope = captureScope(readChatServerScope);
    if (!await setChannelSubscription(id, true) && chatServerIsCurrent(scope) && request === channelTabRequest && pendingChannelTab === id) pendingChannelTab = 0;
}

async function closeChannelTab(channelID) {
    const id = Number(channelID);
    if (id === V().state.myChannelID) {
        V().toast(t("chat.moveUnsubscribe"), "info", "alert");
        return;
    }
    if (tabSubscriptionChanges.has(id)) return;
    const scope = captureScope(readDMHistoryScope), name = "# " + channelName(id);
    const result = await confirmTabSubscription(id, false, scope);
    if (!dmHistoryScopeIsCurrent(scope) || !V().state.channels.some(channel => Number(channel.ChannelID) === id)) return;
    if (!result.ok) {
        if (!result.reported) V().toast(t("chat.tabsCloseFailed", { name }), "warn");
        return;
    }
    removeTabPin("ch:" + id);
    rememberClosedChat("ch:" + id, name);
    renderTabs();
}

// Sending a subscription request does not authorize membership. Wait for the
// authoritative server reply before consuming an undo entry or opening chat.
function confirmTabSubscription(id, subscribe, scope) {
    let resolveConfirmation;
    const confirmed = new Promise(resolve => { resolveConfirmation = resolve; });
    const request = {
        subscribe, scope, timer: 0,
        finish(ok, reported = false) {
            if (tabSubscriptionChanges.get(id) !== request) return;
            clearTimeout(request.timer);
            tabSubscriptionChanges.delete(id);
            resolveConfirmation({ ok, reported });
        },
    };
    tabSubscriptionChanges.set(id, request);
    request.timer = setTimeout(() => request.finish(false), 10_000);
    void setChannelSubscription(id, subscribe).then(sent => {
        if (!sent) request.finish(false, true);
    });
    return confirmed;
}

function rememberClosedChat(key, name) {
    recentlyClosedChats.delete(key);
    recentlyClosedChats.set(key, { key, name, scope: captureScope(readDMHistoryScope), pending: false });
    while (recentlyClosedChats.size > 10) recentlyClosedChats.delete(recentlyClosedChats.keys().next().value);
}

async function reopenClosedChat(entry) {
    const current = () => recentlyClosedChats.get(entry.key) === entry && dmHistoryScopeIsCurrent(entry.scope);
    if (!current() || entry.pending) return;
    if (entry.key.startsWith("dm:")) { V().openDirectMessages?.({ uid: entry.key.slice(3), name: entry.name }); return; }
    if (entry.key.startsWith(GROUP_TAB)) {
        const groupID = entry.key.slice(GROUP_TAB.length);
        recentlyClosedChats.delete(entry.key);
        closedGroupTabs.delete(`${V().state.activeTabID}:${groupID}`);
        groupTabs.set(groupID, { id: groupID, name: entry.name, unread: 0, unreadAt: undefined, latest: 0 });
        renderTabs();
        await openConversationsAt(groupID, 0, () => dmHistoryScopeIsCurrent(entry.scope));
        return;
    }
    const id = Number(entry.key.slice(3));
    if (!V().state.channels.some(channel => Number(channel.ChannelID) === id)) {
        recentlyClosedChats.delete(entry.key); renderTabs();
        V().toast(t("chat.tabsReopenFailed", { name: entry.name }), "warn");
        return;
    }
    if (subscribed(id)) { activateChannel(id); return; }
    if (tabSubscriptionChanges.has(id)) return;
    entry.pending = true; renderTabs();
    const result = await confirmTabSubscription(id, true, entry.scope);
    if (!current()) return;
    entry.pending = false;
    if (result.ok && subscribed(id)) activateChannel(id);
    else {
        renderTabs();
        if (!result.reported) V().toast(t("chat.tabsReopenFailed", { name: entry.name }), "warn");
    }
}

// Normalize the full authoritative subscription payload before replacing the
// local model. Keeping this boundary pure makes malformed bridge events a
// no-op and lets every caller use the same sorted, deduplicated IDs.
export function normalizeSubscriptionState(json) {
    const state = parseRuntimeObject(json);
    if (!state) return null;
    return [...new Set((state.channel_ids || []).map(Number).filter((id) => id > 0))]
        .sort((a, b) => a - b);
}

// SubscriptionState is a full authoritative set. Replacing the local model
// makes rejection, revocation, deletion and missed events self-healing.
export function onSubscriptions(json) {
    const next = normalizeSubscriptionState(json);
    if (!next) return;
    subscriptions = next;
    chanTabs.clear();
    for (const id of subscriptions) chanTabs.set(id, { id });
    for (const [id, request] of tabSubscriptionChanges) {
        // An unrelated snapshot may precede this request's reply. A matching
        // state confirms success; rejection or a lost reply times out safely.
        if (dmHistoryScopeIsCurrent(request.scope) && subscribed(id) === request.subscribe) request.finish(true);
    }

    if (view.kind === "chan" && !subscribed(view.id)) {
        setView({ kind: "channel" });
    } else {
        renderTabs();
        updateHeader();
    }
    if (pendingChannelTab && subscribed(pendingChannelTab)) {
        const id = pendingChannelTab;
        pendingChannelTab = 0;
        activateChannel(id);
    } else if (pendingChannelTab) {
        pendingChannelTab = 0;
    }
}

// closePM closes the TAB. Dropping the in-memory window is safe now that the
// conversation is on disk sealed: the next open replays it from the local log
// instead of starting empty. Destroying that log is a separate, explicit act
// (clearPMHistory) — closing a tab must never delete a conversation (122).
function closePM(uid) {
    const tab = pmTabs.get(uid);
    if (!tab) return;
    rememberClosedChat("dm:" + uid, tab.nick);
    closedDMPeers.add(uid);
    removeTabPin("dm:" + uid);
    pmTabs.delete(uid);
    store.delete("dm:" + uid);
    if (view.kind === "dm" && view.uid === uid) {
        $("chat-scope").value = "channel";
        V().setDirectTargetVisible(false);
        setView({ kind: "channel" });
        return;
    }
    renderTabs();
}

function closeGroupTab(groupID) {
    const tab = groupTabs.get(groupID);
    if (!tab) return;
    rememberClosedChat(GROUP_TAB + groupID, tab.name);
    removeTabPin(GROUP_TAB + groupID);
    closedGroupTabs.set(`${V().state.activeTabID}:${groupID}`, tab.latest || 0);
    groupTabs.delete(groupID);
    renderTabs();
}

// syncGroupTabs mirrors DM tab behaviour for private groups: an unread group
// message opens a tab (never activating it or taking focus), a closed tab
// returns only for a newer message, and muted groups never add a tab on their
// own. An authoritative list also removes tabs of groups the user left.
export function syncGroupTabs(tabID, generation, groups, uid, { authoritative = false } = {}) {
    const st = V().state;
    if (tabID !== st.activeTabID || generation !== st.serverGeneration || !Array.isArray(groups)) return;
    const muted = new Set(st.settings?.muted_conversations || []);
    const accepted = new Map();
    for (const group of groups) {
        const member = group?.members?.find(member => member.unique_id === uid);
        if (typeof group?.id === "string" && member && !member.pending) accepted.set(group.id, group);
    }
    let changed = false;
    if (authoritative) {
        for (const id of [...groupTabs.keys()]) {
            if (!accepted.has(id)) { groupTabs.delete(id); changed = true; }
        }
    }
    const pinned = new Set(tabLayoutOwner?.ready ? tabLayoutOwner.layout.pinned : []);
    for (const group of accepted.values()) {
        const latest = Number(group.latest_message_id) || 0;
        const count = Math.max(0, Number(group.unread_count) || 0);
        let tab = groupTabs.get(group.id);
        if (!tab) {
            const closedAt = closedGroupTabs.get(`${tabID}:${group.id}`);
            const arrived = count > 0 && !muted.has(group.id) && (closedAt === undefined || latest > closedAt);
            if (!arrived && !pinned.has(GROUP_TAB + group.id)) continue;
            closedGroupTabs.delete(`${tabID}:${group.id}`);
            recentlyClosedChats.delete(GROUP_TAB + group.id);
            tab = { id: group.id, name: group.name, unread: 0, unreadAt: undefined, latest: 0 };
            groupTabs.set(group.id, tab);
            changed = true;
        }
        if (tab.name !== group.name) { tab.name = group.name; changed = true; }
        if (tab.unread !== count) {
            if (count > tab.unread && !st.replayingTabID) tab.unreadAt = Date.now();
            if (!count) tab.unreadAt = undefined;
            tab.unread = count;
            changed = true;
        }
        tab.latest = Math.max(tab.latest, latest);
    }
    if (changed) renderTabs();
}

function tabLayoutFor(owner) {
    if (tabLayoutOwner?.owner === owner) return tabLayoutOwner;
    const record = { owner, layout: { order: [], pinned: [] }, ready: false, saved: null, pending: Promise.resolve(), revision: 0, closed: new Set() };
    tabLayoutOwner = record;
    record.loading = callDMHistory(owner, "ChatTabLayoutForContext").then(value => {
        if (tabLayoutOwner !== record || !dmOwnerIsCurrent(owner)) return;
        if (!value || !Array.isArray(value.order) || !Array.isArray(value.pinned)) throw new Error("Invalid chat layout");
        record.layout = { order: value.order, pinned: value.pinned, names: value.names || {} };
        record.saved = record.layout;
        record.ready = true;
        if (record.layout.pinned.some(key => record.closed.has(key))) {
            saveTabLayout({ ...record.layout, pinned: record.layout.pinned.filter(key => !record.closed.has(key)) });
        }
        renderTabs();
    }).catch(() => {
        // The identity/history path reports acquisition failures itself.
        if (owner.context && tabLayoutOwner === record && dmOwnerIsCurrent(owner)) V().toast(t("chat.tabsLoadFailed"), "warn");
    });
    return record;
}

function removeTabPin(key) {
    const record = tabLayoutOwner;
    if (!record || !dmOwnerIsCurrent(record.owner)) return;
    record.closed.add(key);
    if (record.ready && record.layout.pinned.includes(key)) saveTabLayout({ ...record.layout, pinned: record.layout.pinned.filter(id => id !== key) });
}

function saveTabLayout(layout) {
    const record = tabLayoutOwner;
    if (!record?.ready || !dmOwnerIsCurrent(record.owner)) return;
    // Keep human labels only for pinned DMs, in the same encrypted native file.
    const names = {};
    for (const key of layout.pinned) {
        if (!key.startsWith("dm:")) continue;
        const name = pmTabs.get(key.slice(3))?.nick || record.layout.names?.[key];
        if (typeof name !== "string") continue;
        const label = [...name.replace(/[\p{Cc}\p{Zl}\p{Zp}]/gu, "").trim()].slice(0, 64).join("");
        if (label) names[key] = label;
    }
    layout = { order: layout.order, pinned: layout.pinned, ...(Object.keys(names).length ? { names } : {}) };
    const revision = ++record.revision;
    record.layout = layout;
    renderTabs();
    // Each queued write retains the native owner it was created for.
    record.pending = record.pending.then(async () => {
        if (tabLayoutOwner !== record || !dmOwnerIsCurrent(record.owner)) return;
        try {
            const error = await callDMHistory(record.owner, "SaveChatTabLayoutForContext", layout);
            if (error) throw new Error(error);
            record.saved = layout;
        } catch {
            if (tabLayoutOwner !== record || !dmOwnerIsCurrent(record.owner) || record.revision !== revision) return;
            record.layout = record.saved;
            renderTabs();
            V().toast(t("chat.tabsSaveFailed"), "warn");
        }
    });
}

function renderTabs() {
    V()?.syncTrayUnread?.(); // every unread badge change re-renders the tab bar
    window.dispatchEvent(new CustomEvent("noxa-direct-changed"));
    const scope = captureScope(readDMHistoryScope);
    const current = () => dmHistoryScopeIsCurrent(scope);
    const items = [];
    for (const { id } of chanTabs.values()) {
        const badge = unread.get(id);
        const joined = id === V().state.myChannelID;
        items.push({
            key: "ch:" + id, name: "# " + channelName(id), fullName: "# " + (channelPath(V().state.channels, id) || channelName(id)),
            active: activeChannelID() === id && view.kind !== "global" && view.kind !== "dm",
            unread: badge?.n || 0, arrivedAt: badge?.unreadAt, mention: badge?.mention, mentions: badge?.mentions, replies: badge?.replies,
            title: t(joined ? "chat.joinedChannel" : "chat.subscribedChannel"),
            open: () => { if (current() && chanTabs.has(id)) activateChannel(id); },
            close: joined ? null : () => { if (current() && chanTabs.has(id)) closeChannelTab(id); },
        });
    }
    for (const tab of pmTabs.values()) {
        if (!(tab.unread > 0)) continue;
        const owns = () => current() && pmTabs.get(tab.uid) === tab;
        tab.nick = knownPeerName(tab.uid);
        items.push({
            key: "dm:" + tab.uid, name: peerName(tab.uid), active: view.kind === "dm" && view.uid === tab.uid,
            unread: tab.unread, arrivedAt: tab.unreadAt, offline: tab.offline,
            open: () => { if (owns()) V().openDirectMessages?.({ uid: tab.uid }); },
            close: () => { if (owns()) closePM(tab.uid); },
            clearHistory: () => { if (owns()) clearPMHistory(tab.uid, tab.nick); },
        });
    }
    for (const tab of groupTabs.values()) {
        if (!(tab.unread > 0)) continue;
        const owns = () => current() && groupTabs.get(tab.id) === tab;
        items.push({
            key: GROUP_TAB + tab.id, name: tab.name, title: t("group.tabTitle", { name: tab.name }),
            active: isPrivateGroupActive() && viewedConversationID() === tab.id,
            unread: tab.unread, arrivedAt: tab.unreadAt,
            open: () => { if (owns()) V().openDirectMessages?.({ groupID: tab.id }); },
            close: () => { if (owns()) closeGroupTab(tab.id); },
        });
    }
    const saved = items.length && V().state.activeTabID ? tabLayoutFor(getDMOwner()) : null;
    chatTabs?.render(items, saved?.layout || { order: [], pinned: [] }, {
        scope: JSON.stringify(scope), ready: !!saved?.ready,
        recentlyClosed: [...recentlyClosedChats.values()].reverse().filter(entry => dmHistoryScopeIsCurrent(entry.scope) && !items.some(item => item.key === entry.key))
            .map(entry => ({ ...entry, open: () => reopenClosedChat(entry) })),
    });
}

// --- Messages view accessors (dm-view.js) ---------------------------------------
// liveDirectConversations reports this session's DM tabs with their unread
// state and newest in-memory message; storedDirectPeers lists every sealed
// local log with its newest entry. Neither changes any state.
export function liveDirectConversations() {
    return [...pmTabs.values()].map(tab => ({
        uid: tab.uid, name: knownPeerName(tab.uid), unread: tab.unread || 0, offline: !!tab.offline,
        last: store.get("dm:" + tab.uid)?.msgs.at(-1) || null,
    }));
}

export async function storedDirectPeers() {
    const scope = captureScope(readDMHistoryScope);
    const peers = await callDMHistory(getDMOwner(), "DMHistoryPeersForContext");
    return dmHistoryScopeIsCurrent(scope) ? peers || [] : null;
}

export function directMessageFromEntry(entry) { return dmMsg(entry, ""); }
export function directPeerName(uid, supplied = "") { return peerName(uid, supplied); }
export function activeDirectPeer() { return view.kind === "dm" ? view.uid : ""; }
export function closeDirectConversation(uid) { closePM(uid); }
export function clearDirectHistory(uid) {
    if (pmTabs.has(uid)) void clearPMHistory(uid, peerName(uid));
}

// --- local DM history (122) ---------------------------------------------------
// A DM is true E2EE and the server keeps nothing, so the sealed per-peer log
// in client/chat_dm_history.go is the entire history: without these calls a conversation
// exists only until the process exits.

// dmMsg converts one stored DMEntry into the renderer's message shape. The
// local seq is deliberately NOT used as the id: an id addresses a SERVER row,
// and edit/delete/pin/react all key on it, so reusing a local counter there
// would aim those actions at an unrelated message.
function dmMsg(e, peerNick) {
    const st = V().state;
    const self = !!e.self;
    const mode = storedDMMode(e);
    const verified = mode === "e2e";
    return {
        id: 0,
        localSeq: Number(e.seq) || 0,
        from: e.from_nickname || (self ? st.myNickname : peerNick) || "?",
        fromUID: e.from_unique_id || "",
        text: e.body || "",
        ts: (Number(e.sent_at) || 0) * 1000,
        edited: !!e.edited,
        deleted: !!e.deleted,
        reactions: null,
        mentions: [],
        e2e: false,
        direct: true,
        enc: verified,
        encVerified: verified,
        encMode: mode,
        localUnverified: !verified,
        offline: !!e.offline,
        clientMsgID: e.client_msg_id || "",
        // A noxa-olm-v1 record (projected by Go from the protected archive)
        // is identified by protocol + sender + logical ID, never by number.
        protocol: e.protocol || "",
        ratchetKey: e.key || "",
        pending: !!e.protocol && self && e.status === "pending",
        failed: !!e.protocol && self && (e.status === "rejected" || e.status === "aborted"),
        channelID: null,
        self,
        mentioned: false,
    };
}

// storedDMMode maps a local DM record to its badge mode. A verified record
// without a mode predates the field, so it was end-to-end. DMs are end-to-end
// only: a record verified as "server" (the retired fallback) shows as
// downgraded, never as verified. An unverified legacy record keeps the text
// the user already saw, under a warning badge.
function storedDMMode(e) {
    if (!e.enc_verified) return encModeFrom(false, e.enc_mode);
    if (!e.enc_mode || e.enc_mode === "e2e") return "e2e";
    return e.enc_mode === "server" ? DOWNGRADED : "";
}

// ensureDMHistory replays a peer's log into its store once per session. It
// runs in FRONT of whatever already arrived live, so a message that landed
// before the tab was opened stays newest instead of being replayed twice.
async function ensureDMHistory(uid) {
    const key = "dm:" + uid;
    const st = getStore(key);
    if (st.loaded || st.loading || !uid) return false;
    const scope = captureScope(readDMHistoryScope);
    const owner = getDMOwner();
    const attempt = Symbol();
    st.dmLoadAttempt = attempt;
    const current = () => dmHistoryScopeIsCurrent(scope) && store.get(key) === st && st.dmLoadAttempt === attempt;
    st.loading = true;
    try {
        const rows = await callDMHistory(owner, "DMHistoryLoadForContext", uid);
        // A read may already hold the deleted file's contents. Wait for an
        // overlapping clear: success invalidates this load, failure keeps it.
        if (st.dmClearPending) await st.dmClearPending;
        if (!current()) return false;
        const nick = peerName(uid);
        const liveKey = (m) => m.clientMsgID ? JSON.stringify([m.protocol || "", m.protocol ? m.fromUID : "", m.clientMsgID]) : "";
        const live = new Set(st.msgs.map(liveKey).filter(Boolean));
        const older = (rows || []).map((e) => dmMsg(e, nick))
            .filter((m) => !m.clientMsgID || !live.has(liveKey(m)));
        st.msgs = older.concat(st.msgs);
        const tab = pmTabs.get(uid);
        if (tab) tab.nick = knownPeerName(uid);
        renderTabs();
        const max = V().state.settings?.chat_max_lines || 200;
        while (st.msgs.length > Math.max(max, PAGE)) st.msgs.shift();
    } catch (e) {
        if (!current()) return false;
        V().toast(t("chat.dmUnavailable", { error: String(e) }), "warn");
    } finally {
        if (current()) {
            st.loading = false;
            st.loaded = true;
        }
    }
    return true;
}

// dmRecord writes one DM — sent or received — to the peer's sealed log. The
// Go layer de-duplicates on client_msg_id, so an offline spool replayed after
// a reconnect does not double the conversation.
function dmRecord(peer, nick, m) {
    if (!peer) return;
    const scope = captureScope(readDMHistoryScope);
    const st = store.get("dm:" + peer);
    const tab = pmTabs.get(peer);
    const owner = getDMOwner();
    const reportFailure = err => {
        if (!dmHistoryScopeIsCurrent(scope) || store.get("dm:" + peer) !== st || pmTabs.get(peer) !== tab || !err || dmPersistWarned) return;
        dmPersistWarned = true;
        V().toast(t("chat.dmNotSaved", { error: String(err) }), "warn");
    };
    callDMHistory(owner, "DMHistoryAppendForContext", peer, knownPeerName(peer, nick), {
        from_unique_id: m.fromUID,
        from_nickname: m.from,
        body: m.text,
        sent_at: Math.floor(m.ts / 1000),
        self: m.self,
        client_msg_id: m.clientMsgID,
        offline: m.offline,
        enc_verified: m.encVerified,
        enc_mode: m.encVerified ? m.encMode : encModeFrom(false, m.encMode),
    }).then(reportFailure).catch(reportFailure);
}

// clearPMHistory is the ONLY path that destroys a conversation, and it says so
// before it does: the server never had a copy to fall back on (122).
async function clearPMHistory(uid, nick) {
    const key = "dm:" + uid;
    const st = getStore(key);
    if (st.dmClearAttempt) return;
    const scope = captureScope(readDMHistoryScope);
    const owner = getDMOwner();
    const tab = pmTabs.get(uid);
    const attempt = Symbol();
    st.dmClearAttempt = attempt;
    let finishClear;
    const current = () => dmHistoryScopeIsCurrent(scope) && store.get(key) === st && pmTabs.get(uid) === tab && st.dmClearAttempt === attempt;
    try {
        const confirmed = await confirmDialog({
            title: t("chat.deleteHistoryTitle"),
            message: t("chat.dmDeleteHelp", { name: nick }),
            confirmLabel: t("chat.deleteHistoryConfirm"),
            danger: true,
            serverScoped: true,
        });
        if (!confirmed || !current()) return;
        const cleared = new Set(st.msgs);
        st.dmClearPending = new Promise(resolve => { finishClear = resolve; });
        const err = await callDMHistory(owner, "DMHistoryClearForContext", uid);
        if (!current()) return;
        if (err) throw new Error(err);
        st.msgs = st.msgs.filter(message => !cleared.has(message));
        st.dmLoadAttempt = null;
        st.loading = false;
        st.loaded = true;
        if (view.kind === "dm" && view.uid === uid) renderView();
        V().toast(t("chat.dmDeleted", { name: nick }), "info");
    } catch (err) {
        if (current()) V().toast(t("chat.dmClearFailed", { error: String(err) }), "warn");
    } finally {
        if (st.dmClearAttempt === attempt) {
            st.dmClearAttempt = null;
            st.dmClearPending = null;
        }
        finishClear?.();
    }
}

// restorePMTabs gives conversations with a stored log their tab back, so a
// restart shows them without waiting for the peer to speak first.
async function restorePMTabs() {
    const scope = captureScope(readDMHistoryScope);
    const owner = getDMOwner();
    const attempt = Symbol();
    dmRestoreAttempt = attempt;
    let peers = [];
    const saved = V().state.activeTabID ? tabLayoutFor(owner) : null;
    try {
        peers = await callDMHistory(owner, "DMHistoryPeersForContext");
        await saved?.loading;
    } catch {
        return; // no local storage path yet
    }
    if (!dmHistoryScopeIsCurrent(scope) || dmRestoreAttempt !== attempt) return;
    let added = false;
    const pinned = new Set(saved?.layout.pinned.filter(key => key.startsWith("dm:")).map(key => key.slice(3)) || []);
    const peerByID = new Map((peers || []).map(peer => [peer.unique_id, peer]));
    const members = new Map((V().state.clients || []).map(member => [member.unique_id, member]));
    const restored = [...pinned].map(uid => ({
        unique_id: uid, nickname: members.get(uid)?.nickname || peerByID.get(uid)?.nickname || saved.layout.names?.["dm:" + uid],
    })).concat((peers || []).filter(peer => !pinned.has(peer.unique_id)).slice(0, DM_RESTORE_TABS));
    for (const p of restored) {
        if (!p.unique_id || pmTabs.has(p.unique_id) || closedDMPeers.has(p.unique_id)) continue;
        pmTabs.set(p.unique_id, {
            uid: p.unique_id, nick: knownPeerName(p.unique_id, p.nickname),
            unread: 0, offline: false, pendingRead: "",
        });
        added = true;
    }
    if (added) renderTabs();
}

function scrollToBottom() {
    const log = $("chat-log");
    atBottom = true;
    log.scrollTop = log.scrollHeight;
}

function resetNewCount() {
    newCount = 0;
    $("chat-newpill").classList.add("hidden");
}

function showNewPill() {
    const pill = $("chat-newpill");
    pill.textContent = t(newCount === 1 ? "chat.newOne" : "chat.newMany", { count: newCount }) + " ↓";
    pill.setAttribute("aria-label", t("chat.jumpNew", { count: newCount }));
    pill.classList.remove("hidden");
}

function captureScrollAnchor(log) {
    if (atBottom) return null;
    const top = log.getBoundingClientRect().top;
    const row = [...log.querySelectorAll(".msg[data-scroll-key]:not(.hidden)")].find(el => el.getBoundingClientRect().bottom > top);
    return row ? { id: row.dataset.scrollKey, offset: row.getBoundingClientRect().top - top, scrollTop: log.scrollTop } : null;
}

function restoreScrollAnchor(log, anchor) {
    if (!anchor) return;
    const row = [...log.querySelectorAll(".msg[data-scroll-key]")].find(el => el.dataset.scrollKey === anchor.id);
    if (row) log.scrollTop += row.getBoundingClientRect().top - log.getBoundingClientRect().top - anchor.offset;
    else log.scrollTop = anchor.scrollTop;
}

function renderView(keepScrollFrom, { suppressMarkRead = false } = {}) {
    const log = $("chat-log");
    const anchor = log.dataset.viewKey === activeKey() ? captureScrollAnchor(log) : null;
    log.innerHTML = "";
    const key = activeKey();
    log.dataset.viewKey = key;
    const st = getStore(key);

    renderTyping(); // (120) drop the previous scope's indicator before anything else

    if (renderForumChannel(log, activeChannelID(), () => {
        if (activeKey() === key) renderView();
    })) { updateHeader(); return; }

    if (view.kind === "channel" && !V().state.myChannelID) {
        const hint = document.createElement("div");
        hint.className = "empty-state";
        hint.textContent = t(V().state.channels.some(ch => ch.SystemKind === "home") ? "chat.lobbyDisabled" : "chat.joinEmpty");
        log.appendChild(hint);
        updateHeader();
        return;
    }

    if (view.kind === "dm") {
        // (122) the local sealed log is this conversation's whole history.
        if (!st.loaded && !st.loading) {
            const scope = captureScope(readDMHistoryScope);
            ensureDMHistory(view.uid).then((applied) => {
                if (applied && dmHistoryScopeIsCurrent(scope) && activeKey() === key && store.get(key) === st) renderView();
            });
        }
    } else {
        if (!st.loaded && !st.loading) {
            const scope = captureScope(readChatServerScope);
            ensureHistory(key).then(() => {
                if (chatServerIsCurrent(scope) && activeKey() === key && store.get(key) === st) renderView();
            });
        }
        if (st.end && st.loaded && st.msgs.length) {
            const end = document.createElement("div");
            end.className = "chat-history-end";
            end.textContent = t("chat.historyStart");
            log.appendChild(end);
        }
        // (103) the server capped this page's key bundle, so rows in it stayed
        // sealed for a TRANSIENT reason. Left unsaid, a ⚠ placeholder reads as
        // "you will never see this" instead of "ask again".
        if (st.truncated) {
            const gap = document.createElement("div");
            gap.className = "chat-history-gap";
            gap.textContent = t("chat.historyGap");
            gap.title = t("chat.historyRetry");
            log.appendChild(gap);
        }
    }

    // (121) "— new messages —" divider at the first message newer than the
    // persisted last-read pointer (fresh loads of a channel view).
    const lastRead = lastReadFor(key);
    let dividerPlaced = false;

    for (const m of st.msgs) {
        if (!dividerPlaced && lastRead !== null && m.id > lastRead && !m.self) {
            const div = document.createElement("div");
            div.className = "chat-new-divider";
            div.textContent = t("polish.newMessages");
            log.appendChild(div);
            dividerPlaced = true;
        }
        log.appendChild(renderMsg(m));
    }

    applySearchFilter();
    updateHeader();
    renderThreadPanel(); // (108) the open chain follows the loaded window

    if (anchor) {
        restoreScrollAnchor(log, anchor);
    } else if (keepScrollFrom !== undefined) {
        log.scrollTop = log.scrollHeight - keepScrollFrom;
    } else {
        scrollToBottom();
        resetNewCount();
    }
    if (!suppressMarkRead && !anchor) markRead(key);
}

function lastReadFor(key) {
    if (!key.startsWith("ch:")) return null;
    const lr = V().state.settings?.last_read_channels;
    if (!lr) return null;
    const v = lr[key.slice(3)];
    return v ? Number(v) : null;
}

function markRead(key) {
    if (isPrivateGroupActive()) return;
    if (!key.startsWith("ch:")) return;
    const st = getStore(key);
    const newest = st.msgs.length ? st.msgs[st.msgs.length - 1].id : 0;
    if (!newest) return;
    const s = V().state.settings;
    if (!s) return;
    s.last_read_channels = s.last_read_channels || {};
    if ((s.last_read_channels[key.slice(3)] || 0) >= newest) return;
    s.last_read_channels[key.slice(3)] = newest;
    persistSettings();
}

// ---------------------------------------------------------------------------
// History scrollback (103)
// ---------------------------------------------------------------------------

async function ensureHistory(key) {
    const st = getStore(key);
    if (st.loaded || st.loading || !key.startsWith("ch:")) return;
    st.loading = true;
    ensurePins(key); // (109) pin state for the 📌 toggle, alongside the first page
    try {
        await loadPage(key, 0);
        st.loaded = true;
    } catch {
        // History unavailable (old server / no permission) — live chat works.
        st.loaded = true;
        st.end = true;
    } finally {
        st.loading = false;
    }
}

async function loadPage(key, beforeID) {
    const scope = captureScope(readChatServerScope);
    const st = getStore(key);
    const before = new Map(st.msgs.map(message => [message, {
        version: message.version, deleted: message.deleted, reactionRevision: message.reactionRevision,
    }]));
    const chID = Number(key.slice(3));
    const resp = await app().ChatHistoryForTab(scope.tabID, chID, beforeID, PAGE);
    if (!chatServerIsCurrent(scope) || store.get(key) !== st) return 0;
    const page = (resp.messages || []).map((x) => attribute(normalize(x, chID))).reverse(); // newest-first → oldest-first
    if (beforeID === 0) {
        // Only changes received during this request outrank its snapshot.
        // Keep the loaded range contiguous so older-page cursors cannot skip
        // a gap left by an old cached message from an unsubscribed channel.
        const firstID = page[0]?.id || 0;
        const contentChanged = m => !before.has(m) || before.get(m).version !== m.version || before.get(m).deleted !== m.deleted;
        const reactionsChanged = m => !before.has(m) || before.get(m).reactionRevision !== m.reactionRevision;
        const currentByID = new Map(st.msgs.map(m => [m.id, m]));
        const historyIDs = new Set(page.map(m => m.id));
        const concurrent = st.msgs.filter(m => !historyIDs.has(m.id) && (!firstID || m.id >= firstID) && (contentChanged(m) || reactionsChanged(m)));
        const merged = page.map(history => {
            const live = currentByID.get(history.id);
            if (!live) return history;
            const body = contentChanged(live) && !history.deleted && (live.deleted || live.version >= history.version) ? live : history;
            const reactions = reactionsChanged(live) ? live : history;
            return { ...body, reactions: reactions.reactions, reactionRevision: reactions.reactionRevision, renderKey: live.renderKey };
        });
        st.msgs = merged.concat(concurrent)
            .sort((a, b) => a.id && b.id ? a.id - b.id : a.ts - b.ts);
        st.truncated = !!resp.truncated;
    } else {
        st.msgs = page.concat(st.msgs);
        // (103) a gap anywhere in the loaded window is still a gap in it, so
        // one truncated page keeps the notice up until the scope is reloaded.
        st.truncated = st.truncated || !!resp.truncated;
    }
    st.end = page.length < PAGE;
    return page.length;
}

async function maybeLoadOlder() {
    if (view.kind === "dm") return;
    const scope = captureScope(readChatServerScope);
    const key = activeKey();
    const st = getStore(key);
    if (st.loading || st.end || !st.loaded || !st.msgs.length) return;
    st.loading = true;
    const log = $("chat-log");
    const spinner = document.createElement("div");
    spinner.className = "chat-spinner";
    spinner.textContent = t("chat.loadingOlder");
    log.insertBefore(spinner, log.firstChild);
    const prevH = log.scrollHeight;
    try {
        await loadPage(key, st.msgs[0].id);
    } catch { /* keep what we have */ }
    st.loading = false;
    if (!chatServerIsCurrent(scope) || activeKey() !== key || store.get(key) !== st) return;
    // Anchor the viewport: content was prepended, so the old scrollHeight
    // as "distance from the new bottom" lands back on the same message.
    renderView(prevH);
}

// ---------------------------------------------------------------------------
// Live incoming chat (addChat entry point, called from main.js)
// ---------------------------------------------------------------------------

// notificationLine is the toast / OS-notification text for a channel message.
// previewLine never carries an attachment's storage key or a refused or
// unopened body: files show their name, refusals their translated text.
export function notificationLine(m) {
    return (m.from || "someone") + ": " + previewLine(m);
}

// keywordText is the text keyword highlights match against: the same
// sanitized body previewLine shows (attachment references reduced to their
// file name, so a keyword never matches inside a storage key), without the
// 80-character cut. A refused, unopened, downgraded or deleted body has no
// verified user text and matches nothing — not even words of its notice.
export function keywordText(m) {
    // A downgraded DM's body is shown but never counts as verified content.
    if (!m || m.deleted || m.encMode === DOWNGRADED || encryptionBadge(m).bodyKey) return "";
    return String(m.text ?? "")
        .replace(/\[file:([^\]]+)\]/g, (_, capture) => " " + parseFileRef(capture).name + " ")
        .replace(/\s+/g, " ")
        .trim();
}

export function addChat(d) {
    const st = V().state;
    const m = normalize(d);
    m.self = m.fromUID ? m.fromUID === sessionUserID(st) : m.from === st.myNickname;
    clearTyping(m.fromUID); // (120) their message landed; they are done typing

    if (m.direct) {
        return routeDM(d, m);
    }

    const chID = m.channelID ?? 0;
    const key = "ch:" + chID;
    const position = pushMsg(key, m);
    if (position === "duplicate") return false;
    const flags = mentionFlags(m, sessionUserID(st));
    const directMention = flags.direct && !m.self;
    const roleMention = flags.role && !m.self;
    const level = st.settings?.chat_notification_level || "all";
    let announcementAllowed = false;
    let announcementEvent = "channel_message";
    let announcementContext = { channelID: m.channelID, className: "messages" };
    let notification = null;
    if (directMention || roleMention) {
        m.mentioned = true; // (106) accent highlight
    } else if (!m.self) {
        // (388) keyword highlights: whole-word match gets mention treatment.
        const kw = window.__noxaNotify?.matchKeyword(keywordText(m));
        const keywordLevelAllowed = level === "all" || level === "channel_mentions";
        if (kw) {
            m.mentioned = true;
            if (keywordLevelAllowed) {
                notification = {
                    event: "keyword",
                    text: `[${kw}] ` + notificationLine(m),
                    context: { channelID: m.channelID, className: "mentions", kind: "warn" },
                };
            }
        }
    }
    if (!m.self) {
        const allowed = level === "all" || (level === "channel_mentions" && (directMention || roleMention)) || (level === "role_mentions" && roleMention);
        // A direct/role mention outranks a keyword; an otherwise ordinary
        // message is the fallback. Dispatch one notification per message so
        // the new per-event sound cues never overlap for the same chat line.
        if (allowed && (directMention || roleMention)) {
            const event = directMention || roleMention ? "mention" : "channel_message";
            const context = { channelID: m.channelID, className: directMention || roleMention ? "mentions" : "messages" };
            notification = {
                event,
                text: notificationLine(m),
                context: { ...context, kind: "warn" },
            };
        } else if (!notification && level === "all") {
            notification = {
                event: "channel_message",
                text: notificationLine(m),
                context: { channelID: m.channelID, className: "messages", kind: "info" },
            };
        }
    }
    if (notification) {
        const { event, text, context } = notification;
        const eventAnnouncement = chatAnnouncementAllowed(event, context, key);
        if (eventAnnouncement) {
            announcementAllowed = true;
            announcementEvent = event;
            announcementContext = context;
        }
        window.__noxaNotify?.notify(event, text, { ...context, uid: m.fromUID, announce: false,
            reference: { kind: "channel", channel_id: chID, message_id: m.id || 0 } });
    }
    const channelMuted = !!window.__noxaNotify?.channelOverride?.(chID)?.muted;
    const live = key === activeKey() && !isPrivateGroupActive();
    if (live) {
        if (position === "append") appendLive(m);
        else renderView();
    } else if (!m.self && !channelMuted) {
        // (104) unread badge for a non-selected channel. A muted channel (387)
        // suppresses the badge too — silencing only the sound would still nag
        // through the tree.
        const u = unread.get(chID) || { n: 0, mention: false };
        u.n++;
        u.mention = u.mention || directMention || roleMention;
        u.mentions = (u.mentions || 0) + Number(directMention || roleMention);
        u.replies = (u.replies || 0) + Number(!m.self && !!m.replyToID && !!findMsg(m.replyToID)?.self);
        if (!st.replayingTabID) u.unreadAt = Date.now();
        unread.set(chID, u);
        V().renderTree();
        renderTabs();
    }
    return {
        added: true,
        // Shown in the open chat rather than badged (tray unseen counter).
        live: live && !m.self && !channelMuted,
        announcement: eligibleChatAnnouncement(
            m, key, announcementEvent, announcementContext, announcementAllowed),
    };
}

function pushMsg(key, m) {
    const st = getStore(key);
    const duplicate = st.msgs.find((existing) => (m.id && existing.id === m.id) ||
        (m.clientMsgID && existing.clientMsgID === m.clientMsgID && existing.self === m.self &&
            (existing.protocol || "") === (m.protocol || "") && (!m.protocol || existing.fromUID === m.fromUID)));
    if (duplicate) {
        upgradeEncryption(duplicate, m);
        return "duplicate";
    }
    const index = m.id ? st.msgs.findIndex((existing) => existing.id && existing.id > m.id) : -1;
    if (index >= 0) st.msgs.splice(index, 0, m);
    else st.msgs.push(m);
    const max = V().state.settings?.chat_max_lines || 200;
    while (st.msgs.length > Math.max(max, PAGE)) st.msgs.shift();
    return index >= 0 ? "insert" : "append";
}

// upgradeEncryption merges the encryption evidence of a second copy of the
// same message (a live echo after a history replay, or the reverse) so the
// first copy never keeps a weaker badge than this client actually verified.
// A first copy that stayed unopened (UNOPENED_BODIES placeholder, e.g. the key
// arrived later) takes the verified copy's body as well; a verified body is
// never replaced by anything else.
export function upgradeEncryption(existing, m) {
    if (existing.deleted || !m.encVerified) return false;
    const opened = UNOPENED_BODIES.has(existing.text) && typeof m.text === "string" && !UNOPENED_BODIES.has(m.text);
    if (!opened && (existing.encVerified || existing.text !== m.text)) return false;
    if (opened) {
        existing.text = m.text;
        if (m.e2e) existing.e2e = true;
    }
    existing.enc = true;
    existing.encVerified = true;
    existing.encMode = m.encMode;
    existing.localUnverified = false;
    // The Messages list preview follows the opened body.
    if (opened && existing.direct) window.dispatchEvent(new CustomEvent("noxa-direct-changed"));
    if (!existing.renderKey) return true;
    const el = document.querySelector(`#chat-log .msg[data-scroll-key="${CSS.escape(existing.renderKey)}"]`);
    if (el) el.replaceWith(renderMsg(existing));
    return true;
}

// trimLog keeps at most max message rows in the chat log. Other children
// (the Messages view's .dm-day separators) do not count against the limit,
// and a separator left without a message after it is removed with them.
export function trimLog(log, max) {
    const isSeparator = el => !!el?.classList?.contains("dm-day");
    const rows = [...log.children].filter(child => !isSeparator(child));
    for (const row of rows.slice(0, Math.max(0, rows.length - max))) row.remove();
    for (const separator of [...log.children].filter(isSeparator)) {
        const next = separator.nextElementSibling;
        if (!next || isSeparator(next)) separator.remove();
    }
}

function appendLive(m) {
    const log = $("chat-log");
    const anchor = captureScrollAnchor(log);
    log.appendChild(renderMsg(m));
    const max = V().state.settings?.chat_max_lines || 200;
    trimLog(log, max);
    applySearchFilter();
    restoreScrollAnchor(log, anchor);
    refreshThreadFor(m.id); // (108) a reply joins the open chain immediately
    if (atBottom) {
        scrollToBottom();
        markRead(activeKey());
    } else {
        newCount++;
        showNewPill(); // (134) scroll lock: user is reading scrollback
    }
}

// --- DM routing, tabs, offline badge, receipts (122-124) -----------------------

function routeDM(d, m) {
    const st = V().state;
    const peer = directPeer(d, m);
    if (!peer) return false;
    const tab = pmTabs.get(peer) || { uid: peer, nick: knownPeerName(peer, m.self ? "" : m.from), unread: 0, offline: false, pendingRead: "" };
    if (!pmTabs.has(peer)) pmTabs.set(peer, tab);
    tab.nick = knownPeerName(peer, m.self ? "" : m.from);

    const key = "dm:" + peer;
    if (pushMsg(key, m) === "duplicate") return false;
    if (!m.self && !m.offline) {
        // previewLine never puts an attachment's storage key or a refused
        // body into a notification: files show their name, refusals their text.
        window.__noxaNotify?.notify("dm", (m.from || "someone") + ": " + previewLine(m),
            { uid: peer, className: "messages", kind: "info", announce: false,
                reference: { kind: "dm", peer_id: peer, client_message_id: m.clientMsgID || "", local_seq: m.localSeq || 0 } });
    }
    dmRecord(peer, tab.nick, m); // (122) both directions, so a restart replays the thread
    applyPendingDMChanges(peer, m); // after recording: the Go layer applies its own pending copy

    if (m.offline && !m.self) {
        // (123) offline-spooled DM: tab badge + batched toast.
        tab.offline = true;
        const b = offlineBatch.get(peer) || { n: 0, nick: m.from, timer: null };
        b.n++;
        if (b.timer) clearTimeout(b.timer);
        b.timer = setTimeout(() => {
            offlineBatch.delete(peer);
            const summary = t("chat.offlineBatch", { count: b.n, name: b.nick });
            window.__noxaNotify?.notify("dm", summary,
                { uid: peer, className: "messages", kind: "info", announce: false,
                    reference: { kind: "dm", peer_id: peer, client_message_id: m.clientMsgID || "", local_seq: m.localSeq || 0 } });
            if (chatAnnouncementAllowed("dm", { uid: peer, className: "messages" }, key)) {
                V().announceLive(summary);
            }
        }, 700);
        offlineBatch.set(peer, b);
    }

    if (!m.self && m.clientMsgID) {
        // (124) delivery receipt immediately; read receipt when the tab is
        // visible and focused (otherwise queued for activation/focus).
        const scope = captureScope(readChatServerScope);
        void sendChatSignal(() => app().SendChatDeliveredForTab(scope.tabID, m.fromUID, m.clientMsgID));
        tab.pendingRead = { id: m.clientMsgID, scope };
        void sendPendingReads();
    }

    const live = key === activeKey() && !isPrivateGroupActive();
    if (live) {
        appendLive(m);
    } else if (!m.self) {
        tab.unread++;
        if (!st.replayingTabID) tab.unreadAt = Date.now();
    }
    renderTabs();
    return {
        added: true,
        live: live && !m.self,
        incomingDM: !m.self && !m.offline,
        announcement: eligibleChatAnnouncement(
            m,
            key,
            "dm",
            { uid: peer, className: "messages" },
            chatAnnouncementAllowed("dm", { uid: peer, className: "messages" }, key),
        ),
    };
}

async function sendChatSignal(action) {
    try { await action(); }
    catch { /* Typing and delivery are best-effort signals. */ }
}

async function sendPendingReads() {
    if (view.kind !== "dm" || !chatSurfaceVisible("dm:" + view.uid)) return;
    const tab = pmTabs.get(view.uid);
    if (!tab) return;
    const queue = tab.readQueue ||= [];
    if (tab.pendingRead) {
        const pending = tab.pendingRead;
        if (chatServerIsCurrent(pending.scope) && !queue.some(entry => entry.id === pending.id)) queue.push(pending);
        tab.pendingRead = "";
    }
    if (tab.readAttempt) return;
    while (queue.length && !chatServerIsCurrent(queue[0].scope)) queue.shift();
    const entry = queue[0];
    if (!entry) return;
    const attempt = { entry };
    tab.readAttempt = attempt;
    const current = () => pmTabs.get(tab.uid) === tab && tab.readAttempt === attempt && chatServerIsCurrent(entry.scope);
    let sent = false;
    try {
        const error = await app().SendChatReadForTab(entry.scope.tabID, tab.uid, entry.id);
        if (!current() || error) return;
        sent = true;
        if (queue[0] === entry) queue.shift();
    } catch { /* Retain this receipt for a later focus/message trigger. */ }
    finally {
        if (tab.readAttempt === attempt) tab.readAttempt = null;
        if (sent && pmTabs.get(tab.uid) === tab && chatServerIsCurrent(entry.scope) && view.kind === "dm" && view.uid === tab.uid && (queue.length || tab.pendingRead)) {
            void sendPendingReads();
        }
    }
}

export function onDelivered(d) {
    if (d.client_msg_id) {
        receipts.set(d.client_msg_id, "delivered");
        updateReceiptTick(d.client_msg_id);
    }
}

export function onRead(d) {
    if (d.client_msg_id) {
        receipts.set(d.client_msg_id, "read");
        updateReceiptTick(d.client_msg_id);
    }
}

function updateReceiptTick(cmid) {
    const r = receipts.get(cmid);
    document.querySelectorAll(`#chat-log .msg-receipt[data-cmid="${CSS.escape(cmid)}"]`).forEach((el) => {
        el.textContent = r === "read" ? "✓✓" : "✓";
        el.classList.toggle("read", r === "read");
    });
}

// ---------------------------------------------------------------------------
// Live edit/delete/pin/reaction events
// ---------------------------------------------------------------------------

export function onChatEdited(d) {
    // The Go layer unseals chat_edited before emitting it, so the new body is
    // on the event itself: no history refetch, and an edit further back than
    // one page is covered like any other (91-135).
    const m = findMsg(d.message_id);
    if (!m) return;
    m.text = d.body ?? "";
    // The native layer marks an edit body it opened itself; a refused or
    // sealed edit arrives as a placeholder and draws the warning instead.
    if (d.enc_verified) { m.enc = true; m.encVerified = true; m.encMode ||= m.direct ? "" : "server"; }
    m.edited = true;
    m.version = Number(d.version) || (m.version + 1);
    m.mentioned = !m.self && mentionsMe(m.text); // an edit can add or drop my name
    threadCache.clear(); // an edit can add or remove the reply prefix (108)
    refreshMsgEl(m);
    refreshThreadFor(m.id);
}

export function onChatDeleted(d) {
    const m = findMsg(d.message_id);
    if (!m) return;
    m.deleted = true;
    m.text = "";
    threadCache.clear();
    refreshMsgEl(m); // tombstone style (102)
    refreshThreadFor(m.id);
}

export function onChatPinned(d, pinned) {
    V().sysMsg(pinned ? t("chat.wasPinned") : t("chat.wasUnpinned"));
    // (109) keep the per-scope pin set current so the 📌 hover button toggles.
    const key = "ch:" + (Number(d.channel_id) || 0);
    const id = Number(d.message_id);
    // Replay deltas over the initial snapshot without dropping unrelated pins.
    pendingPinChanges.get(key)?.set(id, pinned);
    if (pinned) pinSet(key).add(id);
    else pinSet(key).delete(id);
    const m = findMsg(id);
    if (m) refreshMsgEl(m);
    if (pinsPanel) loadPinsPanel(); // refresh the open panel (109)
}

export function onChatReaction(d) {
    const m = findMsg(d.message_id);
    if (!m) return;
    // The event carries the authoritative counts; the own-highlight only
    // tracks toggles made in this session (history has counts only, 97).
    m.reactions = d.reactions || {};
    m.reactionRevision = (m.reactionRevision || 0) + 1;
    if (d.by && d.by === sessionUserID(V().state) && typeof d.added === "boolean" && typeof d.emoji === "string" && d.emoji) {
        let own = myReactions.get(m.id);
        if (!own) { own = new Set(); myReactions.set(m.id, own); }
        if (d.added) own.add(d.emoji);
        else own.delete(d.emoji);
        m.reactionOwnRevision ??= new Map();
        m.reactionOwnRevision.set(d.emoji, (m.reactionOwnRevision.get(d.emoji) || 0) + 1);
    }
    refreshReacts(m);
}

export function onEmojiAdded() {
    customDirty = true; // re-list on next use (95)
}

// ---------------------------------------------------------------------------
// Typing indicators (120)
// ---------------------------------------------------------------------------
// Wire contract (internal/server/chatx.go handleTyping / typingEvent):
//   send    App.SendTyping(channelID int64, toUniqueID string) -> error string
//           MsgTyping{channel_id, to_unique_id}; exactly one is set, neither
//           set = global scope.
//   receive event envelope {type:"typing", data:{client_id, unique_id,
//           nickname, channel_id}}
// Two consequences of that shape, both handled below. A channel/global relay
// reaches the SENDER too, so an event carrying my own identity is dropped.
// And the event has no to_unique_id: a DM indicator carries channel_id 0 and
// is therefore indistinguishable from a global one, so it lands on the global
// scope — the person who sees it is the DM recipient either way.
// There is no stop message: the relay is fire-and-forget, so the sender pings
// while composing and every receiver expires the entry on its own.

const TYPING_DEBOUNCE_MS = 300;
const TYPING_PING_MS = 2000; // matches the server's per-scope throttle
const TYPING_TTL_MS = 4500; // must exceed the ping interval or it flickers
const TYPING_IDLE_MS = 3000;

// typingScopeOf returns [channelID, toUniqueID] for the active view.
function typingScopeOf() {
    if (view.kind === "dm") return [0, view.uid];
    if (view.kind === "global") return [0, ""];
    return [activeChannelID() || 0, ""];
}

// resetTypingOut ends the local composing session: the next keystroke pings
// immediately instead of waiting out the throttle. Called on send, on blur,
// on an emptied input, on a scope switch and on idle (120).
function resetTypingOut() {
    typingSentAt = 0;
    typingScope = "";
    if (typingIdle) {
        clearTimeout(typingIdle);
        typingIdle = null;
    }
    if (typingDebounce) {
        clearTimeout(typingDebounce);
        typingDebounce = null;
    }
}

// noteTyping pings the relay while the user composes. An empty input is not
// typing, and the ping is throttled so a fast typist sends one frame per
// TYPING_PING_MS rather than one per keystroke.
function noteTyping() {
    if (!$("chat-text").value.trim()) {
        resetTypingOut();
        return;
    }
    const captured = captureScope(readSendScope);
    const [chID, uid] = typingScopeOf();
    const send = () => {
        if (!scopeIsCurrent(captured, readSendScope)) return;
        typingDebounce = null;
        const scope = chID + "|" + uid;
        const now = Date.now();
        if (scope !== typingScope || now - typingSentAt >= TYPING_PING_MS) {
            typingScope = scope;
            typingSentAt = now;
            void sendChatSignal(() => app().SendTypingForTab(captured.tabID, chID, uid));
        }
    };
    if (!typingDebounce) typingDebounce = setTimeout(send, TYPING_DEBOUNCE_MS);
    if (typingIdle) clearTimeout(typingIdle);
    typingIdle = setTimeout(resetTypingOut, TYPING_IDLE_MS);
}

// onTyping records one relayed indicator, keyed exactly on the scope the
// event carries: its channel_id, or the sender's DM when it is direct.
export function onTyping(d) {
    const st = V().state;
    const uid = d.unique_id || "";
    if (!uid || uid === sessionUserID(st) || d.client_id === st.myClientID) return;
    // A relay addressed to this user carries direct: true and belongs to that
    // DM; older servers omit it, which keeps the indicator in the global scope.
    const key = d.direct ? "dm:" + uid : "ch:" + (Number(d.channel_id) || 0);
    if (!typers.has(key)) typers.set(key, new Map());
    typers.get(key).set(uid, { nick: d.nickname || uid, expires: Date.now() + TYPING_TTL_MS });
    startTypingSweep();
    renderTyping();
}

// clearTyping drops a user's indicator early — their message arrived, so they
// are demonstrably no longer typing it. It sweeps every scope because a DM
// indicator is filed under the global scope (see the contract note above), so
// the arriving DM would otherwise leave it hanging until it expires.
function clearTyping(uid) {
    if (!uid) return;
    let hit = false;
    for (const m of typers.values()) hit = m.delete(uid) || hit;
    if (hit) renderTyping();
}

// startTypingSweep runs one interval for every scope, stopping itself once
// nothing is pending so an idle client holds no timer.
function startTypingSweep() {
    if (typingTimer) return;
    typingTimer = setInterval(() => {
        const now = Date.now();
        let live = 0;
        for (const [key, m] of typers) {
            for (const [uid, t] of m) if (t.expires <= now) m.delete(uid);
            if (m.size === 0) typers.delete(key);
            else live += m.size;
        }
        renderTyping();
        if (live === 0) {
            clearInterval(typingTimer);
            typingTimer = null;
        }
    }, 1000);
}

function renderTyping() {
    const el = $("chat-typing");
    if (!el) return;
    // "channel view without a channel" shares the store key with global, so it
    // must not borrow global's typers along with it.
    const inNoChannel = view.kind === "channel" && !V().state.myChannelID;
    const m = inNoChannel ? null : typers.get(activeKey());
    const names = m ? [...m.values()].map((t) => t.nick) : [];
    el.classList.toggle("hidden", names.length === 0);
    if (names.length === 0) {
        el.textContent = "";
        return;
    }
    el.textContent = names.length === 1 ? t("chat.typingOne", { name: names[0] })
        : names.length === 2 ? t("chat.typingTwo", { first: names[0], second: names[1] })
        : t("chat.typingMany", { count: names.length });
}

// ---------------------------------------------------------------------------
// Sending (reply ids 601, file uploads 98/100)
// ---------------------------------------------------------------------------

function readSendScope() {
    const scope = $("chat-scope").value;
    const target = scope === "channel" ? String(activeChannelID() || "")
        : scope === "direct" ? directRecipientID() : "";
    return { ...readExportScope(), sendScope: scope, target };
}

function showSendFailure(error, scope, canRetry) {
    const panel = $("chat-send-error");
    panel.querySelector("span").textContent = t("polish.sendFailed", { error: String(error) });
    panel.classList.remove("hidden");
    const retry = $("chat-retry");
    retry.textContent = t("polish.retry");
    retry.hidden = !canRetry();
    retry.onclick = () => {
        if (!scopeIsCurrent(scope, readSendScope) || !canRetry()) {
            panel.classList.add("hidden");
            return;
        }
        void sendMessage();
    };
}

export async function sendMessage() {
    if (activeSend) return;
    const input = $("chat-text");
    const draft = input.value;
    const text = draft.trim();
    if (!text && pendingFiles.length === 0) return;
    if (pendingFiles.length && chatDelivery?.enabled() && !V().state.myClientID) {
        V().toast(t("delivery.textOnly"), "warn"); return;
    }
    $("chat-send-error").classList.add("hidden");
    $("chat-send-error").querySelector(".chat-offline-queue")?.remove();
    const captured = captureScope(readSendScope);
    if (captured.sendScope === "direct" && !captured.target) {
        V().toast(t("chat.chooseRecipient"), "warn");
        $("chat-target").focus();
        return;
    }
    const operation = { scope: captured };
    activeSend = operation;
    $("chat-send").disabled = true;
    const current = () => activeSend === operation && scopeIsCurrent(captured, readSendScope);
    const inputRevision = draftRevision;
    const parentRevision = replyRevision;
    let expectedDraft = draft, expectedParent = parentRevision, failure = null;
    const retryFiles = [];
    const canRetry = () => draftRevision === inputRevision && replyRevision === expectedParent &&
        input.value === expectedDraft && pendingFiles.length === retryFiles.length &&
        pendingFiles.every((file, index) => file === retryFiles[index]);
    const { sendScope: scope, target } = captured;
    const parentID = scope === "direct" ? 0 : (replyTo?.id || 0);
    const files = pendingFiles.splice(0);
    const jobs = files.length ? attachmentQueue.add(files, captured.tabID, async (file, job) => {
        if (!scopeIsCurrent(captured, readSendScope) || activeSend) return;
        const retryOperation = { scope: captured }; activeSend = retryOperation; $("chat-send").disabled = true;
        const retryCurrent = () => activeSend === retryOperation && scopeIsCurrent(captured, readSendScope);
        try {
            const token = await attachmentQueue.upload(job, captured.sendScope === "channel" ? captured.channelID : 0, retryCurrent);
            if (!token || !retryCurrent()) return;
            file.operationID ||= crypto.randomUUID();
            const error = await sendChatOperation(captured.tabID, captured.sendScope, captured.target, token, 0, file.operationID);
            if (!retryCurrent()) return;
            attachmentQueue.finish(job, error);
            if (!error) { pendingFiles = pendingFiles.filter(entry => entry !== file); renderFilePreview(); }
        } catch (error) { if (retryCurrent()) attachmentQueue.finish(job, error); }
        finally { if (activeSend === retryOperation) { activeSend = null; $("chat-send").disabled = false; } }
    }) : [];
    renderFilePreview();
    if (scope === "direct") {
        if (target && !pmTabs.has(target)) openPMKeepView(target);
    }
    if (!files.length && text && chatDelivery && await chatDelivery.ready) {
        const finishStage = async queueOffline => {
            await chatDelivery.stage(captured, text, parentID, queueOffline);
            if (!current()) return;
            rememberSent(text);
            if (replyRevision === parentRevision) clearReply();
            if (draftRevision === inputRevision && input.value === draft) { input.value = ""; resetTypingOut(); }
            $("chat-send-error").classList.add("hidden");
            updateComposerCount();
        };
        const offerQueue = () => {
            const panel = $("chat-send-error");
            panel.querySelector("span").textContent = t("delivery.offline");
            panel.classList.remove("hidden"); $("chat-retry").hidden = true;
            panel.querySelector(".chat-offline-queue")?.remove();
            const queue = document.createElement("button"); queue.type = "button"; queue.className = "chat-offline-queue";
            queue.textContent = t("delivery.queue"); panel.append(queue);
            queue.onclick = async () => {
                if (!scopeIsCurrent(captured, readSendScope) || !canRetry() || activeSend) return;
                activeSend = operation; queue.disabled = true;
                try { await finishStage(true); }
                catch (error) { if (current()) panel.querySelector("span").textContent = t("delivery.failed", { error: String(error) }); }
                finally { if (activeSend === operation) activeSend = null; queue.disabled = false; }
            };
        };
        try {
            if (!V().state.myClientID) offerQueue();
            else await finishStage(false);
        } catch (error) {
            if (current()) {
                if (!V().state.myClientID || /offline|not connected|connection closed/i.test(String(error))) offerQueue();
                else showSendFailure(error, captured, canRetry);
            }
        } finally {
            if (activeSend === operation) { activeSend = null; $("chat-send").disabled = false; }
        }
        return;
    }
    try {
        const chID = scope === "channel" ? captured.channelID : 0;
        for (const [index, f] of files.entries()) {
            const job = jobs[index];
            if (!current()) return;
            if (job.canceled) continue;
            let token;
            try {
                token = await attachmentQueue.upload(job, chID, current);
                if (!token) continue;
            } catch (e) {
                if (!current()) return;
                if (!job.canceled) { failure = e; retryFiles.push(f); }
                continue;
            }
            if (!current()) return;
            try {
                f.operationID ||= crypto.randomUUID();
                const err = await sendChatOperation(captured.tabID, scope, target, token, 0, f.operationID);
                if (!current()) return;
                attachmentQueue.finish(job, err);
                if (err) { failure = err; retryFiles.push(f); }
            } catch (e) {
                if (!current()) return;
                attachmentQueue.finish(job, e);
                failure = e;
                retryFiles.push(f);
            }
        }
        if (!current()) return;
        if (retryFiles.length) {
            pendingFiles = [...retryFiles, ...pendingFiles];
            renderFilePreview();
        }
        if (text) {
            let err = "";
            const operationKey = JSON.stringify(["text", captured.tabID, scope, target, parentID, text]);
            try {
                err = await sendChatOperation(captured.tabID, scope, target, text, parentID, operationIDFor(operationKey));
                if (!err) sendOperations.delete(operationKey);
            } catch (e) {
                failure = e;
                return;
            }
            if (!current()) return;
            if (err) {
                failure = err;
                return;
            }
            rememberSent(text);
            if (replyRevision === parentRevision) {
                clearReply();
                expectedParent = replyRevision;
            }
            if (draftRevision === inputRevision && input.value === draft) {
                input.value = "";
                expectedDraft = "";
                resetTypingOut();
            }
        }
    } finally {
        updateComposerCount();
        if (failure !== null && current()) showSendFailure(failure, captured, canRetry);
        if (activeSend === operation) {
            activeSend = null;
            $("chat-send").disabled = false;
        }
    }
}

function rememberSent(text) {
    if (!text || sentHistory[sentHistory.length - 1] === text) return;
    sentHistory.push(text);
    if (sentHistory.length > 100) sentHistory.shift();
    sentHistoryPos = sentHistory.length;
    sentHistoryDraft = "";
}

function recallSent(e) {
    const input = $("chat-text");
    if (e.key === "ArrowUp" && (input.selectionStart === 0 || sentHistoryPos < sentHistory.length) && sentHistoryPos > 0) {
        e.preventDefault();
        if (sentHistoryPos === sentHistory.length) sentHistoryDraft = input.value;
        draftRevision++;
        input.value = sentHistory[--sentHistoryPos];
        input.selectionStart = input.selectionEnd = input.value.length;
    } else if (e.key === "ArrowDown" && input.selectionEnd === input.value.length && sentHistoryPos < sentHistory.length) {
        e.preventDefault();
        sentHistoryPos++;
        draftRevision++;
        input.value = sentHistoryPos === sentHistory.length ? sentHistoryDraft : sentHistory[sentHistoryPos];
        input.selectionStart = input.selectionEnd = input.value.length;
    }
}
function closeEmojiPanel() {
    if (emojiPanel) {
        emojiPanel.remove();
        emojiPanel = null;
    }
}

function openPMKeepView(uid) {
    pmTabs.set(uid, { uid, nick: knownPeerName(uid), unread: 0, offline: false, pendingRead: "" });
    renderTabs();
}

// --- file staging: paste / drop (98/100) ---------------------------------------

function stageFiles(fileList) {
    const scope = captureScope(readSendScope);
    readStagedFiles(fileList, () => scopeIsCurrent(scope, readSendScope), file => {
        pendingFiles.push(file);
        renderFilePreview();
    });
}

// readStagedFiles applies the chat attachment rules (25 MiB cap, safe unique
// display names, image previews) for every composer, including groups.
export function readStagedFiles(fileList, isCurrent, onFile) {
    for (const f of fileList) {
        if (f.size > ATTACH_MAX_BYTES) {
            V().toast(t("chat.attachmentTooLarge", { name: f.name || t("chat.file"), size: Math.round(ATTACH_MAX_BYTES / 1048576) }), "warn");
            continue;
        }
        const reader = new FileReader();
        reader.onload = () => {
            if (!isCurrent()) return;
            const dataURL = reader.result;
            const rawName = (f.name || "image.png").replace(/[\[\]#]/g, "_");
            const extIdx = rawName.lastIndexOf(".");
            const uniqueId = Date.now().toString(36) + "_" + Math.random().toString(36).slice(2, 6);
            let uniqueName = rawName;
            if (extIdx > 0) {
                uniqueName = rawName.slice(0, extIdx) + "_" + uniqueId + rawName.slice(extIdx);
            } else {
                uniqueName = rawName + "_" + uniqueId;
            }
            onFile({
                name: uniqueName,
                dataBase64: dataURL.split(",")[1] || "",
                isImage: f.type.startsWith("image/"),
                dataURL: f.type.startsWith("image/") ? dataURL : null,
            });
        };
        reader.onerror = () => { if (isCurrent()) V().toast(t("chat.readFileFailed", { name: f.name || t("chat.file") }), "warn"); };
        reader.readAsDataURL(f);
    }
}

function renderFilePreview() {
    const row = $("file-preview-row");
    row.innerHTML = "";
    row.classList.toggle("hidden", pendingFiles.length === 0);
    pendingFiles.forEach((f, i) => {
        const item = document.createElement("div");
        item.className = "file-preview";
        if (f.dataURL) {
            const img = document.createElement("img");
            img.src = f.dataURL;
            img.alt = f.name;
            item.appendChild(img);
        } else {
            const chip = document.createElement("span");
            chip.className = "file-chip";
            chip.textContent = "📎 " + f.name;
            item.appendChild(chip);
        }
        const x = document.createElement("button");
        x.textContent = "✕";
        x.title = t("chat.remove");
        x.onclick = () => {
            pendingFiles.splice(i, 1);
            renderFilePreview();
        };
        item.appendChild(x);
        row.appendChild(item);
    });
}

function insertAtCursor(text, input = $("chat-text")) {
    const main = input === $("chat-text");
    if (main) draftRevision++;
    const pos = input.selectionStart ?? input.value.length;
    input.value = input.value.slice(0, pos) + text + input.value.slice(input.selectionEnd ?? pos);
    input.selectionStart = input.selectionEnd = pos + text.length;
    if (main) updateComposerCount();
    else input.dispatchEvent(new Event("input", { bubbles: true }));
    input.focus();
}

// toggleSharedEmojiPanel opens the chat emoji picker (built-in and server
// emoji) for another composer, such as a private group.
export function toggleSharedEmojiPanel(input, anchor) {
    toggleEmojiPanel({ input, anchor });
}

function toggleEmojiPanel(target = null) {
    if (emojiPanel) {
        closeEmojiPanel();
        return;
    }
    const insert = text => { if (!target || target.input.isConnected) insertAtCursor(text, target?.input); };
    emojiPanel = document.createElement("div");
    const panel = emojiPanel;
    const panelGeneration = V().state.serverGeneration;
    const currentPanel = () => emojiPanel === panel && panelGeneration === V().state.serverGeneration;
    emojiPanel.className = "emoji-panel";
    emojiPanel.onclick = (e) => e.stopPropagation();

    for (const [cat, list] of EMOJI_CATS) {
        const head = document.createElement("div");
        head.className = "emoji-cat";
        head.textContent = t(cat);
        emojiPanel.appendChild(head);
        const grid = document.createElement("div");
        grid.className = "emoji-grid";
        for (const e of list) {
            const b = document.createElement("button");
            b.textContent = e;
            const code = Object.entries(EMOJI).find(([, v]) => v === e)?.[0];
            if (code) b.title = ":" + code + ":";
            b.onclick = () => insert(e);
            grid.appendChild(b);
        }
        emojiPanel.appendChild(grid);
    }

    // Custom server emoji, cached as data URLs.
    const head = document.createElement("div");
    head.className = "emoji-cat";
    head.textContent = t("chat.serverEmoji");
    emojiPanel.appendChild(head);
    // (96) upload is permission-gated server-side, so the button is always
    // offered and a denial comes back as an error frame.
    const up = document.createElement("button");
    up.className = "emoji-upload";
    up.textContent = t("desktop.upload");
    up.title = t("chat.uploadEmojiHint");
    up.onclick = async (ev) => {
        ev.stopPropagation();
        const generation = V().state.serverGeneration;
        const tabID = V().state.activeTabID;
        const img = await pickIcon(128, 0.9);
        if (!img?.dataBase64 || generation !== V().state.serverGeneration) return;
        const name = (await promptDialog({
            title: t("chat.uploadEmojiTitle"),
            label: t("chat.emojiShortcode"),
            confirmLabel: t("chat.upload"),
            serverScoped: true,
        }) || "").trim();
        if (!name || generation !== V().state.serverGeneration) return;
        try {
            const err = await app().EmojiUploadForTab(tabID, name, img.dataBase64);
            if (generation !== V().state.serverGeneration) return;
            if (err) {
                V().toast(t("chat.emojiFailed", { error: String(err) }), "warn");
                return;
            }
            customDirty = true; // refetch so the new one appears in the picker
            V().toast(t("chat.emojiRequested", { name }));
        } catch (err) {
            if (generation === V().state.serverGeneration) V().toast(t("chat.emojiFailed", { error: String(err) }), "warn");
        }
    };
    head.appendChild(up);
    const grid = document.createElement("div");
    grid.className = "emoji-grid";
    emojiPanel.appendChild(grid);
    ensureCustomEmoji().then(async () => {
        if (!currentPanel()) return;
        if (customEmoji.length === 0) {
            const hint = document.createElement("div");
            hint.className = "set-hint";
            hint.textContent = t("chat.noEmoji");
            grid.appendChild(hint);
            return;
        }
        for (const e of customEmoji) {
            if (!currentPanel()) return;
            const url = await emojiURL(e.name);
            if (!currentPanel()) return;
            if (!url) continue;
            const b = document.createElement("button");
            b.title = ":" + e.name + ":";
            const img = document.createElement("img");
            img.src = url;
            img.alt = ":" + e.name + ":";
            b.appendChild(img);
            b.onclick = () => insert(":" + e.name + ":");
            grid.appendChild(b);
        }
    });

    const r = (target?.anchor || $("chat-emoji")).getBoundingClientRect();
    emojiPanel.style.right = Math.max(8, window.innerWidth - r.right) + "px";
    emojiPanel.style.bottom = (window.innerHeight - r.top + 8) + "px";
    document.body.appendChild(emojiPanel);
}

// ---------------------------------------------------------------------------
// Link preview cards (94)
// ---------------------------------------------------------------------------
// Metadata and raster thumbnails are fetched by the connected server only.

const linkCards = new Map(); // url -> {title: string | null}
let linkCardEl = null;
let linkCardTimer = null;
let linkCardGeneration = 0;
let linkCardRequest = null;
let linkCardAnchor = null;
let linkCardObserver = null;

function closeLinkCard() {
    linkCardGeneration++;
    linkCardRequest?.abort();
    linkCardRequest = null;
    linkCardAnchor = null;
    linkCardObserver?.disconnect();
    linkCardObserver = null;
    if (linkCardTimer) {
        clearTimeout(linkCardTimer);
        linkCardTimer = null;
    }
    if (linkCardEl) {
        linkCardEl.remove();
        linkCardEl = null;
    }
}

async function linkPreview(url, controller) {
    if (linkCards.has(url)) return linkCards.get(url);
    let card = { title: null, desc: null, image: null };
    const owner = captureScope(readExportScope);
    try {
        const data = await window.go.main.App.LinkPreviewForTab(owner.tabID, url);
        if (data) {
            // (181) Only server-transcoded bytes become an <img src>; a
            // third-party URL is shown as a visible blocked marker instead.
            const image = resolvePreviewImage(data);
            card = { title: data.title, desc: data.description, image: image.src, imageBlocked: image.blocked, imageHost: image.host };
            if (image.blocked) {
                card.blockedLabel = t("midnight.previewBlocked");
                card.blockedTitle = t("midnight.previewBlockedHelp", { host: image.host || t("midnight.previewBlockedUnknownHost") });
            }
        }
    } catch { /* unavailable: domain-only card, never a direct HTTP fallback */ }
    if (!controller.signal.aborted && exportScopeIsCurrent(owner)) linkCards.set(url, card);
    return card;
}

function bindLinkCardHover(root) {
    root.addEventListener("mouseover", (event) => {
        const link = event.target.closest(".md-link");
        if (link && root.contains(link) && link !== linkCardAnchor) scheduleLinkCard(link);
    });
    root.addEventListener("mouseout", (event) => {
        const link = event.target.closest(".md-link");
        if (link && !link.contains(event.relatedTarget)) closeLinkCard();
    });
}

function initLinkCardDismissal() {
    window.addEventListener("scroll", closeLinkCard, true);
    window.addEventListener("resize", closeLinkCard);
    window.addEventListener("blur", closeLinkCard);
    document.addEventListener("mouseleave", closeLinkCard);
    document.addEventListener("pointerdown", closeLinkCard, true);
    document.addEventListener("mousemove", event => {
        if (linkCardAnchor && !linkCardAnchor.contains(event.target)) closeLinkCard();
    });
    document.addEventListener("visibilitychange", () => { if (document.hidden) closeLinkCard(); });
    document.addEventListener("keydown", event => { if (event.key === "Escape") closeLinkCard(); });
}

function scheduleLinkCard(linkEl) {
    closeLinkCard();
    const settings = V().state.settings || {};
    if (settings.link_previews === false || previewConversationFor(linkEl) !== "channel") return;
    const url = linkEl.getAttribute("data-url");
    if (!url || youtubeVideo(url)) return;
    const video = videoPortal(url);
    if (video && [...(linkEl.closest(".msg-text")?.querySelectorAll(".video-portal-preview") || [])].some(card => card.dataset.portalKey === video.key)) return;
    const post = xPost(url);
    if (post && linkEl.closest(".msg-text")?.querySelector(`.x-post-preview[data-post-id="${post.id}"]`)) return;
    const generation = linkCardGeneration;
    linkCardAnchor = linkEl;
    // Chat rerenders can remove/hide a hovered link without a mouseout event.
    linkCardObserver = new MutationObserver(() => {
        if (!linkEl.isConnected || !linkEl.getClientRects().length) closeLinkCard();
    });
    linkCardObserver.observe(document, { childList: true, subtree: true });
    // Watch visibility on this link's ancestors without observing unrelated
    // per-frame style changes (for example microphone meters) across the app.
    for (let node = linkEl; node; node = node.parentElement) {
        linkCardObserver.observe(node, { attributes: true, attributeFilter: ["hidden", "class", "style"] });
    }
    linkCardTimer = setTimeout(async () => {
        linkCardTimer = null;
        if (previewConversationFor(linkEl) !== "channel") return;
        const controller = new AbortController();
        linkCardRequest = controller;
        let domain = url;
        try {
            domain = new URL(url).host;
        } catch { /* show the raw URL */ }
        const card = await linkPreview(url, controller);
        if (generation !== linkCardGeneration || !linkEl.isConnected ||
            !linkEl.matches(":hover") || !linkEl.getClientRects().length) return;
        linkCardRequest = null;
        linkCardEl = document.createElement("div");
        linkCardEl.className = "link-card";
        if (card.image) {
            const im = document.createElement("img");
            im.className = "link-card-img";
            im.src = card.image;
            im.onerror = () => im.remove();
            linkCardEl.appendChild(im);
        } else if (card.imageBlocked) {
            // Labels were translated in linkPreview(); `t` is shadowed below.
            const marker = blockedPreviewPlaceholder(document, {
                label: card.blockedLabel, title: card.blockedTitle, className: "link-card-blocked", host: card.imageHost,
            });
            if (marker) linkCardEl.appendChild(marker);
        }
        const t = document.createElement("div");
        t.className = "link-card-title";
        t.textContent = card.title || domain;
        linkCardEl.appendChild(t);
        if (card.desc) {
            const ds = document.createElement("div");
            ds.className = "link-card-desc";
            ds.textContent = card.desc;
            linkCardEl.appendChild(ds);
        }
        const d = document.createElement("div");
        d.className = "link-card-domain";
        d.textContent = card.title ? domain : url;
        linkCardEl.appendChild(d);
        const r = linkEl.getBoundingClientRect();
        linkCardEl.style.left = Math.min(r.left, window.innerWidth - 300) + "px";
        linkCardEl.style.top = Math.max(8, r.top - 64) + "px";
        document.body.appendChild(linkCardEl);
    }, 350);
}

// ---------------------------------------------------------------------------
// Pins panel (109)
// ---------------------------------------------------------------------------

function closePinsPanel() {
    if (pinsPanel) {
        pinsPanel.remove();
        pinsPanel = null;
    }
}

async function togglePinsPanel() {
    if (pinsPanel) {
        closePinsPanel();
        return;
    }
    if (view.kind === "dm") return;
    pinsPanel = document.createElement("div");
    pinsPanel.className = "chat-pop pins-panel";
    $("chat-wrap").appendChild(pinsPanel);
    await loadPinsPanel();
}

async function loadPinsPanel() {
    if (!pinsPanel) return;
    const panel = pinsPanel;
    const request = ++pinsRequest;
    const scope = captureScope(readExportScope);
    const current = () => pinsPanel === panel && pinsRequest === request && exportScopeIsCurrent(scope);
    if (view.kind === "channel" && !V().state.myChannelID) {
        V().toast(t("chat.joinPins"), "info", "alert");
        closePinsPanel();
        return;
    }
    const chID = view.kind === "global" ? 0 : (activeChannelID() || 0);
    pinsPanel.innerHTML = "";
    const head = document.createElement("div");
    head.className = "chat-pop-head";
    head.textContent = t("chat.pinnedTitle");
    const x = document.createElement("button");
    x.textContent = "✕";
    x.onclick = () => { if (current()) closePinsPanel(); };
    head.appendChild(x);
    pinsPanel.appendChild(head);
    let pins = [];
    try {
        // Pins decrypt through the same Go path as history, and the response
        // carries the generations it references, so bodies arrive plaintext
        // and the panel needs no extra round trip (109).
        const resp = await app().ChatPinsForTab(scope.tabID, chID);
        if (!current()) return;
        pins = resp.pins || [];
        // An authoritative refresh supersedes any still-pending preload.
        pinnedIDs.set("ch:" + chID, new Set(pins.map(p => Number(p.message_id))));
    } catch (e) {
        if (!current()) return;
        const err = document.createElement("div");
        err.className = "set-hint warn";
        err.textContent = t("chat.pinsFailed", { error: String(e) });
        pinsPanel.appendChild(err);
        return;
    }
    if (pins.length === 0) {
        const hint = document.createElement("div");
        hint.className = "set-hint";
        hint.textContent = t("chat.noPins");
        pinsPanel.appendChild(hint);
        return;
    }
    for (const p of pins) {
        const row = document.createElement("div");
        row.className = "pin-row";
        const body = document.createElement("span");
        body.className = "pin-body";
        const msg = p.message || {};
        const pinBadge = encryptionBadge(normalize(msg, chID));
        const shown = pinBadge.bodyKey ? t(pinBadge.bodyKey) : (msg.body || "").slice(0, 120);
        body.textContent = `${msg.from_nickname || "?"}: ${msg.deleted ? t("runtime.deleted") : shown}`;
        const pinLock = encryptionBadgeElement(pinBadge);
        if (pinLock) body.appendChild(pinLock);
        row.appendChild(body);
        const jump = document.createElement("button");
        jump.textContent = "↩";
        jump.title = t("chat.messageJump");
        jump.onclick = () => {
            if (!current()) return;
            if (!flashMsg(p.message_id)) {
                V().toast(t("chat.messageUnavailable"), "info", "alert");
            }
        };
        row.appendChild(jump);
        const unpin = document.createElement("button");
        unpin.textContent = "✕";
        unpin.title = t("chat.unpin");
        unpin.onclick = async () => {
            if (!current() || unpin.disabled) return;
            unpin.disabled = true;
            await runChatMutation(scope, () => app().ChatPinMessageForTab(scope.tabID, chID, p.message_id, false), t("chat.unpin"), current);
            if (current()) await loadPinsPanel();
        };
        row.appendChild(unpin);
        pinsPanel.appendChild(row);
    }
}

// ---------------------------------------------------------------------------
// Search (110)
// ---------------------------------------------------------------------------

function openSearch() {
    $("chat-search-row").classList.remove("hidden");
    $("chat-search-btn").setAttribute("aria-expanded", "true");
    $("chat-search").focus();
}

function closeSearch() {
    cancelChatSearch();
    $("chat-search-row").classList.add("hidden");
    $("chat-search-btn").setAttribute("aria-expanded", "false");
    $("chat-search").value = "";
    searchQ = "";
    searchChannel = "current";
    applySearchFilter();
    $("chat-search-btn").focus();
}

function applySearchFilter() {
    const log = $("chat-log");
    const q = searchQ.toLowerCase();
    clearMarks(log);
    let matches = 0;
    const messages = log.querySelectorAll(".msg.rich");
    messages.forEach((el) => {
        const show = !q || el.textContent.toLowerCase().includes(q);
        el.classList.toggle("hidden", !show);
        if (show) matches++;
        if (show && q) {
            const body = el.querySelector(".msg-text");
            if (body) markIn(body, q);
        }
    });
    const count = $("chat-search-count");
    count.textContent = t("polish.matches", { count: matches, total: messages.length });
    count.classList.toggle("hidden", !q);
    groupMessages(log);
}

function groupMessages(log) {
    let previous = null;
    for (const row of log.children) {
        const stamp = Number(row.dataset.ts);
        const priorStamp = Number(previous?.dataset.ts);
        const eligible = row.matches(".msg.rich:not(.hidden):not(.deleted)");
        const sameAuthor = eligible && previous && row.dataset.authorGroup && row.dataset.authorGroup === previous.dataset.authorGroup;
        const together = sameAuthor && stamp >= priorStamp && stamp - priorStamp < 5 * 60 * 1000 &&
            new Date(stamp).toDateString() === new Date(priorStamp).toDateString();
        row.classList.toggle("grouped", !!together);
        previous = eligible ? row : null;
    }
}

function clearMarks(root) {
    root.querySelectorAll("mark").forEach((mk) => {
        const parent = mk.parentNode;
        parent.replaceChild(document.createTextNode(mk.textContent), mk);
        parent.normalize();
    });
}

function markIn(el, q) {
    const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
    const nodes = [];
    while (walker.nextNode()) nodes.push(walker.currentNode);
    for (const node of nodes) {
        const text = node.nodeValue;
        const folded = text.toLowerCase();
        let start = 0;
        let idx = folded.indexOf(q);
        if (idx < 0) continue;
        const fragment = document.createDocumentFragment();
        while (idx >= 0) {
            fragment.append(document.createTextNode(text.slice(start, idx)));
            const mark = document.createElement("mark");
            mark.textContent = text.slice(idx, idx + q.length);
            fragment.append(mark);
            start = idx + q.length;
            idx = folded.indexOf(q, start);
        }
        fragment.append(document.createTextNode(text.slice(start)));
        node.replaceWith(fragment);
    }
}

// SEARCH_MAX mirrors the server's chat_search_max_messages default; the Go
// layer applies its own cap, this is only what we ask for.
const SEARCH_MAX = 2000;


// searchAll runs the full-history search. It is CLIENT-side: App.ChatSearch
// pages the scope in Go and matches over bodies it decrypted itself, because
// the server stores only ciphertext and cannot match on content (110).
async function searchAll() {
    const scope = captureScope(readExportScope);
    const dm = scope.kind === "dm" ? getDMOwner() : null;
    const q = $("chat-search").value.trim();
    if (!q) return;
    const btn = $("chat-search-server");
    if (btn && btn.disabled) return;
    if (view.kind === "channel" && !V().state.myChannelID) {
        V().toast(t("polish.joinSearch"), "info", "alert");
        return;
    }
    cancelChatSearch();
    const operation = { id: crypto.randomUUID(), scope, unsubscribe: null, scanned: 0 };
    activeSearch = operation;
    const current = () => activeSearch === operation && exportScopeIsCurrent(scope);
    btn.disabled = true;
    btn.textContent = t("polish.searching", { count: "" });
    // The paging loop lives in Go now: the keys never cross into the webview
    // and a page can use the store's cap of 200 instead of the UI's 50 (110).
    operation.unsubscribe = window.runtime.EventsOn("chatsearch:progress", data => {
        const count = scanProgressCount(data, operation.id);
        if (current() && count !== null) btn.textContent = t("polish.searching", { count: operation.scanned + count });
    });
    let res = null;
    try {
        if (scope.kind === "dm") res = await callDMHistory(dm, "DMSearchForContext", scope.uid, q, SEARCH_MAX);
        else {
            const visible = V().state.channels.map(channel => Number(channel.ChannelID)).filter(id => Number.isSafeInteger(id) && id > 0);
            const targets = searchChannel === "all" ? [...new Set([scope.channelID, 0, ...visible])]
                : searchChannel === "current" ? [scope.channelID] : [Number(searchChannel)];
            res = { messages: [], scanned: 0, undecryptable: 0, failed: 0, complete: true };
            for (const [index, channelID] of targets.entries()) {
                if (!current()) return;
                if (res.scanned >= SEARCH_MAX) { res.complete = false; break; }
                if (channelID !== 0 && channelID !== scope.channelID && !visible.includes(channelID)) throw new Error("channel unavailable");
                try {
                    operation.scanned = res.scanned;
                    const page = await app().ChatSearchForTab(scope.tabID, operation.id, channelID, q, SEARCH_MAX - res.scanned);
                    if (!current()) return;
                    res.messages.push(...(page.messages || []).map(message => ({ ...message, channel_id: channelID })));
                    res.scanned += page.scanned || 0; res.undecryptable += page.undecryptable || 0;
                    if (page.complete === false) res.complete = false;
                } catch (error) {
                    if (targets.length === 1) throw error;
                    res.failed++; res.complete = false;
                }
                if (index < targets.length - 1 && res.scanned >= SEARCH_MAX) res.complete = false;
            }
        }
    } catch (e) {
        if (current()) V().toast(t("polish.searchFailed", { error: String(e) }), "warn");
    }
    operation.unsubscribe?.();
    if (!current()) return;
    cancelChatSearch();
    if (res) showSearchResults(q, res, scope);
}

export function searchEncryptionBadge(message, direct = false) {
    const verified = !!message.enc_verified;
    return encryptionBadge({ text: message.body, direct, enc: verified, encVerified: verified, encMode: encModeFrom(verified, message.enc_mode) });
}

function showSearchResults(q, response, scope) {
    const results = response.messages || [], scanned = response.scanned || 0, undecryptable = response.undecryptable || 0;
    const channels = [{ id: "current", name: t("midnight.searchCurrent") }];
    if (scope.kind !== "dm") channels.push({ id: "all", name: t("midnight.searchAllChannels") }, { id: 0, name: t("chat.globalTitle") },
        ...V().state.channels.map(channel => ({ id: channel.ChannelID, name: channel.Name })));
    let summary = (undecryptable ? t("polish.searchPartial", { count: results.length, missing: undecryptable }) : t("polish.searchMatches", { count: results.length })) + " · " + t("polish.searched", { count: scanned });
    if (scanned >= SEARCH_MAX) summary += " · " + t("polish.searchCap", { count: SEARCH_MAX });
    else if (response.complete === false) summary += " · " + t("midnight.searchIncomplete");
    if (response.failed) summary += " · " + t("midnight.searchPartial", { count: response.failed });
    const current = () => exportScopeIsCurrent(scope);
    const overlay = createSearchWorkspace({ query: q, results, summary, channels, selectedChannel: searchChannel, current,
        highlight: markIn,
        unified: {
            members: V().state.clients, channels: V().state.channels, me: sessionUserID(V().state), blocked: V().state.settings?.blocked_users || [], avatars: V().state.avatars, avatarColor,
            // Files: the current channel's storage (there is no cross-channel file search on the server).
            loadFiles: scope.kind === "dm" ? null : async () => { const page = await app().FileListForTab(scope.tabID, scope.channelID, ""); return current() ? (page?.entries || []).map(entry => ({ ...entry, channel_id: scope.channelID })) : []; },
            onOpenMember: hit => { if (current()) openPM(hit.uid, hit.name); },
            onOpenChannel: hit => { if (current()) { window.__noxaFiles?.activateWorkspaceTab?.("chat"); void openChannelTab(hit.id); } },
            onOpenFile: () => { if (current()) window.__noxaFiles?.activateWorkspaceTab?.("files", { focus: true }); },
        },
        renderBadge: message => encryptionBadgeElement(searchEncryptionBadge(message, scope.kind === "dm")),
        onAdvanced: scope.kind !== "dm" ? openHistorySearch : null,
        renderBody: (container, message) => renderBody(container, { ...normalize(message, Number(message.channel_id ?? scope.channelID)), isDM: scope.kind === "dm" }),
        onSearch: (query, channel) => { $("chat-search").value = query; searchChannel = channel; searchQ = query; applySearchFilter(); void searchAll(); },
        onOpen: (message, panel) => {
            closeDialog(panel);
            void jumpMessageReference(scope.kind === "dm" ? { kind: "dm", peer_id: scope.uid, client_message_id: message.client_msg_id, local_seq: message.seq || message.id }
                : { kind: "channel", channel_id: message.channel_id ?? scope.channelID, message_id: message.id }, { isCurrent: current, continueConversation: true });
        },
        loadContext: async message => {
            if (scope.kind === "dm") {
                const owner = getDMOwner();
                const rows = await callDMHistory(owner, "DMHistoryLoadForContext", scope.uid);
                if (!current() || !dmOwnerIsCurrent(owner)) return [];
                const index = rows.findIndex(row => Number(row.seq) === Number(message.id));
                return index < 0 ? [] : rows.slice(Math.max(0, index - 2), index + 3).map(row => ({ ...row, id: row.seq }));
            }
            const page = await app().ChatHistoryForTab(scope.tabID, message.channel_id ?? scope.channelID, Number(message.id) + 1, 6);
            if (!current()) return [];
            return (page.messages || []).filter(entry => entry.enc_verified && !entry.deleted).reverse();
        },
    });
    overlay.addEventListener("click", event => {
        const link = event.target.closest(".md-link[data-url]");
        if (link && current()) { event.preventDefault(); window.runtime.BrowserOpenURL(link.dataset.url); }
    });
    searchResultsPanel = overlay;
    mountServerDialog(overlay, { onClose: () => { if (searchResultsPanel === overlay) searchResultsPanel = null; } });
}

// ---------------------------------------------------------------------------
// Header: topic (111), description (112/113), export (125)
// ---------------------------------------------------------------------------

export function refreshHeader() {
    renderTabs();
    updateHeader();
    void sendPendingReads();
}

// fmtSlowMode renders a slow-mode interval the way a person reads it.
function fmtSlowMode(sec) {
    if (sec % 3600 === 0) return sec / 3600 + "h";
    if (sec % 60 === 0) return sec / 60 + "m";
    return sec + "s";
}

function updateHeader() {
    updateComposerCount();
    const st = V().state;
    const filesOpen = $("files-pane")?.hidden === false;
    let title = st.myChannelID === 0 && st.channels.some(ch => ch.SystemKind === "home") ? "Lobby" : "Chat", topic = "";
    let showChanBtns = false;
    let slow = 0;
    if (filesOpen) {
        const channel = st.channels.find((c) => c.ChannelID === st.myChannelID);
        title = channel?.Name || "Files";
        topic = channel?.Topic || "";
    } else if (view.kind === "global") {
        title = t("chat.globalTitle");
    } else if (view.kind === "dm") {
        title = "DM — " + peerName(view.uid);
        if (selectedDirectRecipient?.uid === view.uid && $("chat-target").value === selectedDirectRecipient.label) selectDirectRecipient(view.uid);
    } else {
        const ch = st.channels.find((c) => c.ChannelID === activeChannelID());
        if (ch) {
            title = ch.Name;
            topic = ch.Topic || "";
            slow = ch.SlowModeSeconds || 0;
            showChanBtns = true;
        }
    }
    $("chat-head-title").textContent = title;
    const recordingChannel = filesOpen ? st.myChannelID : ["channel", "chan"].includes(view.kind) ? activeChannelID() : 0;
    if (st.channels.some(channel => channel.ChannelID === recordingChannel && channel.Recording)) {
        const badge = document.createElement("span");
        badge.className = "recording-indicator";
        badge.textContent = t("runtime.recordingActive");
        $("chat-head-title").append(badge);
    }
    $("chat-search-btn").classList.toggle("hidden", filesOpen);
    $("chat-head-actions").querySelector(".channel-actions").hidden = filesOpen;
    const directView = view.kind === "dm" && $("center")?.classList.contains("dm-view-active");
    $("chat-text").placeholder = view.kind === "channel" || view.kind === "chan" ? t("workspace.compose", { name: title }) : directView ? t("dm.composer", { name: peerName(view.uid) }) : view.kind === "dm" ? t("workspace.compose", { name: peerName(view.uid) }) : t("workspace.composeAll");
    const topicEl = $("chat-topic");
    topicEl.textContent = topic;
    topicEl.title = topic; // (111) tooltip carries the full topic
    topicEl.classList.toggle("hidden", !topic);
    // (114) slow mode is enforced by the server and rejects the send, so a user
    // who cannot see the limit only ever learns about it from a failure.
    const slowEl = $("chat-slowmode");
    slowEl.textContent = slow > 0 ? "🐢 " + fmtSlowMode(slow) : "";
    slowEl.title = slow > 0
        ? t("chat.slowModeHelp", { interval: fmtSlowMode(slow) })
        : "";
    slowEl.classList.toggle("hidden", slow <= 0);
    $("chat-pins-btn").classList.toggle("hidden", view.kind === "dm");
    $("chat-info-btn").classList.toggle("hidden", !showChanBtns);
    renderWorkspace();
}

function openDescription() {
    const st = V().state;
    const ch = st.channels.find((c) => c.ChannelID === activeChannelID());
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    const dlg = document.createElement("div");
    dlg.className = "dlg dlg-wide chan-desc";
    const h = document.createElement("h3");
    h.textContent = ch ? "# " + ch.Name : t("chat.channelLabel");
    dlg.appendChild(h);
    const body = document.createElement("div");
    body.className = "chan-desc-body";
    if (ch && ch.Description) {
        body.innerHTML = renderMarkdown(ch.Description); // escaped inside (112/113)
    } else {
        body.classList.add("set-hint");
        body.textContent = t("chat.noDescription");
    }
    dlg.appendChild(body);
    // (91-135) one source for the encryption story, so the client and the
    // README cannot drift apart on what the shield actually promises.
    const encH = document.createElement("h4");
    encH.textContent = t("chat.encryptionTitle");
    dlg.appendChild(encH);
    const enc = document.createElement("div");
    enc.className = "set-hint";
    enc.textContent = t("chat.encryptionHelp");
    dlg.appendChild(enc);
    const btns = document.createElement("div");
    btns.className = "dlg-buttons";
    const ok = document.createElement("button");
    ok.className = "dlg-ok";
    ok.textContent = t("chat.close");
    ok.onclick = () => overlay.remove();
    btns.appendChild(ok);
    dlg.appendChild(btns);
    overlay.appendChild(dlg);
    overlay.onclick = (e) => {
        if (e.target === overlay) overlay.remove();
    };
    mountServerDialog(overlay);
}

// exportProgressDialog reports how far a running export got. Paging a whole
// scope takes long enough that a silent window looks hung, and the header
// button is too small to carry a running count (125).
function exportProgressDialog() {
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    const dlg = document.createElement("div");
    dlg.className = "dlg";
    const h = document.createElement("h3");
    h.textContent = t("chat.exportProgressTitle");
    const p = document.createElement("div");
    p.className = "set-hint";
    p.textContent = t("chat.decrypting");
    dlg.appendChild(h);
    dlg.appendChild(p);
    overlay.appendChild(dlg);
    let cleanup = null;
    mountServerDialog(overlay, { onClose: () => cleanup?.() });
    return {
        update: (n) => { p.textContent = t("chat.decryptedCount", { count: n }); },
        close: () => closeDialog(overlay),
        setCleanup: (fn) => { cleanup = fn; },
    };
}

// exportChat writes the WHOLE stored history out. The transcript is built in
// Go — ChatExportHistory pages and decrypts the entire scope, DMExportHistory
// reads the sealed local log — because the view holds only the window it
// happened to scroll through, and an export of that window silently drops
// everything else (125/122). Everything it produces is decrypted, so a plain
// export is the one place this client can undo the storage guarantee: it takes
// an explicit confirm, and the encrypted container is offered first.
async function exportChat() {
    const attempt = ++exportAttempt;
    activeExport?.progress.close();
    const exportScope = captureScope(readExportScope);
    const dm = exportScope.kind === "dm" ? getDMOwner() : null;
    const attemptIsCurrent = () => attempt === exportAttempt && exportScopeIsCurrent(exportScope);
    // Without a joined channel the channel view has no scope of its own, and
    // channel id 0 is GLOBAL: exporting it here would hand over a different
    // conversation than the one on screen.
    if (exportScope.kind === "channel" && !exportScope.channelID) {
        V().toast(t("chat.joinExport"), "info", "alert");
        return;
    }
    const name = exportScope.kind === "dm" ? "dm-" + exportScope.uid.slice(0, 8)
        : exportScope.kind === "global" ? "global"
        : (V().state.channels.find((c) => c.ChannelID === exportScope.channelID)?.Name || "channel");

    const pass = await askExportPassphrase();
    if (pass === null || !attemptIsCurrent()) return;

    const prog = exportProgressDialog();
    const operation = { id: crypto.randomUUID(), progress: prog };
    activeExport = operation;
    const current = () => activeExport === operation && attemptIsCurrent();
    let unsub = window.runtime.EventsOn("chatexport:progress", data => {
        const count = scanProgressCount(data, operation.id);
        if (current() && count !== null) prog.update(count);
    });
    prog.setCleanup(() => {
        if (activeExport === operation) activeExport = null;
        if (!unsub) return;
        unsub();
        unsub = null;
    });
    let res = null;
    try {
        res = exportScope.kind === "dm"
            ? await callDMHistory(dm, "DMExportHistoryForContext", exportScope.uid)
            : await app().ChatExportHistoryForTab(exportScope.tabID, operation.id, exportScope.channelID, 0);
    } catch (e) {
        if (current()) V().toast(t("chat.exportFailed", { error: String(e) }), "warn");
    }
    const ready = current();
    prog.close();
    if (!ready || !res || !attemptIsCurrent()) return;

    const contents = res.text || "";
    if (pass !== "") {
        try {
            await app().ExportChatEncrypted(`noxa-${name}.noxachat`, contents, pass);
            if (!attemptIsCurrent()) return;
        } catch (e) {
            if (!attemptIsCurrent()) return;
            V().toast(t("chat.exportFailed", { error: String(e) }), "warn");
            return;
        }
    } else {
        const err = await app().ExportChat(`noxa-${name}.txt`, contents);
        if (!attemptIsCurrent()) return;
        if (err) {
            V().toast(t("chat.exportFailed", { error: String(err) }), "warn");
            return;
        }
    }
    // The same notice rides inside the file; repeating it here is what stops a
    // partial transcript from being taken for a whole one before it is opened.
    if (res.complete === false || res.undecryptable > 0) {
        V().toast(t("chat.exportCount", { count: res.messages }) +
            (res.undecryptable > 0 ? t("chat.exportUnreadable", { count: res.undecryptable }) : "") +
            (res.complete === false ? t("chat.exportLimit") : ""), "warn");
    }
}

// askExportPassphrase resolves to a passphrase (encrypted export), "" (plain
// export, explicitly confirmed) or null (cancelled).
function askExportPassphrase() {
    return new Promise((resolve) => {
        let result = null;
        let settled = false;
        let confirmingPlainExport = false;
        const overlay = document.createElement("div");
        overlay.className = "dlg-overlay";
        const dlg = document.createElement("div");
        dlg.className = "dlg";
        const h = document.createElement("h3");
        h.textContent = t("chat.exportTitle");
        dlg.appendChild(h);
        const warn = document.createElement("div");
        warn.className = "set-hint warn";
        warn.textContent = t("chat.exportWarning");

        dlg.appendChild(warn);
        const inp = document.createElement("input");
        inp.type = "password";
        inp.placeholder = t("chat.passphraseHint");
        dlg.appendChild(inp);
        const btns = document.createElement("div");
        btns.className = "dlg-buttons";
        const done = (value) => {
            result = value;
            closeDialog(overlay, value === null ? "cancel" : "close");
        };
        const cancel = document.createElement("button");
        cancel.textContent = t("chat.cancel");
        cancel.onclick = () => done(null);
        const ok = document.createElement("button");
        ok.className = "dlg-ok";
        ok.textContent = t("chat.export");
        ok.onclick = async () => {
            const p = inp.value;
            if (p === "") {
                if (confirmingPlainExport) return;
                confirmingPlainExport = true;
                const confirmed = await confirmDialog({
                    title: t("chat.exportPlainTitle"),
                    message: t("chat.exportPlainHelp"),
                    confirmLabel: t("chat.exportPlainConfirm"),
                    danger: true,
                    serverScoped: true,
                });
                confirmingPlainExport = false;
                if (!confirmed || !isCurrentServerDialog(overlay)) return;
            }
            done(p);
        };
        btns.appendChild(cancel);
        btns.appendChild(ok);
        dlg.appendChild(btns);
        overlay.appendChild(dlg);
        overlay.onclick = (e) => { if (e.target === overlay) done(null); };
        mountServerDialog(overlay, {
            initialFocus: inp,
            onCancel: () => { result = null; },
            onClose: () => {
                if (settled) return;
                settled = true;
                resolve(result);
            },
        });
    });
}

// ---------------------------------------------------------------------------
// Announcement banner (132) + MOTD (133)
// ---------------------------------------------------------------------------

export function onAnnouncement(d) {
    const box = $("banner-box");
    box.innerHTML = "";
    const text = d.text || "";
    if (!text) return;
    // (385) announcements go through the notification matrix.
    window.__noxaNotify?.notify("announcement", t("chat.announcement", { message: text.slice(0, 100) }), { className: "messages", kind: "warn" });
    const h = hashStr(text);
    const s = V().state.settings;
    if (s?.dismissed_announcement === h) return; // already dismissed
    const b = document.createElement("div");
    b.className = "announce-banner";
    const announcementText = document.createElement("span");
    announcementText.textContent = "📢 " + text;
    const x = document.createElement("button");
    x.textContent = "✕";
    x.title = t("chat.dismiss");
    x.onclick = () => {
        b.remove();
        if (s) {
            s.dismissed_announcement = h;
            persistSettings();
        }
    };
    b.appendChild(announcementText);
    b.appendChild(x);
    box.appendChild(b);
}

export async function onConnect() {
    initConversations();
    const st = V().state;
    const { activeTabID: tabID, serverGeneration: generation } = st;
    const current = () => tabID === st.activeTabID && generation === st.serverGeneration;
    composerLimit = null;
    updateComposerCount();
    const limitScope = readChatServerScope();
    void Promise.resolve().then(() => app().ServerInfoForTab(tabID)).then(info => {
        if (!current() || !chatServerIsCurrent(limitScope)) return;
        composerLimit = { scope: limitScope, bytes: Number.isSafeInteger(info?.chat_max_bytes) ? info.chat_max_bytes : 0 };
        updateComposerCount();
    }).catch(() => {});
    // The context resolves the connection's key, which may differ from the
    // identity selected for the next connection. Acquisition sets myUniqueID.
    await getDMOwner().ready;
    if (!current()) return;
    restorePMTabs(); // (122) conversations that survived the last restart
    try {
        const channelIDs = await app().SubscriptionsForTab(tabID);
        if (!current()) return;
        onSubscriptions({ channel_ids: channelIDs });
    } catch { /* the live authoritative event is still the primary path */ }
    if (!current()) return;
    // (133) surface the server MOTD once per connect. It is a server notice,
    // not a message: styling it as one would put it next to a shield or a lock
    // it has not earned.
    try {
        const motd = await app().MOTDForTab(tabID);
        if (!current()) return;
        if (motd) V().sysMsg(t("chat.serverNotice", { message: motd }));
    } catch { /* MOTD is best-effort */ }
}

// reportRead tells the server this desktop read a conversation, so the
// account's phones clear its notification. Best effort.
function reportRead(scope, id) {
    if (id === undefined || id === null || id === "") return;
    try { void app().ReportReadForTab?.(V().state.activeTabID || "", scope, String(id), "")?.catch?.(() => {}); } catch { /* older binding */ }
}

export function onMyChannelChanged() {
    const st = V().state;
    if (view.kind === "channel") invalidateChatViewWork();
    if (unread.delete(st.myChannelID)) V().renderTree();
    reportRead("channel", st.myChannelID);
    if (view.kind === "chan" && view.id === st.myChannelID) {
        setView({ kind: "channel" });
    } else if (view.kind === "channel") renderView();
    else updateHeader();
    renderTabs();
}

// Channel deletion can remove a whole subtree in one event. Purge every
// channel-owned cache immediately instead of waiting for a later subscription
// snapshot, and leave a deleted subscribed-channel view for the joined scope.
export function onChannelsDeleted(channelIDs) {
    const removed = new Set((channelIDs || []).map(Number).filter((id) => id > 0));
    if (removed.size === 0) return;
    subscriptions = subscriptions.filter((id) => !removed.has(id));
    for (const id of removed) {
        recentlyClosedChats.delete("ch:" + id);
        tabSubscriptionChanges.get(id)?.finish(false);
        chanTabs.delete(id);
        unread.delete(id);
        store.delete("ch:" + id);
        pinnedIDs.delete("ch:" + id);
        pendingPinChanges.delete("ch:" + id);
    }
    if (removed.has(pendingChannelTab)) pendingChannelTab = 0;
    if (view.kind === "chan" && removed.has(view.id)) {
        $("chat-scope").value = "channel";
        V().setDirectTargetVisible(false);
        setView({ kind: "channel" });
        return;
    }
    renderTabs();
    updateHeader();
}

// resetView clears all per-server chat state (281 tab switch): message
// stores, unread badges, PM tabs, and the rendered panes. The tab journal
// replay rebuilds the view from server frames afterwards.
export function resetView(options = {}) {
    resetDiscussions();
    composerLimit = null;
    updateComposerCount();
    resetConversations();
    resetDMOwner();
    V().state.myUniqueID = "";
    invalidateChatViewWork();
    closeEmojiPanel();
    closeReactStrip();
    view = { kind: "channel" };
    store.clear();
    unread.clear();
    pmTabs.clear();
    groupTabs.clear();
    chanTabs.clear();
    subscriptions = [];
    pendingChannelTab = 0;
    replyTo = null;
    pendingFiles = [];
    attachmentQueue?.reset();
    newCount = 0;
    jl.verb = null;
    jl.names = [];
    jl.el = null;
    customEmoji = [];
    emojiURLs.clear();
    customDirty = true;
    for (const b of offlineBatch.values()) {
        if (b.timer) clearTimeout(b.timer);
    }
    offlineBatch.clear();
    if (!options.preserveReconnectAnnouncements) cancelReconnectAnnouncementBatch();
    linkCards.clear();
    receipts.clear();
    myReactions.clear();
    pinnedIDs.clear();
    pendingPinChanges.clear();
    threadCache.clear();
    closeThreadPanel();
    closePinsPanel();
    typers.clear();
    resetTypingOut();
    if (typingTimer) {
        clearInterval(typingTimer);
        typingTimer = null;
    }
    renderTyping();
    $("chat-log").innerHTML = "";
    $("pm-tabs").innerHTML = "";
    $("pm-tabs").classList.add("hidden");
    $("reply-bar").classList.add("hidden");
    $("file-preview-row").classList.add("hidden");
    $("chat-head-title").textContent = t("desktop.chat");
    $("chat-topic").classList.add("hidden");
    renderTabs();
    // (122) DM logs are sealed to the IDENTITY, not to a server, so the same
    // conversations belong on the tab that was just switched to.
    restorePMTabs();
}

// ---------------------------------------------------------------------------
// Join/leave system lines with collapsing (130/131)
// ---------------------------------------------------------------------------

const jl = { verb: null, names: [], ts: 0, el: null };

export function sysJoinLeave(nick, verb) {
    const s = V().state.settings;
    if (s && s.sys_join_leave === false) return; // (130) category filter
    const now = Date.now();
    // (131) consecutive same-verb lines inside 60s collapse into one line.
    if (jl.verb === verb && jl.el && jl.el.isConnected && now - jl.ts < 60000) {
        jl.names.push(nick);
        jl.ts = now;
        jl.el.textContent = "— " + jl.names.join(", ") + " " + verb + " —";
        return;
    }
    jl.verb = verb;
    jl.names = [nick];
    jl.ts = now;
    const el = document.createElement("div");
    el.className = "msg sys";
    el.textContent = "— " + nick + " " + verb + " —";
    $("chat-log").appendChild(el);
    jl.el = el;
    if (atBottom) scrollToBottom();
}

// ---------------------------------------------------------------------------
// Display prefs (126-129)
// ---------------------------------------------------------------------------

export function applyChatPrefs() {
    const s = V().state.settings || {};
    const log = $("chat-log");
    log.classList.toggle("chat-compact", (s.chat_density || "comfortable") === "compact");
    log.classList.toggle("chat-bubbles", (s.chat_layout || "irc") === "bubbles");
    log.style.setProperty("--chat-font-size", (s.chat_font_size || 14) + "px");
    const previewsEnabled = s.link_previews !== false;
    if (linkPreviewPreference !== previewsEnabled) {
        linkPreviewPreference = previewsEnabled;
        if (!previewsEnabled) { closeLinkCard(); document.querySelectorAll(".youtube-preview, .x-post-preview, .video-portal-preview").forEach(card => card.remove()); }
        else {
            const scope = captureScope(readExportScope);
            for (const message of getStore(activeKey()).msgs) {
                const body = log.querySelector(`[data-scroll-key="${CSS.escape(message.renderKey || "")}"] .msg-text`);
                if (body && !body.querySelector(".youtube-preview")) renderYouTubePreviews(body, { enabled: true, conversation: previewConversation(message), scope, isCurrent: () => exportScopeIsCurrent(scope) && V().state.settings?.link_previews !== false });
                if (body && !body.querySelector(".x-post-preview")) renderXPostPreviews(body, { enabled: true, conversation: previewConversation(message), scope, isCurrent: () => exportScopeIsCurrent(scope) && V().state.settings?.link_previews !== false });
                if (body && !body.querySelector(".video-portal-preview")) renderVideoPortalPreviews(body, { enabled: true, conversation: previewConversation(message), scope, isCurrent: () => exportScopeIsCurrent(scope) && V().state.settings?.link_previews !== false });
            }
        }
    }
    // Timestamps re-render live when the mode changes (126).
    log.querySelectorAll(".msg[data-ts]").forEach((el) => {
        const t = el.querySelector(".msg-timestamp");
        if (t) t.textContent = fmtTime(Number(el.dataset.ts));
    });
}

// ---------------------------------------------------------------------------
// Quick switcher (135)
// ---------------------------------------------------------------------------

let qsOverlay = null;

function qsScore(q, s) {
    q = q.toLowerCase();
    s = s.toLowerCase();
    if (!q) return 0;
    const i = s.indexOf(q);
    if (i >= 0) return 100 - i; // substring beats subsequence
    let qi = 0;
    for (const ch of s) if (ch === q[qi]) qi++;
    return qi === q.length ? 50 - s.length * 0.05 : -1;
}

// Export the pure message and switcher boundaries for compatibility coverage;
// UI event handlers continue to use these same implementations above.
export { fmtSlowMode, mentionsMe, normalize, qsScore, resolveParent };

function closeQS() {
    if (qsOverlay) {
        closeDialog(qsOverlay);
    }
}

function openQS() {
    closeQS();
    const st = V().state;
    const { activeTabID: tabID, serverGeneration: generation } = st;
    const items = [];
    const toChat = () => window.__noxaFiles?.activateWorkspaceTab?.("chat");
    for (const ch of st.channels) {
        // Text channels have no voice: picking one opens its chat.
        if (ch.TextOnly) {
            items.push({ label: "# " + ch.Name, hint: t("shell.qs.kind.text"), concepts: ["channel"], action: () => {
                if (generation !== st.serverGeneration) return;
                toChat();
                void openChannelTab(ch.ChannelID);
            } });
            continue;
        }
        items.push({ label: "# " + ch.Name, hint: t("chat.channelHint"), concepts: ["channel", "voice"], action: async () => {
            if (generation !== st.serverGeneration) return;
            try {
                const err = await app().JoinChannelForTab(tabID, ch.SystemKind === "home" ? 0 : ch.ChannelID);
                if (err && generation === st.serverGeneration) V().toast(err, "warn");
            } catch (err) {
                if (generation === st.serverGeneration) V().toast(String(err), "warn");
            }
        } });
    }
    for (const c of st.clients) {
        if (c.unique_id && c.unique_id !== sessionUserID(st)) {
            items.push({ label: "@ " + (c.nickname || c.unique_id), hint: t("chat.userHint"), concepts: ["member", "dm"], action: () => openPM(c.unique_id, c.nickname) });
        }
    }
    for (const tab of pmTabs.values()) {
        items.push({ label: "✉ " + tab.nick, hint: t("chat.pmTabHint"), concepts: ["dm"], action: () => activatePM(tab.uid) });
    }
    for (const tab of chanTabs.values()) {
        items.push({ label: "# " + channelName(tab.id), hint: t("chat.channelTabHint"), concepts: ["channel"], action: () => activateChannel(tab.id) });
    }
    // Commands: the Messages view, a new direct message through the member
    // picker, and the unified search. "DM", "Nachricht", "conversation" and
    // the other synonyms reach them through their concepts.
    items.push({ label: "✉ " + t("shell.qs.openMessages"), hint: t("shell.qs.kind.command"), concepts: ["dm", "group"], action: () => {
        if (generation !== st.serverGeneration) return;
        toChat();
        if (typeof V().openDirectMessages === "function") V().openDirectMessages(); else window.__noxaShell?.openDirectMessages?.();
    } });
    items.push({ label: "✉ " + t("shell.qs.newDirect"), hint: t("shell.qs.kind.command"), concepts: ["dm", "member"], action: () => {
        if (generation === st.serverGeneration) openNewDirectMessagePicker();
    } });
    items.push({ label: "🔍 " + t("shell.qs.searchAll"), hint: t("shell.qs.kind.command"), concepts: ["search", "files", "member", "channel"], action: () => {
        if (generation !== st.serverGeneration) return;
        toChat();
        openSearch();
    } });
    // Settings pages match once the user types, so they never crowd the
    // default list of channels and people.
    // The settings dialog may publish its own page list (settingsPages());
    // otherwise the shell's static list is used.
    let settingsPages = null;
    try { settingsPages = V().settingsPages?.(); } catch { settingsPages = null; }
    const pages = Array.isArray(settingsPages) && settingsPages.length
        ? settingsPages.filter((page) => page?.id).map((page) => [page.id, String(page.label || page.id)])
        : QUICK_SWITCH_SETTINGS;
    for (const [page, key] of pages) {
        const label = /^[\w-]+(\.[\w-]+)+$/.test(key) ? t(key) : key;
        items.push({ label: "⚙ " + label, hint: t("shell.qs.kind.setting"), typed: true, concepts: ["settings"], action: () => V()?.openSettings?.(page) });
    }

    qsOverlay = document.createElement("div");
    qsOverlay.className = "dlg-overlay qs-overlay";
    qsOverlay.setAttribute("aria-label", t("shell.qs.title"));
    const box = document.createElement("div");
    box.className = "qs-box";
    const input = document.createElement("input");
    input.placeholder = t("chat.switcherHint");
    input.autocomplete = "off";
    input.spellcheck = false;
    input.setAttribute("role", "combobox");
    input.setAttribute("aria-label", t("shell.qs.title"));
    input.setAttribute("aria-autocomplete", "list");
    input.setAttribute("aria-expanded", "true");
    input.setAttribute("aria-controls", "qs-list");
    input.setAttribute("aria-describedby", "qs-help");
    const list = document.createElement("div");
    list.className = "qs-list";
    list.id = "qs-list";
    list.setAttribute("role", "listbox");
    list.setAttribute("aria-label", t("shell.qs.results"));
    const help = document.createElement("p");
    help.className = "qs-help";
    help.id = "qs-help";
    help.textContent = t("shell.qs.hint");
    const status = document.createElement("p");
    status.className = "sr-only";
    status.setAttribute("role", "status");
    box.append(input, list, help, status);
    qsOverlay.appendChild(box);
    qsOverlay.onclick = (e) => {
        if (e.target === qsOverlay) closeQS();
    };
    const mountedOverlay = qsOverlay;
    const dmScope = captureScope(readDMHistoryScope);
    const currentQuickSwitcher = () => qsOverlay === mountedOverlay && mountedOverlay.isConnected && dmHistoryScopeIsCurrent(dmScope);
    mountServerDialog(qsOverlay, {
        initialFocus: input,
        onClose: () => {
            if (qsOverlay === mountedOverlay) qsOverlay = null;
        },
    });

    let sel = 0;
    let matches = [];
    const renderList = () => {
        const q = input.value.trim();
        matches = items
            .filter((it) => q || !it.typed)
            .map((it) => ({ it, score: Math.max(qsScore(q, it.label.replace(/^[#@✉⚙👥🔍] /u, "")), conceptScore(q, it, qsScore)) }))
            .filter((x) => x.score >= 0)
            .sort((a, b) => b.score - a.score)
            .slice(0, 12)
            .map((x) => x.it);
        sel = Math.min(sel, Math.max(0, matches.length - 1));
        list.innerHTML = "";
        matches.forEach((it, i) => {
            const row = document.createElement("div");
            row.className = "qs-row" + (i === sel ? " sel" : "");
            row.id = `qs-option-${i}`;
            row.setAttribute("role", "option");
            row.setAttribute("aria-selected", String(i === sel));
            const l = document.createElement("span");
            l.textContent = it.label;
            const h = document.createElement("span");
            h.className = "qs-hint";
            h.textContent = it.hint;
            row.appendChild(l);
            row.appendChild(h);
            row.onmousedown = (e) => e.preventDefault(); // keep focus in the input
            row.onclick = () => {
                if (!currentQuickSwitcher()) return;
                closeQS();
                it.action();
            };
            list.appendChild(row);
        });
        if (matches.length) {
            input.setAttribute("aria-activedescendant", `qs-option-${sel}`);
            list.querySelector(".qs-row.sel")?.scrollIntoView({ block: "nearest" });
        } else input.removeAttribute("aria-activedescendant");
        status.textContent = matches.length ? t("shell.qs.count", { count: matches.length }) : t("shell.qs.empty", { query: q });
    };
    input.oninput = () => {
        sel = 0;
        renderList();
    };
    input.onkeydown = (e) => {
        e.stopPropagation();
        if (!currentQuickSwitcher()) return;
        if (e.key === "Escape") closeQS();
        if (e.key === "ArrowDown") {
            e.preventDefault();
            sel = Math.min(sel + 1, matches.length - 1);
            renderList();
        }
        if (e.key === "ArrowUp") {
            e.preventDefault();
            sel = Math.max(sel - 1, 0);
            renderList();
        }
        if (e.key === "Home" || e.key === "End") {
            if (!e.ctrlKey) return;
            e.preventDefault();
            sel = e.key === "Home" ? 0 : Math.max(0, matches.length - 1);
            renderList();
        }
        if (e.key === "Enter" && matches[sel]) {
            closeQS();
            matches[sel].action();
        }
    };
    renderList();
    input.focus();
    // Private groups come from the server; add them when the list arrives.
    const listGroups = app().ConversationForTab;
    if (typeof listGroups === "function") void Promise.resolve().then(() => listGroups(tabID, { action: "list" })).then((result) => {
        if (!currentQuickSwitcher() || generation !== st.serverGeneration) return;
        const me = sessionUserID(st);
        for (const group of result?.conversations || []) {
            if (!group?.id || group.members?.find((member) => member.unique_id === me)?.pending) continue;
            items.push({ label: "👥 " + (group.name || group.id), hint: t("shell.qs.kind.group"), concepts: ["group", "dm"], action: () => {
                toChat();
                void openConversationsAt(group.id);
            } });
        }
        renderList();
    }).catch(() => { /* Groups are optional; channels and people still work. */ });
}

// openNewDirectMessagePicker starts a direct message through the member
// picker: members online, contacts, open conversations and private-group
// members, told apart by server, short account ID, friend and channel; an
// account ID can still be pasted under "Advanced". An existing conversation
// with the chosen member is reopened rather than duplicated (openPM reuses
// the tab).
export function openNewDirectMessagePicker({ onPick = null } = {}) {
    const st = V().state;
    const generation = st.serverGeneration;
    const me = sessionUserID(st);
    const server = document.getElementById("server-name")?.textContent?.trim() || st.lastConnect?.addr || "";
    const groups = (() => { try { return listedPrivateGroups()?.groups || []; } catch { return []; } })();
    return openMemberPicker({
        title: t("picker.newMessageTitle"), hint: t("picker.newMessageHint"),
        current: () => generation === V().state.serverGeneration,
        candidates: () => buildMemberCandidates({
            clients: st.clients, contacts: st.settings?.contacts || [], conversations: directPeers().map(peer => ({ uid: peer.uid, name: peer.nick })),
            groups, blocked: st.settings?.blocked_users || [], me, channels: st.channels, server,
        }),
        onPick: entry => {
            if (generation !== V().state.serverGeneration) return;
            if (onPick) { onPick(entry); return; }
            window.__noxaFiles?.activateWorkspaceTab?.("chat");
            openPM(entry.uid, entry.name || "");
        },
    });
}

// The top bar's "Search or jump to…" field opens the same switcher.
export function openQuickSwitcher() {
    if (!document.getElementById("app")?.classList.contains("hidden")) openQS();
}

// ---------------------------------------------------------------------------
// @nickname tab-completion in the chat input (105/106)
// ---------------------------------------------------------------------------

let tabCycle = null; // {start, end, base, matches, idx}

function handleTabComplete(e) {
    const input = $("chat-text");
    const pos = input.selectionStart ?? input.value.length;
    if (tabCycle && pos === tabCycle.end) {
        e.preventDefault();
        tabCycle.idx = (tabCycle.idx + 1) % tabCycle.matches.length;
        const pick = tabCycle.matches[tabCycle.idx];
        draftRevision++;
        input.value = input.value.slice(0, tabCycle.start) + pick + " " + input.value.slice(tabCycle.end);
        tabCycle.end = tabCycle.start + pick.length + 1;
        input.selectionStart = input.selectionEnd = tabCycle.end;
        return;
    }
    const before = input.value.slice(0, pos);
    const m = before.match(/@([\w-]*)$/);
    if (!m) {
        tabCycle = null;
        return;
    }
    const base = m[1].toLowerCase();
    const names = V().state.clients
        .filter((c) => c.client_id !== V().state.myClientID)
        .map((c) => c.nickname || c.unique_id)
        .filter((n) => n && n.toLowerCase().startsWith(base)).map(n => "@" + n);
    if (view.kind !== "dm") names.push(...roleMentionChoices(V().state.clients, base).map(role => role.token));
    if (canMentionAll()) {
        for (const f of MENTION_ALL_FORMS) if (f.startsWith(base)) names.push("@" + f);
    }
    if (names.length === 0) {
        tabCycle = null;
        return;
    }
    e.preventDefault();
    const start = pos - m[0].length;
    const pick = names[0];
    const end = start + pick.length + 1;
    tabCycle = { start, end, base, matches: names, idx: 0 };
    draftRevision++;
    input.value = input.value.slice(0, start) + pick + " " + input.value.slice(pos);
    input.selectionStart = input.selectionEnd = end;
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

export function initChat() {
    chatDelivery = createChatDelivery({ app, state: () => V().state, readScope: readSendScope, restore: entry => {
        const input = $("chat-text");
        draftRevision++;
        input.value = entry.text + (input.value ? (input.tagName === "TEXTAREA" ? "\n" : " ") + input.value : "");
        if (entry.reply_to_id) setReply({ id: entry.reply_to_id, from: "#"+entry.reply_to_id, text: "" });
        updateComposerCount(); input.focus();
    } });
    chatTabs?.dispose();
    unreadEffects?.dispose();
    const uploadHost = document.createElement("div"); uploadHost.className = "chat-upload-queue"; uploadHost.hidden = true;
    $("file-preview-row").after(uploadHost);
    attachmentQueue = createChatUploadQueue({ app, currentTab: () => V().state.activeTabID, container: uploadHost });
    window.runtime.EventsOn("ft_progress", event => attachmentQueue.progress(event));
    unreadEffects = createUnreadTabEffects($("pm-tabs"));
    chatTabs = createChatTabs($("pm-tabs"), { onLayoutChange: saveTabLayout, onRefresh: () => unreadEffects?.refresh() });
    initMessageTools({ unread: unreadMessageReferences, jump: jumpMessageReference });
    initDiscussions(() => activeChannelID());
    $("chat-text").addEventListener("input", updateComposerCount);
    $("chat-text").addEventListener("keyup", updateComposerCount);
    window.addEventListener("noxa-language-changed", updateComposerCount);
    window.runtime.EventsOn("subscriptions", onSubscriptions);
    window.runtime.EventsOn("dm_history_identity_changed", onDMIdentityChanged);
    window.runtime.EventsOn("tab_identity", data => {
        const identity = parseRuntimeObject(data);
        if (identity?.tab_id === V().state.activeTabID && typeof identity.identity_uid === "string") {
            V().state.myUniqueID = identity.identity_uid;
        }
    });
    const log = $("chat-log");

    applyChatPrefs();
    const stopGIFPlayback = manageChatGIFPlayback(document.body, ".msg img, .lightbox img, .msg video.msg-video, .lightbox video, .group-message-body img, .group-message-body video.msg-video");

    // Attachments, wrapped text and viewport changes can grow the conversation
    // after appendLive has scrolled. Keep following the newest message until
    // the user scrolls into history; observe rows as well as the viewport.
    let lastViewportHeight = log.clientHeight, lastContentHeight = log.scrollHeight;
    const rememberChatLayout = () => { lastViewportHeight = log.clientHeight; lastContentHeight = log.scrollHeight; };
    const followLatest = () => {
        if (atBottom) scrollToBottom();
        rememberChatLayout();
    };
    const chatResize = new ResizeObserver(followLatest);
    chatResize.observe(log);
    const chatRows = new MutationObserver(records => {
        for (const record of records) {
            for (const node of record.removedNodes) if (node.nodeType === 1) chatResize.unobserve(node);
            for (const node of record.addedNodes) if (node.nodeType === 1) chatResize.observe(node);
        }
    });
    chatRows.observe(log, { childList: true });
    log.addEventListener("load", followLatest, true);
    log.addEventListener("loadedmetadata", followLatest, true);
    window.addEventListener("pagehide", () => { chatResize.disconnect(); chatRows.disconnect(); stopGIFPlayback(); chatTabs?.dispose(); unreadEffects?.dispose(); }, { once: true });

    // Scope selector drives the active view (channel/global/direct).
    $("chat-scope").addEventListener("change", () => {
        const scope = $("chat-scope").value;
        if (scope === "global") setView({ kind: "global" });
        else if (scope === "channel") setView({ kind: "channel" });
        else {
            // Entering direct mode from a channel starts a new selection.
            // An already-open private chat keeps its own recipient.
            selectDirectRecipient(view.kind === "dm" ? view.uid : "");
            invalidateChatViewWork();
        }
    });
    $("chat-target").addEventListener("input", () => {
        selectedDirectRecipient = null;
        invalidateChatViewWork();
    });
    $("chat-target").addEventListener("change", () => {
        const uid = directRecipientID();
        if ($("chat-scope").value === "direct" && uid) openPM(uid);
        else { selectedDirectRecipient = null; invalidateChatViewWork(); }
    });

    // (134) scroll lock + (103) scroll-up history paging.
    log.addEventListener("scroll", () => {
        // Layout can dispatch scroll before ResizeObserver. That does not mean
        // the user chose to leave the bottom of the conversation.
        if (atBottom && (log.clientHeight !== lastViewportHeight || log.scrollHeight !== lastContentHeight)) scrollToBottom();
        atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 30;
        rememberChatLayout();
        if (atBottom) {
            resetNewCount();
            markRead(activeKey());
        }
        if (log.scrollTop < 40) maybeLoadOlder();
    });
    $("chat-newpill").onclick = () => {
        scrollToBottom();
        resetNewCount();
        atBottom = true;
    };

    // Click delegation: external links (93), reply quotes (107).
    log.addEventListener("click", (e) => {
        const link = e.target.closest(".md-link");
        if (link) {
            e.preventDefault();
            const url = link.getAttribute("data-url");
            if (url) window.runtime.BrowserOpenURL(url);
            return;
        }
        const q = e.target.closest(".msg-quote");
        if (q) scrollToQuote(q);
    });
    // (94) hover preview cards.
    bindLinkCardHover(log);
    initLinkCardDismissal();

    // Header buttons.
    $("chat-pins-btn").onclick = togglePinsPanel;
    $("chat-info-btn").onclick = openDescription;
    $("chat-e2ee-btn").onclick = openE2EEDiagnostics;
    $("chat-export-btn").onclick = exportChat;
    $("chat-search-btn").onclick = () => $("chat-search-row").classList.contains("hidden") ? openSearch() : closeSearch();
    $("chat-search-close").onclick = closeSearch;
    $("chat-search").addEventListener("input", () => {
        cancelChatSearch();
        searchQ = $("chat-search").value.trim();
        applySearchFilter();
        if (searchQ) historySearchTimer = setTimeout(() => { historySearchTimer = null; void searchAll(); }, 450);
    });
    $("chat-search").addEventListener("keydown", (e) => {
        e.stopPropagation();
        if (e.key === "Escape") closeSearch();
        if (e.key === "Enter") { clearTimeout(historySearchTimer); void searchAll(); }
    });
    $("chat-search-server").onclick = searchAll;

    // Emoji picker (95).
    const pollButton = document.createElement("button");
    pollButton.id = "chat-poll";
    pollButton.className = "icon-btn";
    pollButton.textContent = "▥";
    pollButton.title = t("poll.create");
    pollButton.setAttribute("aria-label", t("poll.create"));
    pollButton.onclick = () => {
        if (view.kind === "dm" || (view.kind === "channel" && !V().state.myChannelID)) {
            V().toast(t("poll.channelOnly"), "warn");
            return;
        }
        createPoll(view.kind === "global" ? "global" : "channel", view.kind === "global" ? "" : String(activeChannelID()));
    };
    $("chat-emoji").before(pollButton);
    $("chat-emoji").onclick = (e) => {
        e.stopPropagation();
        toggleEmojiPanel();
    };
    document.addEventListener("click", () => {
        closeEmojiPanel();
        closeReactStrip();
    });

    // File paste + drop + picker (98/100).
    $("chat-text").addEventListener("paste", (e) => {
        const files = [...(e.clipboardData?.files || [])];
        if (files.length === 0) {
            for (const item of [...(e.clipboardData?.items || [])]) {
                if (item.kind !== "file" || !item.type.startsWith("image/")) continue;
                const raw = item.getAsFile();
                if (!raw) continue;
                const ext = item.type.split("/")[1]?.replace("jpeg", "jpg") || "png";
                files.push(raw.name ? raw : new File([raw], `clipboard-${Date.now()}.${ext}`, { type: item.type }));
            }
        }
        if (files.length) {
            e.preventDefault();
            stageFiles(files);
        }
    });
    const wrap = $("chat-wrap");
    let dragDepth = 0; // dragenter/leave fire per child; a counter avoids flicker
    wrap.addEventListener("dragover", (e) => e.preventDefault());
    wrap.addEventListener("dragenter", (e) => {
        e.preventDefault();
        if (++dragDepth === 1) wrap.classList.add("drop-target");
    });
    wrap.addEventListener("dragleave", () => {
        if (--dragDepth <= 0) {
            dragDepth = 0;
            wrap.classList.remove("drop-target");
        }
    });
    wrap.addEventListener("drop", (e) => {
        e.preventDefault();
        dragDepth = 0;
        wrap.classList.remove("drop-target");
        if (e.dataTransfer?.files?.length) stageFiles(e.dataTransfer.files);
    });
    $("chat-attach").onclick = () => $("chat-file").click();
    $("chat-attach").after(voiceMessageButton(() => {
        const scope = captureScope(readSendScope);
        const parent = scope.sendScope === "direct" ? 0 : replyTo?.id || 0;
        return {
            tabID: scope.tabID, channelID: scope.sendScope === "channel" ? scope.channelID : 0,
            isCurrent: () => scopeIsCurrent(scope, readSendScope),
            send: async token => {
                // The recording keeps its uploaded token across retries.
                const key = JSON.stringify(["voice", scope.tabID, scope.target, token]);
                const error = await sendChatOperation(scope.tabID, scope.sendScope, scope.target, token, parent, operationIDFor(key));
                if (!error) sendOperations.delete(key);
                return error;
            },
        };
    }));
    $("chat-file").addEventListener("change", (e) => {
        if (e.target.files?.length) stageFiles(e.target.files);
        e.target.value = ""; // picking the same file twice must re-stage it
    });

    // @nick completion (106) + typing indicator (120).
    $("chat-text").addEventListener("keydown", (e) => {
        if (e.key === "ArrowUp" || e.key === "ArrowDown") recallSent(e);
        if (e.key === "Tab") handleTabComplete(e);
        else tabCycle = null;
    });
    $("chat-text").addEventListener("input", () => {
        draftRevision++;
        $("chat-send-error").classList.add("hidden");
        noteTyping();
    });
    $("chat-text").addEventListener("blur", resetTypingOut);

    // Read receipts need a focused window (124).
    window.addEventListener("focus", sendPendingReads);
    document.addEventListener("visibilitychange", sendPendingReads);

    // Ctrl+F search (110), Ctrl+K quick switcher (135).
    document.addEventListener("keydown", (e) => {
        if (e.ctrlKey && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "f") {
            e.preventDefault();
            openSearch();
        }
        if (e.ctrlKey && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "k") {
            e.preventDefault();
            openQS();
        }
    });

    // Share helpers through the module namespace (clientinfo.js pattern).
    Object.assign(window.__noxa, {
        openPM,
        applyChatPrefs,
        chatUnread: (chID) => unread.get(chID) || null,
        refreshHeader,
        openChannelTab,
        setChannelSubscription,
        isSubscribed,
    });

    window.addEventListener("noxa-language-changed", () => {
        pollButton.title = t("poll.create");
        pollButton.setAttribute("aria-label", t("poll.create"));
        const log = $("chat-log");
        const bottomOffset = log.scrollHeight - log.scrollTop;
        const pending = newCount;
        const pillWasVisible = !$("chat-newpill").classList.contains("hidden");
        renderTabs();
        renderView(bottomOffset, { suppressMarkRead: pillWasVisible });
        requestAnimationFrame(() => {
            newCount = pending;
            if (pillWasVisible && newCount) showNewPill();
        });
    });
    renderTabs();
    renderView();
}

export function unreadFor(chID) {
    return unread.get(chID) || null;
}

// Sidebar shell (app-shell.js): the "Messages" row counts direct messages;
// private groups are added from the conversation list. The quick switcher
// lists the open DM peers and resolves the chat currently on screen.
export function directUnreadTotal() {
    let total = 0;
    for (const peer of pmTabs.values()) total += peer.unread || 0;
    return total;
}
export function directPeers() {
    return [...pmTabs.values()].map(({ uid, nick, unread, unreadAt }) => ({ uid, nick, unread: unread || 0, unreadAt: unreadAt || 0 }));
}
export function activeChatKey() { return activeKey(); }

// Tray counter (main.js): the same badges unreadMessageReferences lists.
export function unreadTotal() {
    let total = 0;
    for (const [channelID, value] of unread) if (channelID === 0 || subscribed(channelID)) total += value.n || 0;
    for (const peer of pmTabs.values()) total += peer.unread || 0;
    return total;
}

export function unreadMessageReferences() {
    const channels = [...unread.entries()].filter(([channelID]) => channelID === 0 || subscribed(channelID)).map(([channelID, value]) => ({
        kind: "channel", channel_id: channelID,
        message_id: getStore(`ch:${channelID}`).msgs.filter(message => !message.self && message.id).at(-1)?.id || 0,
        name: V().state.channels.find(channel => Number(channel.ChannelID) === channelID)?.Name || (channelID ? `#${channelID}` : "Global"),
        count: value.n, mention: value.mention,
    }));
    return channels.concat([...pmTabs.values()].filter(peer => peer.unread > 0).map(peer => ({ kind: "dm", peer_id: peer.uid, name: peer.nick, count: peer.unread })));
}

// Historical jumps open an authenticated context window without splicing a
// disconnected old page into the live store (which would create paging gaps).
export async function jumpMessageReference(reference, options = {}) {
    const destinationCurrent = () => !options.isCurrent || options.isCurrent();
    if (!destinationCurrent()) return;
    if (reference.kind === "group") {
        await openConversationsAt(reference.group_id, reference.message_id || 0, destinationCurrent);
        return;
    }
    if (reference.kind === "dm") {
        if (!reference.client_message_id && !reference.local_seq) { openPM(reference.peer_id, reference.name); return; }
        const owner = getDMOwner();
        const rows = await callDMHistory(owner, "DMHistoryLoadForContext", reference.peer_id);
        if (!dmOwnerIsCurrent(owner) || !destinationCurrent()) return;
        const entry = rows.find(row => reference.client_message_id ? row.client_msg_id === reference.client_message_id : Number(row.seq) === Number(reference.local_seq));
        if (!entry) { V().toast(t("messages.notFound"), "warn", "alert", { bypassDND: !!options.continueConversation }); return; }
        const index = rows.indexOf(entry), name = pmTabs.get(reference.peer_id)?.nick || reference.peer_id;
        const context = rows.slice(Math.max(0, index - 19), index + 1).map(row => dmMsg(row, name));
        showMessageReferenceContext(`DM · ${name}`, context, context.at(-1), options.continueConversation
            ? () => { if (dmOwnerIsCurrent(owner)) openPM(reference.peer_id, name); } : null);
        return;
    }
    const scope = captureScope(readChatServerScope);
    const channelID = Number(reference.channel_id), targetID = Number(reference.message_id);
    if (!targetID) { if (channelID) await openChannelTab(channelID); else setView({ kind: "global" }); return; }
    const page = await app().ChatHistoryForTab(scope.tabID, channelID, targetID + 1, 20);
    if (!chatServerIsCurrent(scope) || !destinationCurrent()) return;
    if (!(page.messages || []).some(message => message.id === targetID && !message.deleted && message.enc_verified)) {
        V().toast(t("messages.notFound"), "warn", "alert", { bypassDND: !!options.continueConversation }); return;
    }
    if (!isPrivateGroupActive() && $("chat-log").getClientRects().length && channelID === (activeChannelID() || 0) && flashMsg(targetID)) return;
    const messages = [...page.messages].reverse().map(message => attribute(normalize(message, channelID)));
    const name = V().state.channels.find(channel => Number(channel.ChannelID) === channelID)?.Name || (channelID ? `#${channelID}` : "Global");
    showMessageReferenceContext(name, messages, messages.find(message => message.id === targetID), options.continueConversation
        ? () => { if (chatServerIsCurrent(scope)) { if (channelID) void openChannelTab(channelID); else setView({ kind: "global" }); } } : null);
}

function showMessageReferenceContext(name, messages, target, continueConversation = null) {
    const overlay = document.createElement("div"); overlay.className = "dlg-overlay";
    const box = document.createElement("section"); box.className = "dlg message-tools-dialog";
    const heading = document.createElement("h2"); heading.textContent = name;
    const list = document.createElement("div"); list.className = "message-tools-list";
    for (const message of messages) {
        const row = document.createElement("article"); row.className = "message-tool-result"; row.dataset.messageId = String(message.id);
        const author = document.createElement("strong"); author.textContent = message.from || "?";
        const body = document.createElement("p");
        // Attachment secrets remain inside the existing scoped renderer.
        renderBody(body, message);
        row.append(author, body); list.append(row);
        if (message === target) row.classList.add("message-reference-flash");
    }
    const close = document.createElement("button"); close.textContent = t("messages.close"); close.onclick = () => closeDialog(overlay);
    box.append(heading, list);
    if (continueConversation) {
        const open = document.createElement("button"); open.textContent = t("messages.openConversation");
        open.onclick = () => { if (!isCurrentServerDialog(overlay)) return; closeDialog(overlay); continueConversation(); };
        box.append(open);
    }
    box.append(close); overlay.append(box); mountServerDialog(overlay);
    list.querySelector(".message-reference-flash")?.scrollIntoView({ block: "center" });
}

// Loaded live messages supplement authenticated history without opening tabs.
function activityEntry(kind, id, name, message) {
    const reference = kind === "dm"
        ? { kind, peer_id: id, client_message_id: message.clientMsgID || "", local_seq: message.localSeq || 0 }
        : kind === "group" ? { kind, group_id: id, message_id: message.id }
        : { kind, channel_id: Number(id), message_id: message.id };
    return { key: `${kind}:${id}:${message.id || message.clientMsgID || message.localSeq}`, reference, name, message,
        cursor: kind === "dm" ? message.localSeq : message.id };
}

export function activityMessages() {
    const state = V().state;
    if (!state.myClientID || !state.myUniqueID) return [];
    const entries = [];
    for (const [key, value] of store) {
        const dm = key.startsWith("dm:"), id = key.slice(3);
        if (!dm && Number(id) !== 0 && !state.channels.some(c => c.ChannelID === Number(id) && c.SystemKind !== "home")) continue;
        for (const message of value.msgs) entries.push(activityEntry(dm ? "dm" : "channel", id,
            dm ? peerName(id) : (state.channels.find(c => c.ChannelID === Number(id))?.Name || "Global"), message));
    }
    return entries;
}

export function activityHistory(current, changed) {
    const captured = captureScope(readDMHistoryScope), owner = getDMOwner();
    const valid = () => current() && dmHistoryScopeIsCurrent(captured) && dmOwnerIsCurrent(owner);
    const request = command => app().ConversationForTab(captured.tabID, command);
    const denied = error => /permission|denied|not a member|publish an encryption key|not supported/i.test(String(error));
    return createActivityHistory({ current: valid, changed,
        async listSources() {
            const [peers, groups] = await Promise.allSettled([
                callDMHistory(owner, "DMHistoryPeersForContext"), request({ action: "list" }),
            ]);
            if (!valid()) return [];
            const channels = [{ ChannelID: 0, Name: "Global" }, ...V().state.channels.filter(channel => channel.SystemKind !== "home")];
            const result = channels.map(channel => ({ key: `channel:${channel.ChannelID}`, kind: "channel", id: channel.ChannelID, name: channel.Name }));
            for (const peer of peers.status === "fulfilled" ? peers.value || [] : []) result.push({ key: `dm:${peer.unique_id}`, kind: "dm", id: peer.unique_id, name: knownPeerName(peer.unique_id, peer.nickname) });
            for (const group of groups.status === "fulfilled" ? groups.value?.conversations || [] : []) {
                if (!group.members?.some(member => member.unique_id === sessionUserID(V().state) && !member.pending)) continue;
                result.push({ key: `group:${group.id}`, kind: "group", id: group.id, name: group.name });
            }
            const failed = [peers, groups].some(result => result.status === "rejected" && !denied(result.reason) && !/private groups unavailable|unsupported/i.test(String(result.reason)));
            return { sources: [...new Map(result.map(source => [source.key, source])).values()], failed };
        },
        async loadPage(source, before) {
            if (!valid()) throw new Error("Activity owner changed");
            let rows, messages, size = PAGE;
            try {
                if (source.kind === "channel") {
                    rows = (await app().ChatHistoryForTab(captured.tabID, source.id, before, size))?.messages || [];
                    messages = rows.map(row => attribute(normalize(row, source.id)));
                } else if (source.kind === "dm") {
                    rows = (await callDMHistory(owner, "DMHistoryLoadForContext", source.id) || [])
                        .filter(row => !before || Number(row.seq) < before).sort((a, b) => b.seq - a.seq).slice(0, size);
                    messages = rows.map(row => dmMsg(row, source.name));
                } else {
                    size = 25;
                    rows = (await request({ action: "history", id: source.id, before_id: before }))?.messages || [];
                    messages = rows.map(row => attribute(normalize({ ...row, from: peerName(row.from_unique_id), sent_at: row.created_at,
                        enc_mode: row.enc_verified ? "e2e" : "" }, null)));
                }
            } catch (error) {
                if (denied(error)) return { entries: [], denied: true };
                throw error;
            }
            const entries = messages.map(message => activityEntry(source.kind, source.id, source.name, message));
            const ids = entries.map(entry => entry.cursor).filter(id => id > 0);
            const cursor = ids.length ? Math.min(...ids) : 0;
            return { entries, cursor, more: rows.length >= size && cursor > 0 && (!before || cursor < before) };
        },
    });
}

export function renderActivityMessage(container, entry) {
    if (entry.reference.kind !== "group") {
        renderBody(container, { ...entry.message, isDM: entry.reference.kind === "dm" });
        return;
    }
    const scope = captureScope(readChatServerScope), id = entry.reference.group_id;
    const current = () => container.isConnected && chatServerIsCurrent(scope);
    const download = ref => app().DownloadConversationAttachmentForTab(scope.tabID, id, ref.storage, ref.key);
    const voiceDownload = ref => download(ref).catch(error => {
        if (!ref.key || !current()) throw error;
        return app().DownloadChatAttachmentForTab(scope.tabID, 0, ref.storage, ref.key);
    });
    if (renderVoiceMessage(container, entry.message.text, { tabID: scope.tabID, channelID: 0, isCurrent: current, download: voiceDownload })) return;
    renderSharedRichText(container, entry.message.text, {
        download,
        previewCurrent: current, saveCurrent: current,
        save: (ref, name) => app().SaveConversationAttachmentForTab(scope.tabID, id, ref.storage, ref.key, name),
    });
}

// (458) applyRemoteRead merges a read position another device of this account
// reported (read-sync.js). Positions only move forward: a channel marker with
// an id advances last_read_channels to max(local, remote) and clears the badge
// only when nothing newer is known here; a marker without an id (the other
// device opened the chat) clears the badge the way our own view would.
export function applyRemoteRead(marker) {
    if (!marker || !marker.id) return;
    if (marker.scope === "channel") {
        const chID = Number(marker.id);
        if (!Number.isFinite(chID) || chID < 0) return; // 0 is global chat
        const s = V().state.settings;
        if (marker.messageID && s) {
            s.last_read_channels = s.last_read_channels || {};
            const next = mergeReadMarker(s.last_read_channels[chID], marker.messageID);
            if (next > (Number(s.last_read_channels[chID]) || 0)) { s.last_read_channels[chID] = next; persistSettings(); }
        }
        if (!unread.has(chID)) return;
        const msgs = store.get(`ch:${chID}`)?.msgs || [];
        const newest = msgs.length ? msgs[msgs.length - 1].id : 0;
        if (marker.messageID && newest > marker.messageID) return;
        unread.delete(chID);
        V().renderTree();
        renderTabs();
        return;
    }
    if (marker.scope === "dm") {
        const tab = pmTabs.get(marker.id);
        if (!tab?.unread) return;
        tab.unread = 0;
        tab.unreadAt = undefined;
        renderTabs();
    }
}
