import test from "node:test";
import assert from "node:assert/strict";

import { ProtocolError } from "../src/core/connection.js";
import { generateIdentity } from "../src/core/crypto.js";
import { Msg } from "../src/core/frame.js";
import { Groups, seenMarkers } from "../src/core/groups.js";

const GROUP = { id: "g1", name: "Crew", owner: "self", revision: 3, epoch: 2, members: [{ unique_id: "self" }, { unique_id: "ivy" }, { unique_id: "kai" }] };

function setup({ capabilities = ["conversation_activity_v1"], request } = {}) {
    const identity = generateIdentity();
    const requests = [];
    const sent = [];
    const conn = {
        open: true,
        send: (type, payload) => sent.push([type, payload]),
        request: async (_type, payload) => {
            requests.push(payload);
            if (request) return request(payload);
            if (payload.action === "get") return { action: "get", conversations: [structuredClone(GROUP)] };
            if (payload.action === "send") return { action: "send", message_id: 42 };
            return { action: payload.action };
        },
    };
    const session = Object.assign(new EventTarget(), { conn, uniqueID: "self", deviceID: "phone", identity, capabilities: new Set(capabilities) });
    const device = { deviceID: "desk", first: true, x25519: identity.xPublic, x25519B64: "k" };
    const dm = { devicesOf: async () => ({ trust: "ok", devices: [device, { deviceID: "phone", first: false, x25519: identity.xPublic, x25519B64: "p" }] }) };
    const groups = new Groups(session, dm);
    const event = (type, data) => session.dispatchEvent(new CustomEvent("server-event", { detail: { type, data } }));
    return { groups, session, requests, sent, event };
}

test("Seen by sits on the newest message a member's shared position covers", () => {
    const messages = [{ id: 1, from_unique_id: "self" }, { id: 2, from_unique_id: "ivy" }, { id: 3, from_unique_id: "self" }, { id: "local:x", local: true, from_unique_id: "self" }];
    const members = [
        { unique_id: "self", seen_message_id: 3 },
        { unique_id: "ivy", seen_message_id: 3 },
        { unique_id: "kai", seen_message_id: 2 },
        { unique_id: "lou", seen_message_id: 1 },
        { unique_id: "mia", pending: true, seen_message_id: 3 },
        { unique_id: "noa" },
    ];
    const markers = seenMarkers(messages, members, "self");
    assert.deepEqual([...markers.entries()], [[3, ["ivy"]], [2, ["kai"]], [1, ["lou"]]]);
    // A member is never listed under their own newest message.
    assert.deepEqual([...seenMarkers([{ id: 5, from_unique_id: "ivy" }], [{ unique_id: "ivy", seen_message_id: 5 }], "self")], []);
});

test("a sent message shows locally while pending and is replaced by the accepted one", async () => {
    const { groups } = setup();
    const states = [];
    groups.addEventListener("messages", () => states.push(groups.messages.get("g1").map(m => (m.local ? `local:${m.pending ? "pending" : m.queued ? "queued" : "failed"}` : `server:${m.id}`))));
    const id = await groups.send("g1", "hello", { reference: "ref-1", sentAt: 100 });
    assert.equal(id, 42);
    assert.deepEqual(states[0], ["local:pending"]);
    assert.deepEqual(states.at(-1), ["server:42"]);
    assert.equal(groups.messages.get("g1")[0].reference, "ref-1");
});

test("a lost connection queues the group message (retryable), a refusal fails it", async () => {
    let mode = "lost";
    const { groups } = setup({ request: payload => {
        if (payload.action === "get") return { action: "get", conversations: [structuredClone(GROUP)] };
        if (mode === "lost") throw new Error("connection closed");
        throw new ProtocolError({ message: "conversation access denied" });
    } });
    await assert.rejects(groups.send("g1", "offline", { reference: "ref-2", sentAt: 1 }), error => error.retryable === true);
    assert.deepEqual(groups.messages.get("g1").map(m => [m.reference, m.queued, m.failed]), [["ref-2", true, false]]);
    mode = "refused";
    await assert.rejects(groups.send("g1", "refused", { reference: "ref-3", sentAt: 2 }), error => error.retryable === false);
    assert.deepEqual(groups.messages.get("g1").map(m => [m.reference, m.queued, m.failed]), [["ref-2", true, false], ["ref-3", false, true]]);
    // The outbox shows an expired entry as failed, and discarding removes it.
    groups.showQueued("g1", { reference: "ref-2", text: "offline", sentAt: 1, expired: true });
    assert.equal(groups.messages.get("g1")[0].failed, true);
    groups.discardLocal("g1", "ref-2");
    assert.deepEqual(groups.messages.get("g1").map(m => m.reference), ["ref-3"]);
});

