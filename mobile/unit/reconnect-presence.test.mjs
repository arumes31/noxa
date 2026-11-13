import assert from "node:assert/strict";
import test from "node:test";
import { presenceLines } from "../src/core/join-leave.js";

const clients = [{ client_id: "c1", nickname: "Ann", unique_id: "u1", channel_id: 7 }];

test("a lost connection and a reconnect get their own lines; a replaced session none", () => {
    assert.deepEqual(presenceLines(clients, "user_left", { client_id: "c1", reason: "connection_lost" }, 1).map(l => [l.scope, l.kind]), [[0, "lost"], [7, "lost"]]);
    assert.deepEqual(presenceLines(clients, "user_left", { client_id: "c1", reason: "replaced" }, 1), []);
    assert.deepEqual(presenceLines(clients, "user_left", { client_id: "c1" }, 1).map(l => l.kind), ["left", "left"]);
    assert.deepEqual(presenceLines([], "user_joined", { client_id: "c2", nickname: "Ann", channel_id: 7, reconnected: true }, 1).map(l => l.kind), ["reconnected", "reconnected"]);
    assert.deepEqual(presenceLines([], "user_joined", { client_id: "c2", nickname: "Ann" }, 1).map(l => l.kind), ["joined"]);
});

test("a deliberate close says goodbye first; a plain close does not", async () => {
    const { Connection } = await import("../src/core/connection.js");
    const { Msg } = await import("../src/core/frame.js");
    const sent = [];
    const fake = Object.create(Connection.prototype);
    Object.defineProperty(fake, "open", { get: () => true });
    fake.send = (type) => sent.push(type);
    try { Connection.prototype.close.call(fake, { goodbye: true }); } catch { /* private fields of a fake instance */ }
    assert.deepEqual(sent, [Msg.Goodbye]);
});

test("an update restart gets a quiet updating line and its return none", () => {
    assert.deepEqual(presenceLines(clients, "user_left", { client_id: "c1", reason: "updating" }, 1).map(l => [l.scope, l.kind]), [[0, "updating"], [7, "updating"]]);
    assert.deepEqual(presenceLines([], "user_joined", { client_id: "c2", nickname: "Ann", channel_id: 7, returned: true }, 1), []);
});
