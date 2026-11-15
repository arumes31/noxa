// Audit: the channel message store must stay bounded (like direct.js, cap
// MAX_MESSAGES=5000) for a busy scope the user never scrolled back in, while
// never dropping a loaded-history page the user is viewing or a pending
// (unacked) send that would re-insert as a partial row on ack.
import test from "node:test";
import assert from "node:assert/strict";
import { ChatSession } from "../src/core/chat-session.js";
import { Msg } from "../src/core/frame.js";

const CAP = 5000; // mirrors MAX_MESSAGES in chat-session.js

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

const liveBurst = (conn, scope, base, n) => {
    for (let i = 0; i < n; i++) {
        conn.deliver(Msg.Event, { type: "chat", data: { channel_id: scope, id: base + i, from_unique_id: "u", from: "me" } });
    }
};

test("the channel store is capped at 5000, keeping the newest, still sorted oldest to newest", () => {
    const { conn, session } = channelSession();
    const total = CAP + 200;
    liveBurst(conn, 7, 100000, total);
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
    liveBurst(conn, 7, 100000, CAP + 200);
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

test("after loading older history, later live messages never evict the loaded page", async () => {
    const { conn, session } = channelSession();
    liveBurst(conn, 7, 100000, CAP + 200);
    const older = Array.from({ length: 10 }, (_, i) => ({ id: i + 1, from_unique_id: "u", from_nickname: "me", sent_at: 1 + i }));
    conn.request = async () => ({ keys: [], messages: older });
    await session.loadHistory(7); // the scope is now expanded
    const before = session.messages.get(7).length;
    // A burst of new live messages must not trim away the page the user is reading.
    liveBurst(conn, 7, 200000, 300);
    const list = session.messages.get(7);
    assert.equal(list[0].id, 1, "the oldest loaded entry survives later live traffic");
    assert.equal(list.length, before + 300, "an expanded scope is not re-capped this session");
});

test("a pending (unacked) send in the oldest region is never evicted", () => {
    const { conn, session } = channelSession();
    // A queued/unacked own send sits at the very oldest position (small sentAt);
    // evicting it would re-insert a partial row when its ack later arrives.
    session.messages.set(7, [{ clientMsgID: "p1", sentAt: 0, pending: true, text: "queued" }]);
    liveBurst(conn, 7, 100000, CAP + 50);
    const list = session.messages.get(7);
    assert.ok(list.some(m => m.clientMsgID === "p1" && m.pending), "the pending send is kept, not trimmed");
    assert.equal(list[0].clientMsgID, "p1", "it stays at the oldest position");
});
