import { test } from "node:test";
import assert from "node:assert/strict";
import { privateCallRTCConfiguration } from "../src/private-call-privacy.js";
test("private calls fail closed without authenticated TURN", () => {
    for (const servers of [[], [{ urls: "stun:example.com" }], [{ urls: "turn:example.com" }]]) {
        assert.throws(() => privateCallRTCConfiguration(servers), /TURN relay/);
    }
});
test("private calls use relay candidates only", () => {
    const relay = { urls: ["turns:example.com:448"], username: "session", credential: "secret" };
    assert.deepEqual(privateCallRTCConfiguration([{ urls: "stun:example.com" }, relay]), { iceServers: [relay], iceTransportPolicy: "relay" });
});
