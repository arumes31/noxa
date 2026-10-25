// threads.js — threads and forum posts of a channel (DiscussionRequest,
// core/discussions.js): the list (#/threads/<channel>, with filters and the
// new-thread form at #/threads/<channel>/new[/<rootMessageID>]) and one
// thread (#/thread/<channel>/<thread>) with its replies, membership, follow
// and the author's and moderators' actions. Managers configure forum mode,
// the channel's tags and auto-archive here as well.
import { app } from "../app.js";
import { withRateRetry } from "../core/poll-state.js";
import { t } from "../i18n.js";
import { avatar, clock, h, sheet, toast, replaceContent } from "./dom.js";
import { icon } from "./icons.js";
import { header, scopeName } from "./common.js";
import { richBody, watchCustomEmoji } from "./rich-text.js";
import { chooseChat, discussionsFor } from "./chat-tools.js";
import { personName } from "./tool-sources.js";
import { encryptionBadge, headerActions, lineCache, menuButton, relativeTicker, timeNode, watchDisplaySettings, withDays } from "./message-view.js";
import "./chat-tools-strings.js";
import "./chat-tools.css";

function when(seconds) {
    if (!seconds) return "";
    const date = new Date(seconds * 1000);
    return date.toDateString() === new Date().toDateString() ? clock(seconds) : date.toLocaleDateString();
}

function threadChips(thread) {
    return h("span", { class: "chips" },
        thread.pinned ? h("span", { class: "chip chip-accent" }, t("threads.pinned")) : null,
        thread.resolved ? h("span", { class: "chip chip-ok" }, t("threads.resolved")) : null,
        thread.archived ? h("span", { class: "chip chip-warn" }, t("threads.archived")) : null,
        thread.unread ? h("span", { class: "chip chip-accent" }, t("threads.unread")) : null,
        (thread.tags || []).map(tag => h("span", { class: "chip" }, tag)));
}

/** A modal form sheet; build(close) returns the form's children. */
function formSheet(title, build) {
    const opener = document.activeElement;
    const form = h("form", { class: "sheet tool-form", role: "dialog", "aria-modal": "true", "aria-label": title });
    const overlay = h("div", { class: "sheet-overlay", onclick: event => { if (event.target === overlay) close(); } }, form);
    const onKey = event => { if (event.key === "Escape") close(); };
    function close() {
        overlay.remove();
        document.removeEventListener("keydown", onKey);
        opener?.focus?.();
    }
    form.append(h("h2", { class: "sheet-title" }, title), ...build(close, form));
    document.addEventListener("keydown", onKey);
    document.body.append(overlay);
    form.querySelector("input:not([type=checkbox]), textarea")?.focus();
    return { form, close };
}

function tagPicker(available, chosen = []) {
    const selected = new Set(chosen);
    const node = h("fieldset", { class: "chips", style: "border:0;padding:0;margin:0" },
        h("legend", { class: "field-label" }, t("threads.tags")),
        available.map(tag => {
            const box = h("input", { type: "checkbox", checked: selected.has(tag) || null, onchange: () => box.checked ? selected.add(tag) : selected.delete(tag) });
            return h("label", { class: "check-chip" }, box, tag);
        }));
    return { node: available.length ? node : null, value: () => available.filter(tag => selected.has(tag)) };
}

/** The new thread/post form; resolves after navigating to the new thread. */
function openNewThread(session, channelID, { rootMessageID = 0, text = "" } = {}) {
    const discussions = discussionsFor(session);
    const state = discussions.channels.get(channelID) || {};
    const title = h("input", { class: "input", id: "thread-title", maxlength: "120", required: true, autocomplete: "off" });
    const body = h("textarea", { class: "input", id: "thread-body", maxlength: "12000", required: true });
    body.value = text;
    const tags = tagPicker(state.tags || []);
    const error = h("p", { class: "error", role: "alert" });
    const submit = h("button", { class: "btn btn-primary", type: "submit" }, t(state.forum ? "threads.createPost" : "threads.create"));
    const { form, close } = formSheet(t(state.forum ? "threads.newPost" : "threads.new"), close => [
        h("div", { class: "field" }, h("label", { for: "thread-title" }, t("threads.titleField")), title),
        h("div", { class: "field" }, h("label", { for: "thread-body" }, t("threads.firstMessage")), body),
        tags.node,
        rootMessageID ? h("p", { class: "hint" }, t("threads.fromMessage")) : null,
        error,
        h("div", { class: "sheet-actions" }, submit, h("button", { class: "btn", type: "button", onclick: close }, t("link.cancel"))),
    ]);
    form.onsubmit = async event => {
        event.preventDefault();
        if (!title.value.trim() || !body.value.trim()) return;
        submit.disabled = true;
        try {
            const result = await discussions.create(channelID, { title: title.value.trim(), tags: tags.value(), text: body.value.trim(), rootMessageID });
            close();
            location.hash = `#/thread/${channelID}/${result.thread_id}`;
        } catch (failure) {
            error.textContent = t("common.error", { error: failure.message });
            submit.disabled = false;
        }
    };
}

