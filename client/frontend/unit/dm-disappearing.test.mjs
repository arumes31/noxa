import test from "node:test";
import assert from "node:assert/strict";
import { DISAPPEAR_OPTIONS, applyExpired, applyExpiries, durationLabel, needsStart, remainingSeconds, timerLabel } from "../src/dm-disappearing.js";
import { disappearingFromBody, ratchetDMMessage } from "../src/ratchet-projection.js";

const record = (extra = {}) => ({ protocol: "noxa-olm-v1", scope: "direct", peer: "bob", sender: "bob", logical_id: "m1", key: "k", purpose: "chat",
    body: { text: "hi" }, sent_at: 1000, self: false, copied: false, transport: {}, ...extra });

test("only valid disappearing fields are read from a decrypted body", () => {
    assert.deepEqual(disappearingFromBody({ text: "a", ttl: 3600 }), { ttl: 3600, timer: null, expired: false });
    assert.deepEqual(disappearingFromBody({ ttl: -1 }), { ttl: 0, timer: null, expired: false });
    assert.deepEqual(disappearingFromBody({ ttl: 1e9 }), { ttl: 0, timer: null, expired: false });
    assert.deepEqual(disappearingFromBody({ ttl: 1.5 }), { ttl: 0, timer: null, expired: false });
    assert.deepEqual(disappearingFromBody({ disappearing: 0 }), { ttl: 0, timer: 0, expired: false });
    assert.deepEqual(disappearingFromBody({ disappearing: 300 }), { ttl: 0, timer: 300, expired: false });
    assert.deepEqual(disappearingFromBody({ disappearing: "300" }), { ttl: 0, timer: null, expired: false });
    assert.deepEqual(disappearingFromBody({ expired: true }), { ttl: 0, timer: null, expired: true });
    assert.deepEqual(disappearingFromBody(null), { ttl: 0, timer: null, expired: false });
});

test("live records carry the timer: own copies from sending, received ones on display", () => {
    const own = ratchetDMMessage(record({ self: true, sender: "me", body: { text: "x", ttl: 60 } }));
    assert.equal(own.ttl, 60);
    assert.equal(own.expiresAt, 1060);
    const received = ratchetDMMessage(record({ body: { text: "x", ttl: 60 } }));
    assert.equal(received.ttl, 60);
    assert.equal(received.expiresAt, 0, "starts only when shown");
    const notice = ratchetDMMessage(record({ body: { text: "on", disappearing: 3600 } }));
    assert.equal(notice.timer, 3600);
    assert.equal(notice.ttl, 0, "a setting change never expires itself");
    const redacted = ratchetDMMessage(record({ body: { expired: true } }));
    assert.equal(redacted.deleted, true);
    assert.equal(redacted.text, "");
    const plain = ratchetDMMessage(record());
    assert.equal(plain.ttl, 0);
    assert.equal(plain.timer, null);
});

test("expiry results update only the matching messages", () => {
    const msgs = [
        { fromUID: "bob", clientMsgID: "m1", text: "secret", ttl: 60, expiresAt: 0 },
        { fromUID: "me", clientMsgID: "m1", text: "same id, other sender", ttl: 60, expiresAt: 0, self: true },
        { fromUID: "bob", clientMsgID: "m2", text: "keeps", ttl: 0 },
    ];
    assert.equal(needsStart(msgs), true);
    const started = applyExpiries(msgs, [{ from_unique_id: "bob", client_msg_id: "m1", expires_at: 2000 }]);
    assert.deepEqual(started, [msgs[0]]);
    assert.equal(msgs[0].expiresAt, 2000);
    assert.equal(msgs[1].expiresAt, 0);
    assert.equal(needsStart(msgs), false, "own copies never wait for display");
    const gone = applyExpired(msgs, [{ from_unique_id: "bob", client_msg_id: "m1" }]);
    assert.deepEqual(gone, [msgs[0]]);
    assert.equal(msgs[0].text, "");
    assert.equal(msgs[0].deleted, true);
    assert.equal(msgs[0].expired, true);
    assert.equal(msgs[1].text, "same id, other sender");
    assert.equal(msgs[2].text, "keeps");
    assert.deepEqual(applyExpired(msgs, [{ from_unique_id: "bob", client_msg_id: "m1" }]), [], "expiring twice changes nothing");
});

test("labels and countdowns", () => {
    assert.deepEqual([...DISAPPEAR_OPTIONS], [0, 30, 300, 3600, 86400, 604800]);
    for (const seconds of DISAPPEAR_OPTIONS) assert.ok(timerLabel(seconds).length > 0);
    assert.equal(durationLabel(45), "45s");
    assert.equal(durationLabel(125), "2m");
    assert.equal(durationLabel(7200), "2h");
    assert.equal(durationLabel(3 * 86400 + 5), "3d");
    assert.equal(durationLabel(-4), "0s");
    assert.equal(remainingSeconds(1010, 1_000_000), 10);
    assert.equal(remainingSeconds(900, 1_000_000), 0);
});
