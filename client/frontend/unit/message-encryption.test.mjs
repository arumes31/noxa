import assert from "node:assert/strict";
import test from "node:test";
import { DOWNGRADED, encModeFrom, encryptionBadge, UNOPENED_BODIES } from "../src/message-encryption.js";

const kind = (m) => encryptionBadge(m).kind;

test("every displayed message gets lock, shield or warning", () => {
    // End-to-end: DMs opened locally, legacy e2e flags, private groups.
    assert.equal(kind({ text: "hi", direct: true, enc: true, encVerified: true, encMode: "e2e" }), "lock");
    assert.equal(kind({ text: "hi", e2e: true }), "lock");
    assert.equal(kind({ text: "hi", direct: true, enc: true }), "lock");
    assert.equal(kind({ text: "hi", enc: true, encMode: "e2e" }), "lock");
    assert.equal(encryptionBadge({ text: "hi", enc: true, encMode: "e2e" }).label, "chat.groupHelp");
    assert.equal(encryptionBadge({ text: "hi", direct: true, encMode: "e2e" }).label, "chat.directHelp");

    // Client→server→client: channel and global chat only.
    assert.equal(kind({ text: "hi", enc: true }), "shield");
    assert.equal(kind({ text: "hi", enc: true, encVerified: true, encMode: "server" }), "shield");
    assert.equal(kind({ text: "hi", channelID: 4, enc: true, encVerified: true, encMode: "server" }), "shield");
    assert.equal(encryptionBadge({ text: "hi", enc: true }).label, "chat.channelHelp");
    assert.equal(encryptionBadge({ text: "hi", enc: true, encVerified: true, encMode: "server" }).label, "chat.channelHelp");
    // DMs are end-to-end only: a server-mode DM is a warning, never a shield.
    const serverDM = encryptionBadge({ text: "hi", direct: true, enc: true, encVerified: true, encMode: "server" });
    assert.deepEqual(serverDM, { kind: "warning", label: "chat.downgradedHelp", bodyKey: "" });

    // No evidence at all: plaintext is refused, never shown.
    for (const m of [{ text: "plain" }, { text: "plain", direct: true }, { text: "plain", channelID: 4 }]) {
        assert.deepEqual(encryptionBadge(m), { kind: "warning", label: "chat.plaintextHelp", bodyKey: "chat.plaintextRefused" });
    }
    // A legacy local DM record keeps its text under a warning.
    assert.deepEqual(encryptionBadge({ text: "old", direct: true, localUnverified: true }),
        { kind: "warning", label: "chat.unverifiedHelp", bodyKey: "" });

    // Tombstones carry no content and no badge.
    assert.equal(kind({ deleted: true, enc: true }), "");
    assert.equal(kind(null), "");
});

test("sealed and refused placeholders always draw the warning", () => {
    assert.equal(UNOPENED_BODIES.size, 6);
    for (const [text, bodyKey] of UNOPENED_BODIES) {
        // Even with verification flags a placeholder is never an ordinary
        // encrypted message.
        for (const flags of [{}, { enc: true }, { e2e: true }, { encVerified: true, encMode: "server" }]) {
            assert.deepEqual(encryptionBadge({ text, ...flags }), { kind: "warning", label: "chat.encryptedHelp", bodyKey });
        }
    }
    assert.equal(encryptionBadge({ text: "[refused: server sent plaintext history]", enc: true }).bodyKey, "chat.plaintextRefused");
});

test("a downgraded DM shows its body under a warning, never verified", () => {
    const downgraded = { text: "hi", direct: true, enc: false, encVerified: false, encMode: DOWNGRADED, localUnverified: true };
    assert.deepEqual(encryptionBadge(downgraded), { kind: "warning", label: "chat.downgradedHelp", bodyKey: "" });
    // Stale legacy flags never lift it to a lock or shield.
    assert.equal(kind({ ...downgraded, enc: true, e2e: true }), "warning");
    // A placeholder still reads as sealed, not as a downgraded body.
    assert.equal(encryptionBadge({ ...downgraded, text: "[encrypted message — key unavailable]" }).label, "chat.encryptedHelp");
    assert.equal(kind({ ...downgraded, deleted: true }), "");
});

test("encModeFrom pairs each mode with its verification state", () => {
    assert.equal(encModeFrom(true, "e2e"), "e2e");
    assert.equal(encModeFrom(true, "server"), "server");
    assert.equal(encModeFrom(true, "downgraded"), "");
    assert.equal(encModeFrom(true, "bogus"), "");
    assert.equal(encModeFrom(false, "downgraded"), "downgraded");
    assert.equal(encModeFrom(false, "e2e"), "");
    assert.equal(encModeFrom(false, "server"), "");
    assert.equal(encModeFrom(false, undefined), "");
});
