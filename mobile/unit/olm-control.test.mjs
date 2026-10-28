// 1c-iii-b item 3: explicitly targeted, expiring media controls (mirror of
// internal/e2ee/admission/control.go and control_test.go).
import test from "node:test";
import assert from "node:assert/strict";
import { validateManifest, expectedBinding, controlTarget, parseControlBody, validateControlSend, controlDisposition, MAX_CONTROL_TTL } from "../src/core/olm-admission.js";

const manifest = () => ({
    version: "noxa-olm-admission-v1", server: "tls-sha256:" + "a".repeat(64), sender: "a", device: "d0", account_epoch: 1, attempt_id: "1", logical_id: "ctl1",
    purpose: "control", scope: "direct", peer: "b", deadline: 1900000000, rosters: [{ principal: "a", version: 1 }, { principal: "b", version: 1 }],
    targets: [{ principal: "a", device: "d0", account_epoch: 1, identity: "i0", local: true }, { principal: "b", device: "b1", account_epoch: 2, identity: "i1" }],
    parts: [{ digest: "c".repeat(64), length: 2 }],
});

test("a control manifest names exactly one non-local device of the peer", () => {
    const m = manifest();
    validateManifest(m);
    assert.equal(controlTarget(m).device, "b1");
    assert.equal(expectedBinding(m, m.targets[1]).purpose, "media-key");
    const own = manifest();
    own.peer = "a"; own.rosters = [{ principal: "a", version: 1 }]; own.targets[1].principal = "a";
    validateManifest(own);
    const bad = {
        fanout: m => m.targets.push({ principal: "b", device: "b2", account_epoch: 1, identity: "i2" }),
        ownBesidePeer: m => { m.targets[1] = { principal: "a", device: "d1", account_epoch: 1, identity: "i3" }; },
        noTarget: m => { m.targets = m.targets.slice(0, 1); },
        reference: m => { m.reference = "msg"; },
        group: m => { m.scope = "group"; delete m.peer; m.conversation = "g"; m.revision = 1; m.epoch = 1; },
    };
    for (const [name, mutate] of Object.entries(bad)) {
        const x = manifest();
        mutate(x);
        assert.throws(() => validateManifest(x), undefined, name);
    }
});

test("control body: shape, expiry window and disposition", () => {
    const now = 1_800_000_000;
    const ok = { kind: "media-key", scope: { type: "channel", id: "42" }, epoch: 3, expires_at: now + 60, data: { key: "abc" } };
    assert.equal(validateControlSend(ok, now).kind, "media-key");
    for (const [name, body] of Object.entries({
        expired: { ...ok, expires_at: now },
        tooLong: { ...ok, expires_at: now + MAX_CONTROL_TTL + 1 },
        unknown: { ...ok, x: 1 },
        badKind: { ...ok, kind: "Media Key" },
        noScopeID: { ...ok, scope: { type: "channel", id: "" } },
        hugeData: { ...ok, data: "a".repeat(4096) },
        array: [1],
    })) assert.throws(() => validateControlSend(body, now), undefined, name);
    assert.equal(parseControlBody({ kind: "media-key", scope: { type: "call", id: "c" }, epoch: 0, expires_at: 5 }).epoch, 0);
    assert.equal(controlDisposition(ok, now), "delivered");
    assert.equal(controlDisposition({ ...ok, expires_at: now }, now), "expired");
    assert.equal(controlDisposition({ nope: 1 }, now), "invalid");
});