test("mark_read shares the read position only while activity is shared, once per message", async () => {
    const { groups, requests } = setup();
    groups.messages.set("g1", [{ id: 7, from_unique_id: "ivy" }, { id: "local:z", local: true, reference: "z", from_unique_id: "self" }]);
    groups.markRead("g1");
    groups.markRead("g1");
    assert.deepEqual(requests.filter(r => r.action === "mark_read"), [{ action: "mark_read", id: "g1", read_message_id: 7 }]);
    groups.shareActivity = () => true;
    groups.messages.get("g1").push({ id: 8, from_unique_id: "ivy" });
    groups.markRead("g1");
    assert.deepEqual(requests.filter(r => r.action === "mark_read").at(-1), { action: "mark_read", id: "g1", read_message_id: 8, share_read: true });
});

test("typing is sent at most every 3 s and only with the server capability and shared activity", () => {
    let run = setup();
    assert.equal(run.groups.sendTyping("g1", 1000), false, "activity private by default");
    run.groups.shareActivity = () => true;
    assert.equal(run.groups.sendTyping("g1", 1000), true);
    assert.equal(run.groups.sendTyping("g1", 3000), false);
    assert.equal(run.groups.sendTyping("g1", 4100), true);
    assert.deepEqual(run.sent, [[Msg.Typing, { conversation_id: "g1" }], [Msg.Typing, { conversation_id: "g1" }]]);
    run = setup({ capabilities: [] });
    run.groups.shareActivity = () => true;
    assert.equal(run.groups.sendTyping("g1", 1000), false);
});

test("typing and read relays update the group", async () => {
    const { groups, event } = setup();
    await groups.get("g1");
    const seen = [];
    groups.addEventListener("typing", e => seen.push(["typing", e.detail.id]));
    groups.addEventListener("seen", e => seen.push(["seen", e.detail.id]));
    event("conversation_typing", { conversation_id: "g1", unique_id: "ivy", nickname: "Ivy" });
    event("conversation_typing", { conversation_id: "g1", unique_id: "self", nickname: "Me" });
    assert.deepEqual(groups.typingNames("g1"), ["Ivy"]);
    assert.deepEqual(groups.typingNames("g1", Date.now() + 10000), []);
    event("conversation_read", { conversation_id: "g1", unique_id: "kai", message_id: 9 });
    event("conversation_read", { conversation_id: "g1", unique_id: "kai", message_id: 4 });
    assert.equal(groups.list.get("g1").members.find(m => m.unique_id === "kai").seen_message_id, 9);
    assert.deepEqual(seen, [["typing", "g1"], ["seen", "g1"]]);
    groups.close();
});

test("owner changes use the current revision; leaving forgets the group", async () => {
    const { groups, requests } = setup();
    await groups.transfer("g1", "ivy");
    await groups.remove("g1", "kai");
    await groups.rename("g1", " New name ");
    await groups.deleteMessage("g1", 5);
    const changes = requests.filter(r => r.action !== "get");
    assert.deepEqual(changes, [
        { id: "g1", revision: 3, action: "transfer", target: "ivy" },
        { id: "g1", revision: 3, action: "remove", target: "kai" },
        { id: "g1", revision: 3, action: "rename", name: "New name" },
        { id: "g1", revision: 3, action: "delete_message", message_id: 5 },
    ]);
    groups.messages.set("g1", [{ id: 1 }]);
    await groups.leave("g1");
    assert.equal(groups.list.has("g1"), false);
    assert.equal(groups.messages.has("g1"), false);
});