/** Forum settings for managers: forum mode, tags (one per line), auto-archive. */
function openForumSettings(session, channelID) {
    const discussions = discussionsFor(session);
    const state = discussions.channels.get(channelID) || {};
    const forum = h("input", { type: "checkbox", checked: state.forum || null });
    const tags = h("textarea", { class: "input", id: "forum-tags", maxlength: "396" });
    tags.value = (state.tags || []).join("\n");
    const archive = h("select", { class: "input", id: "forum-archive" },
        [0, 24, 72, 168].map(hours => h("option", { value: String(hours), selected: (state.autoArchiveHours || 0) === hours || null }, hours ? t("threads.archiveAfter", { hours }) : t("threads.archiveNever"))));
    const error = h("p", { class: "error", role: "alert" });
    const submit = h("button", { class: "btn btn-primary", type: "submit" }, t("threads.save"));
    const { form, close } = formSheet(t("threads.settings"), close => [
        h("label", { class: "toggle" }, forum, h("span", { class: "row-main" }, h("span", { class: "row-title" }, t("threads.forumMode")), h("span", { class: "row-sub", style: "white-space:normal" }, t("threads.forumHelp")))),
        h("div", { class: "field" }, h("label", { for: "forum-tags" }, t("threads.availableTags")), tags),
        h("div", { class: "field" }, h("label", { for: "forum-archive" }, t("threads.autoArchive")), archive),
        error,
        h("div", { class: "sheet-actions" }, submit, h("button", { class: "btn", type: "button", onclick: close }, t("link.cancel"))),
    ]);
    form.onsubmit = async event => {
        event.preventDefault();
        const values = tags.value.split(/\r?\n/).map(tag => tag.trim()).filter(Boolean);
        if (values.length > 12 || new Set(values).size !== values.length || values.some(tag => [...tag].length > 32)) {
            error.textContent = t("threads.invalidTags");
            return;
        }
        submit.disabled = true;
        try {
            await discussions.configure(channelID, { forum: forum.checked, tags: values, autoArchiveHours: Number(archive.value) });
            close();
            toast(t("threads.saved"));
        } catch (failure) {
            error.textContent = t("common.error", { error: failure.message });
            submit.disabled = false;
        }
    };
}

