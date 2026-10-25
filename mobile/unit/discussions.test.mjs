import assert from "node:assert/strict";
import { test } from "node:test";

import { Discussions } from "../src/core/discussions.js";
import { Msg } from "../src/core/frame.js";

/** A session with one scope key and a scripted connection. */
function fixture() {
    const key = new Uint8Array(32).fill(7);
    const sent = [];
    const listeners = new Map();
    const replies = [];
    const conn = {
        request(type, request, replyType) {
            sent.push({ type, request, replyType });
            const reply = replies.shift();
            if (!reply) return Promise.reject(new Error("no scripted reply"));
            return Promise.resolve(typeof reply === "function" ? reply(request) : reply);
        },
    };
    const session = new EventTarget();
    Object.assign(session, {
        conn, uniqueID: "me", nickname: "Me",
        current: new Map([[4, 2]]), keys: new Map([[4, new Map([[2, key]])]]),
        installed: [],
        installKey(k) { session.installed.push(k); },
        decrypt(scope, keyID, body) { return keyID === 2 ? `plain:${body}` : null; },
        emit(type, data) { session.dispatchEvent(new CustomEvent("server-event", { detail: { type, data } })); },
    });
    return { session, sent, replies, discussions: new Discussions(session) };
}

test("list, history and send seal with the channel key and track state", async () => {
    const f = fixture();
    f.replies.push({ action: "list", channel_id: 4, forum: true, tags: ["help"], auto_archive_hours: 72, can_manage: true, has_more: true,
        threads: [{ id: 9, channel_id: 4, title: "First", tags: [], author: "x", message_count: 2 }] });
    const state = await f.discussions.list(4);
    assert.equal(state.forum, true);
    assert.deepEqual(state.tags, ["help"]);
    assert.equal(state.threads[0].title, "First");
    assert.equal(state.hasMore, true);

    f.replies.push({ action: "history", channel_id: 4, thread_id: 9, has_more: false, keys: [{ channel_id: 4, key_id: 1, sealed_key: "s" }],
        messages: [{ id: 12, from_unique_id: "x", from_nickname: "X", key_id: 2, body_enc: "b1", sent_at: 20 }, { id: 11, from_unique_id: "me", from_nickname: "Me", key_id: 2, body_enc: "b0", sent_at: 10 }] });
    assert.equal(await f.discussions.history(4, 9), false);
    const messages = f.discussions.messages.get(9);
    assert.deepEqual(messages.map(m => [m.id, m.text, m.own]), [[11, "plain:b0", true], [12, "plain:b1", false]]);
    assert.equal(f.session.installed.length, 1);

    f.replies.push(request => ({ action: "send", channel_id: 4, thread_id: 9, message_id: 13, request_id: request.request_id }));
    await f.discussions.send(4, 9, "hello");
    const request = f.sent.at(-1).request;
    assert.equal(request.action, "send");
    assert.equal(request.key_id, 2);
    assert.ok(request.body_enc && request.body_enc !== "hello", "the body is sealed");
    assert.equal(request.text, undefined);
    assert.ok(request.request_id);
    assert.equal(f.discussions.messages.get(9).at(-1).id, 13);
});

test("flags map to the server's actions and events update the local view", async () => {
    const f = fixture();
    f.replies.push({ action: "list", channel_id: 4, threads: [{ id: 9, title: "T" }] });
    await f.discussions.list(4);
    f.replies.push({ action: "reopen", channel_id: 4, thread_id: 9, threads: [{ id: 9, title: "T", archived: false }] });
    await f.discussions.setFlag(4, 9, "archive", false);
    assert.equal(f.sent.at(-1).request.action, "reopen");
    f.replies.push({ action: "pin", channel_id: 4, thread_id: 9, threads: [{ id: 9, title: "T", pinned: true }] });
    await f.discussions.setFlag(4, 9, "pin", true);
    assert.equal(f.sent.at(-1).request.pinned, true);
    assert.equal(f.discussions.channels.get(4).threads[0].pinned, true);

    const changes = [];
    f.discussions.addEventListener("changed", event => changes.push(event.detail));
    f.session.emit("discussion_changed", { channel_id: 4, thread_id: 9, new_message: true, message_id: 20, author: "x" });
    assert.deepEqual(changes[0], { channelID: 4, threadID: 9, newMessage: true, messageID: 20, author: "x", deleted: false });
    f.session.emit("discussion_changed", { channel_id: 4, thread_id: 9, deleted: true });
    assert.equal(f.discussions.channels.get(4).threads.length, 0);
});
