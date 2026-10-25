import test from "node:test";
import assert from "node:assert/strict";

import { Calls } from "../src/core/calls.js";

// Drives Calls through "private_call" events; each event fetches the call.
function setup() {
    let current = null;
    const session = Object.assign(new EventTarget(), {
        uniqueID: "self",
        conn: { request: async () => ({ call: structuredClone(current) }) },
    });
    const calls = new Calls(session, {});
    const records = [];
    calls.addEventListener("record", event => records.push(event.detail));
    const apply = async call => {
        current = call;
        session.dispatchEvent(new CustomEvent("server-event", { detail: { type: "private_call", data: { id: call.id } } }));
        await new Promise(resolve => setTimeout(resolve, 0));
    };
    return { calls, records, apply };
}

const call = (id, caller, states, extra = {}) => ({ id, caller, created_at: 1700000000, revision: extra.revision || 1, ended_at: extra.ended_at || 0,
    participants: Object.entries(states).map(([unique_id, state]) => ({ unique_id, state })) });

test("an incoming call that stops ringing is recorded once as missed", async () => {
    const { records, apply } = setup();
    await apply(call("c1", "ivy", { ivy: "accepted", self: "ringing" }));
    assert.equal(records.length, 0);
    await apply(call("c1", "ivy", { ivy: "left", self: "missed" }, { revision: 2, ended_at: 1700000030 }));
    await apply(call("c1", "ivy", { ivy: "left", self: "missed" }, { revision: 3, ended_at: 1700000030 }));
    assert.equal(records.length, 1);
    assert.deepEqual({ ...records[0], endedAt: 0 }, { id: "c1", peer: "ivy", direction: "in", outcome: "missed", startedAt: 1700000000000, endedAt: 0, duration: 0 });
});

test("an outgoing call the contact declines is recorded as declined", async () => {
    const { records, apply } = setup();
    await apply(call("c2", "self", { self: "accepted", kai: "ringing" }));
    await apply(call("c2", "self", { self: "accepted", kai: "declined" }, { revision: 2, ended_at: 1700000010 }));
    assert.deepEqual(records.map(r => [r.peer, r.direction, r.outcome]), [["kai", "out", "declined"]]);
});

test("a call answered on another device of the account is left to that device", async () => {
    const { records, apply } = setup();
    await apply(call("c3", "lou", { lou: "accepted", self: "ringing" }));
    await apply(call("c3", "lou", { lou: "accepted", self: "elsewhere" }, { revision: 2 }));
    assert.equal(records.length, 0);
});

test("group calls are not recorded per contact", async () => {
    const { records, apply } = setup();
    const group = { ...call("c4", "ivy", { ivy: "accepted", self: "ringing", kai: "ringing" }), conversation_id: "g1" };
    await apply(group);
    await apply({ ...group, revision: 2, ended_at: 1, participants: group.participants.map(p => ({ ...p, state: p.unique_id === "ivy" ? "left" : "missed" })) });
    assert.equal(records.length, 0);
});
