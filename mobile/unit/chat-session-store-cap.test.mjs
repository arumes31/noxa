// Audit: the channel message store must stay bounded like the DM path
// (direct.js caps at MAX_MESSAGES=5000), trimming only the oldest when a new
// message is appended at the end — never when an older history page is merged
// in, so scrollback / jump-to-reply / search keep working.
import test from "node:test";
import assert from "node:assert/strict";
import { ChatSession } from "../src/core/chat-session.js";
import { Msg } from "../src/core/frame.js";

const CAP = 5000; // mirrors MAX_MESSAGES in chat-session.js and direct.js

class FakeConnection extends EventTarget {
    open = true;
    on(type, fn) { const f = e => fn(e.detail); this.addEventListener(String(type), f); return () => this.removeEventListener(String(type), f); }
    deliver(type, detail) { this.dispatchEvent(new CustomEvent(String(type), { detail })); }
    close() { this.open = false; }
}

function channelSession() {
    const conn = new FakeConnection();
    // own messages (from_unique_id === unique_id) skip the keyword scan.
    const session = new ChatSession(conn, {}, { client_id: "c", unique_id: "u", nickname: "me" });
    return { conn, session };
}

test("the channel store is capped at 5000, keeping the newest, still sorted oldest to newest", () => {
    const { conn, session } = channelSession();
    const total = CAP + 200;
    for (let i = 0; i < total; i++) {
        conn.deliver(Msg.Event, { type: "chat", data: { channel_id: 7, id: 100000 + i, from_unique_id: "u", from: "me" } });
    }
    const list = session.messages.get(7);
    assert.equal(list.length, CAP, "bounded to the cap");
    // The newest survive; the oldest were trimmed from the bottom.
    assert.equal(list[list.length - 1].id, 100000 + total - 1, "newest kept");
    assert.equal(list[0].id, 100000 + total - CAP, "oldest beyond the cap dropped");
    for (let i = 1; i < list.length; i++) {
        assert.ok(list[i - 1].sentAt <= list[i].sentAt, "sorted oldest to newest");
    }
});

test("loading an older history page is not immediately trimmed away", async () => {
    const { conn, session } = channelSession();
    for (let i = 0; i < CAP + 200; i++) {
        conn.deliver(Msg.Event, { type: "chat", data: { channel_id: 7, id: 100000 + i, from_unique_id: "u", from: "me" } });
    }
    assert.equal(session.messages.get(7).length, CAP);
    // An older page loaded on scroll: entries older than everything held.
    const older = Array.from({ length: 10 }, (_, i) => ({ id: i + 1, from_unique_id: "u", from_nickname: "me", sent_at: 1 + i }));
    conn.request = async () => ({ keys: [], messages: older });
    await session.loadHistory(7);
    const list = session.messages.get(7);
    assert.equal(list.length, CAP + older.length, "older page added, nothing trimmed from the top");
    assert.equal(list[0].id, 1, "the oldest loaded entry is present, not trimmed away");
    for (let i = 1; i < list.length; i++) {
        assert.ok(list[i - 1].sentAt <= list[i].sentAt, "still sorted oldest to newest");
    }
});