/** #/threads/<channel>[/new[/<rootMessageID>]] */
export function renderThreads(root, arg) {
    const session = app.session;
    const [channelPart, mode, rootPart] = String(arg).split("/");
    const channelID = Number(channelPart) || 0;
    const discussions = discussionsFor(session);
    const name = scopeName(session, channelID);
    const filters = { following: false, archived: false, tag: "" };
    const body = h("div", { class: "screen-body" });
    const list = h("ul", { class: "list", "aria-label": t("threads.title") });
    const status = h("p", { class: "tool-progress", role: "status" });
    const more = h("button", { class: "btn", type: "button", hidden: true }, t("threads.loadMore"));
    const filterBar = h("div", { class: "tool-actions" });
    let loading = false;

    const state = () => discussions.channels.get(channelID) || { threads: [], tags: [] };

    function renderFilters() {
        const s = state();
        const toggle = (key, label) => h("button", { class: "btn", type: "button", "aria-pressed": String(filters[key]), onclick: () => { filters[key] = !filters[key]; load(); } }, label);
        const tagSelect = s.tags?.length ? h("select", { class: "input", "aria-label": t("threads.tagFilter"), onchange: event => { filters.tag = event.target.value; load(); } },
            h("option", { value: "" }, t("threads.allTags")), s.tags.map(tag => h("option", { value: tag, selected: filters.tag === tag || null }, tag))) : null;
        replaceContent(filterBar, toggle("following", t("threads.followingOnly")), toggle("archived", t("threads.showArchived")), tagSelect);
    }

    function render() {
        const s = state();
        renderFilters();
        list.replaceChildren(...s.threads.map(thread => h("li", {}, h("a", { class: "row tool-row thread-row", href: `#/thread/${channelID}/${thread.id}` },
            avatar(personName(session, thread.author), "sm"),
            h("span", { class: "row-main" },
                h("span", { class: "row-title" }, thread.title),
                threadChips(thread),
                h("span", { class: "thread-meta" }, t("threads.meta", { author: personName(session, thread.author), count: Math.max(0, (thread.message_count || 0) - 1), time: when(thread.updated_at) })))))));
        if (!s.threads.length && !loading) list.replaceChildren(h("li", { class: "hint" }, t(filters.following ? "threads.noneFollowing" : "threads.none")));
        more.hidden = !s.hasMore || !s.threads.length;
        manage.hidden = !s.canManage;
        // A forum channel's chat link would lead straight back here.
        head.querySelector("a.icon-btn")?.setAttribute("href", s.forum ? "#/servers" : `#/chat/${channelID}`);
        forumHint.hidden = !s.forum;
        newButton.textContent = t(s.forum ? "threads.newPost" : "threads.new");
    }

    async function load(append = false) {
        loading = true;
        status.textContent = t("threads.loading");
        const last = state().threads.at(-1);
        try {
            await discussions.list(channelID, {
                following: filters.following, archived: filters.archived, tags: filters.tag ? [filters.tag] : [],
                beforeID: append ? last?.id : 0, beforePinned: append ? !!last?.pinned : false,
            });
            status.textContent = "";
        } catch (error) {
            status.textContent = t("common.error", { error: error.message });
        }
        loading = false;
        render();
    }

    more.onclick = () => load(true);
    const newButton = h("button", { class: "btn btn-primary", type: "button", onclick: () => openNewThread(session, channelID) }, t("threads.new"));
    const manage = h("button", { class: "btn", type: "button", hidden: true, onclick: () => openForumSettings(session, channelID) }, t("threads.settings"));
    const chatButton = h("a", { class: "btn", href: `#/chat/${channelID}`, onclick: () => chooseChat(channelID) }, t("threads.openChat"));
    const forumHint = h("p", { class: "hint", hidden: true }, t("threads.forumHint"));
    body.append(h("div", { class: "tool-actions" }, newButton, chatButton, manage), forumHint, filterBar, status, list, more);

    const head = header(t("threads.title"), { back: `#/chat/${channelID}`, sub: name });
    let timer = 0;
    const onList = event => { if (event.detail.channelID === channelID) render(); };
    const onChanged = event => {
        if (event.detail.channelID !== channelID) return;
        clearTimeout(timer);
        timer = setTimeout(() => load(), 300);
    };
    discussions.addEventListener("list", onList);
    discussions.addEventListener("changed", onChanged);
    root.replaceChildren(h("section", { class: "screen chat-tools" }, head, body));
    render();
    load().then(() => {
        if (mode === "new") {
            const rootMessageID = Number(rootPart) || 0;
            const source = rootMessageID ? session.messageByID(channelID, rootMessageID) : null;
            history.replaceState(null, "", `#/threads/${channelID}`);
            openNewThread(session, channelID, { rootMessageID, text: source?.text || "" });
        }
    });
    return () => {
        clearTimeout(timer);
        discussions.removeEventListener("list", onList);
        discussions.removeEventListener("changed", onChanged);
    };
}

