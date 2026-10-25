// chat-tools.js — the small hooks the conversation screens use for threads:
// one Discussions instance per session, the header's Threads button with
// its unread badge, the forum-mode redirect and the noxa:open-thread event
// (create a thread from a message, or open the one it already has).
import { Discussions } from "../core/discussions.js";
import { t } from "../i18n.js";
import { h, toast } from "./dom.js";
import { icon } from "./icons.js";
import "./chat-tools-strings.js";
import "./chat-tools.css";

const instances = new WeakMap();

/** The session's Discussions (threads and forum posts). */
export function discussionsFor(session) {
    if (!instances.has(session)) instances.set(session, new Discussions(session));
    return instances.get(session);
}

// Forum channels open their post list; once the user asks for the chat of
// a channel it stays the chat for this app run.
const chatChosen = new Set();

export function chooseChat(channelID) {
    chatChosen.add(channelID);
}

/**
 * The header's Threads button for a channel. Lists the channel's threads
 * once to learn its forum mode and unread count; a forum channel switches
 * to its post list unless the user chose the chat. Returns {button, stop}.
 */
export function threadsButton(session, channelID) {
    const discussions = discussionsFor(session);
    const badge = h("span", { class: "badge", hidden: true });
    const button = h("a", { class: "icon-btn thread-badge", href: `#/threads/${channelID}`, "aria-label": t("threads.title") }, icon("threads"), badge);
    const show = () => {
        const state = discussions.channels.get(channelID);
        const unread = (state?.threads || []).filter(thread => thread.unread).length;
        badge.hidden = !unread;
        badge.textContent = String(unread);
        button.setAttribute("aria-label", unread ? t("threads.unreadLabel", { count: unread }) : t("threads.title"));
    };
    let timer = 0;
    const refresh = () => discussions.list(channelID).then(state => {
        show();
        if (state.forum && !chatChosen.has(channelID) && location.hash.startsWith(`#/chat/${channelID}`)) location.replace(`#/threads/${channelID}`);
    }).catch(() => {});
    const onList = event => { if (event.detail.channelID === channelID) show(); };
    const onChanged = event => {
        if (event.detail.channelID !== channelID) return;
        clearTimeout(timer);
        timer = setTimeout(refresh, 400);
    };
    discussions.addEventListener("list", onList);
    discussions.addEventListener("changed", onChanged);
    show();
    refresh();
    return {
        button,
        stop() {
            clearTimeout(timer);
            discussions.removeEventListener("list", onList);
            discussions.removeEventListener("changed", onChanged);
        },
    };
}

/** Finds the thread started from a channel message, looking through a few list pages. */
async function threadFor(discussions, channelID, messageID) {
    for (const archived of [false, true]) {
        let state = await discussions.list(channelID, { archived });
        for (let page = 0; page < 5; page++) {
            const found = state.threads.find(thread => thread.root_message_id === messageID);
            if (found) return found;
            const last = state.threads.at(-1);
            if (!state.hasMore || !last) break;
            state = await discussions.list(channelID, { archived, beforeID: last.id, beforePinned: !!last.pinned });
        }
    }
    return null;
}

/**
 * Handles noxa:open-thread {scope, messageID} (the message menu's "Open
 * thread"): opens the message's thread, or the new-thread form with it as
 * the root. Returns stop().
 */
export function watchOpenThread(session) {
    const onOpen = async event => {
        const { scope, messageID } = event.detail || {};
        if (!(scope > 0) || !messageID) {
            toast(t("threads.channelOnly"), "warn");
            return;
        }
        try {
            const thread = await threadFor(discussionsFor(session), scope, messageID);
            location.hash = thread ? `#/thread/${scope}/${thread.id}` : `#/threads/${scope}/new/${messageID}`;
        } catch (error) {
            toast(t("common.error", { error: error.message }), "warn");
        }
    };
    window.addEventListener("noxa:open-thread", onOpen);
    return () => window.removeEventListener("noxa:open-thread", onOpen);
}
