import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { initialOneTimeKey, semanticKey } from "../src/core/ratchet-consumer.js";
import { legacyID } from "../src/core/groups.js";

test("initial one-time key comes from the Olm v1 prekey message body", async () => {
    const vector = JSON.parse(await readFile(new URL("../../internal/e2ee/ratchet/testdata/interop-vector.json", import.meta.url), "utf8"));
    const body = vector.inbound.envelope.message.body;
    const raw = Buffer.from(body, "base64");
    assert.equal(initialOneTimeKey(body), raw.subarray(3, 35).toString("base64").replace(/=+$/, ""));
    const normal = Buffer.from(raw); normal[0] = 2;
    assert.throws(() => initialOneTimeKey(normal.toString("base64")), /invalid/);
    assert.throws(() => initialOneTimeKey("AA"), /invalid/);
});

test("semantic keys keep senders reusing one logical string distinct", () => {
    assert.notEqual(semanticKey("alice", "m1"), semanticKey("bob", "m1"));
    assert.equal(semanticKey("alice", "m1"), JSON.stringify(["noxa-olm-v1", "alice", "m1"]));
    // Delimiter injection cannot forge another sender's key.
    assert.notEqual(semanticKey("a\",\"b", "c"), semanticKey("a", "b\",\"c"));
});

test("only numeric accepted IDs are legacy group history positions", () => {
    assert.equal(legacyID({ id: 7 }), true);
    assert.equal(legacyID({ id: "olm:[\"noxa-olm-v1\",\"a\",\"1\"]" }), false);
    assert.equal(legacyID({ id: "123" }), false, "no string-to-number coercion");
    assert.equal(legacyID({ id: 7, local: true }), false);
});
