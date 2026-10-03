import { t } from "./i18n.js";
import { closeDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { sessionUserID } from "./session-identity.js";
import { voiceMessageButton, renderVoiceMessage } from "./voice-messages.js";
import { openWebhooks } from "./webhooks.js";
import "./discussions.css";

const V = () => window.__noxa;
let active = null;
let notifying = false;
let bridgeQueue = Promise.resolve();
const channelBoards = new Map();
const boardKey = channelID => `${V().state.activeTabID}:${V().state.serverGeneration}:${channelID}`;
function bridgeRequest(tabID, generation, request) {
    const operation = bridgeQueue.catch(() => {}).then(() => {
        if (tabID !== V().state.activeTabID || generation !== V().state.serverGeneration) throw new Error("Server changed");
        return window.go.main.App.DiscussionForTab(tabID, request);
    });
    bridgeQueue = operation;
    return operation;
}
const el = (tag, className = "", text) => {
    const node = document.createElement(tag); node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
};
const button = (key, action) => { const node = el("button", "", t(key)); node.type = "button"; node.onclick = action; return node; };
const field = (key, input) => {
    const label = el("label", input.type === "checkbox" ? "discussion-check" : "discussion-field", t(key));
    if (input.type === "checkbox") label.prepend(input);
    else { input.classList.add("dlg-input"); label.append(input); }
    return label;
};
const requestID = () => crypto.randomUUID();

export function closeDiscussions() { if (active) closeDialog(active.overlay, "navigate"); }
export function resetDiscussions() { closeDiscussions(); channelBoards.clear(); }
export async function discussionChanged(event) {
    const board = channelBoards.get(boardKey(Number(event.channel_id)));
    if (board) { board.loaded = false; board.repaint?.(); }
    if (active && Number(event.channel_id) === active.channelID) { active.refresh(); return; }
    if (!event.new_message || !event.thread_id || event.author === sessionUserID(V().state) || notifying) return;
    const { activeTabID: tabID, serverGeneration: generation } = V().state;
    notifying = true;
    try {
        const result = await bridgeRequest(tabID, generation, { action: "state", channel_id: Number(event.channel_id), thread_id: Number(event.thread_id) });
        if (tabID !== V().state.activeTabID || generation !== V().state.serverGeneration) return;
        const thread = result.threads?.[0];
        if (thread?.joined && thread.subscribed && thread.unread) window.__noxaNotify?.notify("channel_message", t("discussion.notification", { title: thread.title }), { channelID: Number(event.channel_id), uid: event.author });
    } catch { /* A revoked channel must not disclose the old thread title. */ }
    finally { notifying = false; }
}

// A channel's persisted forum mode controls its default view. The classic chat
// remains reachable so existing messages and drafts are never discarded.
export function renderForumChannel(log, channelID, repaint) {
    const input = document.getElementById("chat-input-row");
    input?.classList.remove("discussion-composer-hidden");
    if (!(channelID > 0) || !window.go?.main?.App?.DiscussionForTab) return false;
    const key = boardKey(channelID);
    if (!channelBoards.has(key)) {
        if (channelBoards.size > 40) channelBoards.clear();
        channelBoards.set(key, { loaded: false, busy: false, chat: false, data: null });
    }
    const board = channelBoards.get(key); board.repaint = repaint;
    if (!board.loaded && !board.busy) {
        board.busy = true;
        const { activeTabID: tabID, serverGeneration: generation } = V().state;
        void bridgeRequest(tabID, generation, { action: "list", channel_id: channelID }).then(result => {
            if (key !== boardKey(channelID)) return;
            board.data = result;
        }).catch(() => { board.data = null; }).finally(() => {
            board.busy = false; board.loaded = true;
            if (key === boardKey(channelID)) repaint();
        });
    }
    if (!board.data?.forum) return false;
    if (board.chat) {
        const back = button("discussion.forum", () => { board.chat = false; repaint(); });
        back.className = "discussion-chat-toggle"; log.append(back); return false;
    }
    input?.classList.add("discussion-composer-hidden");
    const section = el("section", "discussion-channel-board");
    section.setAttribute("aria-label", t("discussion.forum"));
    const header = el("div", "discussion-header");
    header.append(el("h2", "", t("discussion.forum")), button("discussion.new", () => openDiscussions(channelID, { text: "" })), button("discussion.open", () => openDiscussions(channelID)), button("discussion.chat", () => { board.chat = true; repaint(); }));
    section.append(header, el("p", "discussion-hint", t("discussion.membership")));
    for (const thread of board.data.threads || []) {
        const post = el("button", "discussion-post"); post.type = "button";
        post.append(el("strong", "", thread.title), el("span", "discussion-meta", (thread.tags || []).join(" · ")), el("span", "discussion-meta", t("discussion.replies", { count: thread.message_count })));
        if (thread.unread) post.append(el("span", "discussion-unread", t("discussion.unread")));
        post.onclick = () => openDiscussions(channelID, null, thread.id); section.append(post);
    }
    if (!board.data.threads?.length) section.append(el("p", "discussion-hint", t("discussion.empty")));
    log.append(section); return true;
}

export function initDiscussions(readChannel) {
    if (document.getElementById("chat-discussions")) return;
    const open = button("discussion.open", () => openDiscussions(readChannel()));
    open.id = "chat-discussions"; open.className = "icon-btn";
    open.textContent = "☷"; open.title = t("discussion.open"); open.setAttribute("aria-label", t("discussion.open"));
    document.getElementById("chat-emoji")?.before(open);
}

export function openDiscussions(channelID, source = null, initialThreadID = 0, targetMessageID = 0) {
    channelID = Number(channelID);
    if (!(channelID > 0)) { V().toast(t("discussion.channelOnly"), "warn"); return; }
    if (!window.go.main.App.DiscussionForTab) { V().toast(t("discussion.unavailable"), "warn"); return; }
    closeDiscussions();
    const tabID = V().state.activeTabID, generation = V().state.serverGeneration;
    const overlay = el("div", "dlg-overlay");
    const dialog = el("section", "dlg discussion-dialog"); overlay.append(dialog);
    const heading = el("h2", "", t("discussion.title"));
    const header = el("div", "discussion-header");
    header.append(heading, button("discussion.close", () => closeDialog(overlay, "close")));
    const toolbar = el("div", "discussion-toolbar");
    const content = el("div", "discussion-content");
    const status = el("p", "discussion-status"); status.setAttribute("role", "status");
    dialog.append(header, toolbar, content, status);
    let busy = false, pendingRefresh = false, selected = initialThreadID, data = null, archived = false, tag = "", following = false;
    let mode = "list", replyDraft = "", messages = [], posts = [], requestSequence = 0;
    let targetPages = 0;
    const current = () => isCurrentServerDialog(overlay) && tabID === V().state.activeTabID && generation === V().state.serverGeneration;
    const disable = value => { for (const control of dialog.querySelectorAll("button,input,textarea,select")) control.disabled = value; };
    const request = async (action, extra = {}, append = false) => {
        if (!current() || busy) return false;
        busy = true; disable(true); status.textContent = t("discussion.loading");
        const sequence = ++requestSequence;
        try {
            const result = await bridgeRequest(tabID, generation, { action, channel_id: channelID, thread_id: selected, ...extra });
            if (!current() || sequence !== requestSequence) return false;
            if (result.channel_id !== channelID || result.action !== action) throw new Error(t("discussion.unavailable"));
            data = result;
            if (action === "configure") {
                const board = channelBoards.get(boardKey(channelID));
                if (board) { board.loaded = false; board.repaint?.(); }
            }
            if (action === "create") selected = result.thread_id;
            if (selected) {
                mode = "thread";
                const merged = append ? [...(result.messages || []), ...messages] : [...(result.messages || [])];
                messages = [...new Map(merged.map(message => [message.id, message])).values()].sort((a, b) => a.id - b.id);
            } else {
                mode = "list";
                posts = append ? [...posts, ...(result.threads || [])] : result.threads || [];
            }
            status.textContent = ""; render();
            if (targetMessageID && selected) {
                const row = content.querySelector(`[data-discussion-message="${Number(targetMessageID)}"]`);
                if (row) { row.tabIndex = -1; row.focus(); row.scrollIntoView({ block: "center" }); row.classList.add("discussion-target"); targetMessageID = 0; }
                else if (result.has_more && messages.length && ++targetPages < 50) { queueMicrotask(() => request("get", { before_id: messages[0].id }, true)); }
                else { status.textContent = t("discussion.messageMissing"); targetMessageID = 0; }
            }
            return true;
        } catch (error) {
            if (current()) status.textContent = t("discussion.error", { error: String(error) });
            return false;
        } finally {
            busy = false;
            if (current()) {
                disable(false);
                if (pendingRefresh && (mode === "thread" || mode === "list")) { pendingRefresh = false; void refresh(); }
            }
        }
    };
    const refresh = () => {
        if (!current()) return;
        if (busy || !["list", "thread"].includes(mode)) { pendingRefresh = true; return; }
        return selected ? request("get") : request("list", { archived, tags: tag ? [tag] : [] });
    };
    const back = () => { selected = 0; replyDraft = ""; mode = "list"; void refresh(); };
    const render = () => {
        toolbar.replaceChildren(); content.replaceChildren();
        heading.textContent = selected ? data.threads?.[0]?.title || t("discussion.title") : t(data.forum ? "discussion.forum" : "discussion.title");
        if (selected) renderThread(); else renderList();
    };
    const renderList = () => {
        const state = el("select", "dlg-input"); state.setAttribute("aria-label", t("discussion.archived"));
        for (const [value, key] of [["false", "discussion.active"], ["true", "discussion.archived"]]) { const option = el("option", "", t(key)); option.value = value; state.append(option); }
        state.value = String(archived); state.onchange = () => { archived = state.value === "true"; void refresh(); };
        const tags = el("select", "dlg-input"); tags.setAttribute("aria-label", t("discussion.tags"));
        const all = el("option", "", t("discussion.allTags")); all.value = ""; tags.append(all);
        for (const name of data.tags || []) { const option = el("option", "", name); option.value = name; tags.append(option); }
        tags.value = tag; tags.onchange = () => { tag = tags.value; void refresh(); };
        const followed = el("input"); followed.type = "checkbox"; followed.checked = following;
        followed.onchange = () => { following = followed.checked; render(); };
        const followLabel = field("discussion.following", followed);
        const filters = el("div", "discussion-filters");
        filters.append(state, tags, followLabel);
        const newPost = button("discussion.new", () => createForm()); newPost.className = "discussion-new";
        toolbar.append(newPost, button("discussion.refresh", refresh));
        if (data.can_manage) toolbar.append(button("discussion.configure", configure));
        if (data.can_manage) toolbar.append(button("webhook.title", () => openWebhooks(channelID)));
        toolbar.append(filters);
        content.append(el("p", "discussion-hint", t("discussion.membership")));
        const shown = posts.filter(post => !following || post.subscribed).sort((a,b) => Number(b.pinned)-Number(a.pinned));
        if (!shown.length) content.append(el("p", "discussion-hint", t("discussion.empty")));
        for (const post of shown) {
            const card = el("button", "discussion-post"); card.type = "button";
            card.append(el("strong", "", post.title));
            const tags = el("div", "discussion-tags");
            if (post.pinned) tags.append(el("span", "discussion-tag", t("discussion.pinned")));
            if (post.resolved) tags.append(el("span", "discussion-tag", t("discussion.resolved")));
            for (const tag of post.tags || []) tags.append(el("span", "discussion-tag", tag));
            if (post.unread) tags.append(el("span", "discussion-unread", t("discussion.unread")));
            card.append(tags, el("span", "discussion-meta", t("discussion.replies", { count: post.message_count })));
            card.onclick = () => { selected = post.id; replyDraft = ""; void request("get"); };
            content.append(card);
        }
        if (data.has_more && posts.length) content.append(button("discussion.loadMore", () => request("list", { before_id: posts.at(-1).id, before_pinned: Boolean(posts.at(-1).pinned), archived, tags: tag ? [tag] : [] }, true)));
    };
    const renderThread = () => {
        const thread = data.threads?.[0];
        toolbar.append(button("discussion.back", back), button("discussion.refresh", refresh));
        if (!thread) return;
        toolbar.append(button(thread.joined ? "discussion.leave" : "discussion.join", () => request(thread.joined ? "leave" : "join")));
        if (thread.joined) toolbar.append(button(thread.subscribed ? "discussion.unsubscribe" : "discussion.subscribe", () => request("subscribe", { subscribed: !thread.subscribed })));
        if (thread.author === sessionUserID(V().state) || data.can_moderate) toolbar.append(button(thread.archived ? "discussion.reopen" : "discussion.archive", () => request(thread.archived ? "reopen" : "archive")));
        if (thread.author === sessionUserID(V().state) || data.can_moderate) {
            toolbar.append(button("discussion.edit", () => editThread(thread)), button(thread.resolved ? "discussion.unresolve" : "discussion.resolve", () => request("resolve", { resolved: !thread.resolved })));
        }
        if (data.can_moderate) toolbar.append(button(thread.pinned ? "discussion.unpin" : "discussion.pin", () => request("pin", { pinned: !thread.pinned })));
        if (thread.root_message_id) content.append(el("p", "discussion-hint", t("discussion.source")));
        if (data.has_more && messages.length) content.append(button("discussion.loadMore", () => request("get", { before_id: messages[0].id }, true)));
        for (const message of messages) {
            const row = el("article", "discussion-message");
            row.dataset.discussionMessage = message.id;
            const body = el("div", "discussion-message-body");
            if (!renderVoiceMessage(body, message.body, { tabID, channelID, isCurrent: current })) body.textContent = message.body || "";
            row.append(el("strong", "", message.from_nickname), el("span", "discussion-meta", ` · ${new Date(message.sent_at * 1000).toLocaleString()}`), body);
            if (window.__noxaMessageTools?.save) row.append(button("discussion.saveMessage", () => window.__noxaMessageTools.save({kind:"thread", channel_id:channelID, thread_id:selected, message_id:message.id})));
            content.append(row);
        }
        if (thread.archived || !thread.joined) { content.append(el("p", "discussion-hint", t(thread.archived ? "discussion.archivedHint" : "discussion.joinHint"))); return; }
        const form = el("form", "discussion-field"); const input = el("textarea"); input.maxLength = 12000; input.required = true; input.value = replyDraft;
        input.oninput = () => { replyDraft = input.value; };
        const send = button("discussion.send", null); send.type = "submit";
        form.append(field("discussion.reply", input), send);
        form.append(voiceMessageButton(() => {
            const threadID = selected, voiceID = requestID();
            return { tabID, channelID, isCurrent: () => current() && selected === threadID && mode === "thread" && data.threads?.[0]?.joined && !data.threads[0].archived,
                send: async token => await request("send", { text: token, request_id: voiceID }) ? "" : status.textContent || t("discussion.unavailable") };
        }));
        let id = requestID();
        form.onsubmit = async event => { event.preventDefault(); if (!input.value.trim() || busy) return; const text = input.value; if (await request("send", { text, request_id: id })) { replyDraft = ""; id = requestID(); if (current()) render(); } };
        content.append(form);
    };
    const createForm = (sourceMessage = null) => {
        mode = "create"; toolbar.replaceChildren(button("discussion.back", back)); content.replaceChildren();
        const form = el("form", "discussion-field");
        const title = el("input"); title.required = true; title.maxLength = 120;
        const body = el("textarea"); body.required = true; body.maxLength = 12000;
        if (sourceMessage) body.value = sourceMessage.text || "";
        const tags = el("div", "discussion-tags"); const selectedTags = new Set();
        for (const tag of data?.tags || []) { const check = el("input"); check.type = "checkbox"; check.onchange = () => check.checked ? selectedTags.add(tag) : selectedTags.delete(tag); const label = el("label", "discussion-check", tag); label.prepend(check); tags.append(label); }
        const submit = button("discussion.create", null); submit.type = "submit";
        form.append(field("discussion.titleField", title), field("discussion.body", body), tags, el("p", "discussion-hint", t("discussion.metadata")), submit);
        form.append(voiceMessageButton(() => ({ tabID, channelID, isCurrent: () => current() && form.isConnected && mode === "create", send: async token => { body.value = token; return ""; } })));
        const id = requestID();
        form.onsubmit = event => { event.preventDefault(); if (!busy) void request("create", { title: title.value.trim(), text: body.value.trim(), tags: [...selectedTags], root_message_id: sourceMessage?.id || 0, request_id: id }); };
        content.append(form); title.focus();
    };
    const editThread = thread => {
        mode = "edit"; toolbar.replaceChildren(button("discussion.back", () => { mode = "thread"; void refresh(); })); content.replaceChildren();
        const form = el("form", "discussion-field"), title = el("input"), tags = el("div", "discussion-tags");
        title.required = true; title.maxLength = 120; title.value = thread.title;
        const selectedTags = new Set(thread.tags || []);
        for (const name of data.tags || []) { const input = el("input"); input.type = "checkbox"; input.checked = selectedTags.has(name); input.onchange = () => input.checked ? selectedTags.add(name) : selectedTags.delete(name); tags.append(field(name, input)); }
        const save = button("discussion.save", null); save.type = "submit";
        form.append(field("discussion.titleField", title), tags, save);
        form.onsubmit = event => { event.preventDefault(); void request("edit", {title:title.value.trim(),tags:[...selectedTags].filter(tag => data.tags.includes(tag))}); };
        content.append(form); title.focus();
    };
    const configure = () => {
        mode = "configure"; toolbar.replaceChildren(button("discussion.back", back)); content.replaceChildren();
        const form = el("form", "discussion-field"); const forum = el("input"); forum.type = "checkbox"; forum.checked = data.forum;
        const forumLabel = field("discussion.forumMode", forum);
        const tags = el("textarea"); tags.value = (data.tags || []).join("\n"); tags.maxLength = 396;
        const submit = button("discussion.save", null); submit.type = "submit";
        const archive = el("select");
        for (const hours of [0,24,72,168]) { const option = el("option", "", t(hours ? "discussion.archiveHours" : "discussion.archiveNever", {hours})); option.value = hours; archive.append(option); }
        archive.value = String(data.auto_archive_hours || 0);
        form.append(forumLabel, field("discussion.availableTags", tags), field("discussion.autoArchive", archive), submit);
        form.onsubmit = event => {
            event.preventDefault();
            const values = tags.value.split(/\r?\n/).map(tag => tag.trim()).filter(Boolean);
            if (values.length > 12 || new Set(values).size !== values.length || values.some(tag => [...tag].length > 32)) { status.textContent = t("discussion.invalidTags"); return; }
            void request("configure", { forum: forum.checked, tags: values, auto_archive_hours: Number(archive.value) });
        };
        content.append(form);
    };
    active = { overlay, channelID, refresh };
    mountServerDialog(overlay, { onClose: () => { requestSequence++; if (active?.overlay === overlay) active = null; } });
    void request(selected ? "get" : "list").then(ok => { if (ok && source && current()) createForm(source); });
}