/** #/thread/<channel>/<thread> */
export function renderThread(root, arg) {
    const session = app.session;
    const [channelID, threadID] = String(arg).split("/").map(Number);
    const discussions = discussionsFor(session);
    const log = h("div", { class: "chat-log", role: "log", "aria-live": "polite", "aria-label": t("threads.thread") });
    const toolbar = h("div", { class: "thread-toolbar" });
    const chips = h("div", { style: "flex-shrink:0;padding:0 12px" });
    const footer = h("div");
    const title = h("span", {}, t("threads.thread"));
    const input = h("textarea", { rows: "1", "aria-label": t("threads.reply"), placeholder: t("threads.reply"), enterkeyhint: "send", maxlength: "12000" });
    const sendButton = h("button", { class: "send", type: "submit", "aria-label": t("chat.send") }, icon("send"));
    const older = h("button", { class: "btn", style: "align-self:center;margin:8px 0" }, t("chat.loadOlder"));
    let thread = null;

    const canEdit = () => thread && (thread.author === session.uniqueID || discussions.channels.get(channelID)?.canModerate);
    const canModerate = () => !!discussions.channels.get(channelID)?.canModerate;

    const line = lineCache(message => h("div", { class: `msg${message.own ? " own" : ""}`, dataset: { id: String(message.id ?? "") } },
        timeNode(message.sentAt),
        avatar(message.from, "sm"),
        h("div", { class: "msg-body" },
            h("span", { class: "msg-from" }, message.from),
            message.deleted ? null : encryptionBadge("channel"),
            message.deleted ? h("span", { class: "msg-deleted" }, t("chat.deleted"))
                : message.text === null || message.text === undefined ? h("span", { class: "msg-locked" }, t("chat.locked"))
                    : richBody(message.text, { session, conn: session.conn, channelID }),
            !message.deleted && canModerate() ? h("button", { class: "link-btn", type: "button", onclick: () => removeMessage(message) }, t("threads.deleteMessage")) : null)));

    function renderLog() {
        const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
        const messages = discussions.messages.get(threadID) || [];
        log.replaceChildren(discussions.hasMore.get(threadID) ? older : h("span"), ...withDays(messages, m => m.sentAt, line));
        if (atBottom || messages.at(-1)?.own) log.scrollTop = log.scrollHeight;
    }

    const toolbarButton = run => h("button", { class: "btn", type: "button", hidden: true, onclick: () => thread && run() });
    const toolbarButtons = {
        join: toolbarButton(() => act(thread.joined ? "leave" : "join")()),
        follow: toolbarButton(() => act("subscribe", !thread.subscribed)()),
        resolve: toolbarButton(() => act("resolve", !thread.resolved)()),
        pin: toolbarButton(() => act("pin", !thread.pinned)()),
    };

    // While a change is on its way the toolbar waits for it.
    const busy = on => Object.values(toolbarButtons).forEach(button => { button.disabled = on; });
    const act = (action, value) => async () => {
        busy(true);
        try {
            await withRateRetry(() => discussions.setFlag(channelID, threadID, action, value));
            thread = await discussions.get(channelID, threadID) || thread;
            renderMeta();
        } catch (error) {
            toast(t("common.error", { error: error.message }), "warn");
        } finally {
            busy(false);
        }
    };

    function renderMeta() {
        if (!thread) return;
        title.textContent = thread.title;
        chips.replaceChildren(threadChips({ ...thread, unread: false }));
        // The buttons stay in place (only their state changes), so a tap
        // never lands on one a refresh just replaced.
        const set = (button, label, on, shown = true) => {
            button.textContent = label;
            button.setAttribute("aria-pressed", String(on));
            button.hidden = !shown;
        };
        set(toolbarButtons.join, t(thread.joined ? "threads.joined" : "threads.join"), thread.joined);
        set(toolbarButtons.follow, t(thread.subscribed ? "threads.following" : "threads.follow"), thread.subscribed, thread.joined);
        set(toolbarButtons.resolve, t(thread.resolved ? "threads.resolved" : "threads.resolve"), thread.resolved, !!canEdit());
        set(toolbarButtons.pin, t(thread.pinned ? "threads.pinned" : "threads.pin"), thread.pinned, canModerate());
        if (!toolbar.childElementCount) toolbar.append(...Object.values(toolbarButtons));
        if (thread.archived) {
            footer.replaceChildren(h("p", { class: "thread-hint" }, t("threads.archivedHint"),
                canEdit() ? h("button", { class: "btn", type: "button", onclick: act("archive", false) }, t("threads.reopen")) : null));
        } else if (!thread.joined) {
            footer.replaceChildren(h("p", { class: "thread-hint" }, t("threads.joinHint"), h("button", { class: "btn btn-primary", type: "button", onclick: act("join") }, t("threads.join"))));
        } else {
            footer.replaceChildren(form);
        }
    }

    async function removeMessage(message) {
        const sure = await sheet(t("threads.deleteMessage"), t("threads.deleteMessageHelp"), [{ label: t("dm.delete"), value: true, danger: true }, { label: t("link.cancel"), value: false }]);
        if (sure) await discussions.remove(channelID, threadID, message.id).catch(error => toast(t("common.error", { error: error.message }), "warn"));
    }

    function editThread() {
        const state = discussions.channels.get(channelID) || {};
        const titleInput = h("input", { class: "input", id: "thread-edit-title", maxlength: "120", required: true, value: thread.title });
        const tags = tagPicker(state.tags || [], thread.tags || []);
        const submit = h("button", { class: "btn btn-primary", type: "submit" }, t("threads.save"));
        const error = h("p", { class: "error", role: "alert" });
        const { form: editForm, close } = formSheet(t("threads.edit"), close => [
            h("div", { class: "field" }, h("label", { for: "thread-edit-title" }, t("threads.titleField")), titleInput),
            tags.node, error,
            h("div", { class: "sheet-actions" }, submit, h("button", { class: "btn", type: "button", onclick: close }, t("link.cancel"))),
        ]);
        editForm.onsubmit = async event => {
            event.preventDefault();
            submit.disabled = true;
            try {
                await discussions.edit(channelID, threadID, { title: titleInput.value.trim(), tags: tags.value() });
                thread = await discussions.get(channelID, threadID) || thread;
                renderMeta();
                close();
            } catch (failure) {
                error.textContent = t("common.error", { error: failure.message });
                submit.disabled = false;
            }
        };
    }

    async function deleteThread() {
        const sure = await sheet(t("threads.delete"), t("threads.deleteHelp"), [{ label: t("threads.delete"), value: true, danger: true }, { label: t("link.cancel"), value: false }]);
        if (!sure) return;
        try {
            await discussions.remove(channelID, threadID);
            location.hash = `#/threads/${channelID}`;
        } catch (error) {
            toast(t("common.error", { error: error.message }), "warn");
        }
    }

    const menu = menuButton(() => [
        canEdit() ? { label: t("threads.edit"), run: editThread } : null,
        canEdit() ? { label: t(thread.archived ? "threads.reopen" : "threads.archive"), run: act("archive", !thread.archived) } : null,
        canModerate() && thread ? { label: t(thread.pinned ? "threads.unpin" : "threads.pin"), run: act("pin", !thread.pinned) } : null,
        app.saved && thread ? { label: t("saved.saveFirst"), run: () => {
            const last = (discussions.messages.get(threadID) || []).find(m => m.id && !m.deleted);
            if (last) app.saved.save({ kind: "thread", channelID, threadID, messageID: last.id }).then(() => toast(t("saved.added")));
        } } : null,
        canModerate() && thread ? { label: t("threads.delete"), run: deleteThread, danger: true } : null,
        { label: t("threads.backToList"), run: () => { location.hash = `#/threads/${channelID}`; } },
    ].filter(Boolean));

    const form = h("form", { class: "composer", onsubmit: async event => {
        event.preventDefault();
        const text = input.value.trim();
        if (!text) return;
        input.value = "";
        input.style.height = "";
        try {
            await withRateRetry(() => discussions.send(channelID, threadID, text));
        } catch (error) {
            input.value = text;
            toast(t("chat.sendFailed", { error: error.message }), "warn");
        }
    } }, input, sendButton);
    input.addEventListener("keydown", event => {
        if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); form.requestSubmit(); }
    });
    input.addEventListener("input", () => {
        input.style.height = "";
        input.style.height = `${Math.min(input.scrollHeight, 140)}px`;
    });
    older.onclick = async () => {
        older.disabled = true;
        try { await discussions.history(channelID, threadID); } catch (error) { toast(t("common.error", { error: error.message }), "warn"); }
        older.disabled = false;
        renderLog();
    };

    async function refresh() {
        try {
            thread = await discussions.get(channelID, threadID);
            if (!thread) {
                toast(t("threads.gone"), "warn");
                location.hash = `#/threads/${channelID}`;
                return;
            }
            renderMeta();
            renderLog();
        } catch (error) {
            footer.replaceChildren(h("p", { class: "thread-hint error" }, t("common.error", { error: error.message })));
        }
    }

    let timer = 0;
    const onMessages = event => { if (event.detail.threadID === threadID) renderLog(); };
    const onChanged = event => {
        if (event.detail.threadID !== threadID) return;
        if (event.detail.deleted) {
            toast(t("threads.gone"), "warn");
            location.hash = `#/threads/${channelID}`;
            return;
        }
        clearTimeout(timer);
        timer = setTimeout(refresh, 250);
    };
    discussions.addEventListener("messages", onMessages);
    discussions.addEventListener("changed", onChanged);
    const rerender = () => { line.clear(); renderLog(); };
    const stops = [watchCustomEmoji(session, rerender), watchDisplaySettings(rerender), relativeTicker(log)];
    root.replaceChildren(h("section", { class: "screen chat" },
        header(title, { back: `#/threads/${channelID}`, sub: scopeName(session, channelID), action: headerActions(menu) }),
        chips, toolbar, log, footer));
    renderLog();
    refresh();
    return () => {
        clearTimeout(timer);
        discussions.removeEventListener("messages", onMessages);
        discussions.removeEventListener("changed", onChanged);
        stops.forEach(stop => stop());
    };
}
