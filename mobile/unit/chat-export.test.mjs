import assert from "node:assert/strict";
import { test } from "node:test";

import { EXPORT_ITERATIONS, exportFileName, exportJSON, openExport, sealExport, stamp, transcript } from "../src/core/chat-export.js";

const at = new Date(2026, 9, 24, 13, 5, 9).getTime() / 1000;
const messages = [
    { id: 1, from: "Ada", fromUniqueID: "u1", sentAt: at, text: "hello **there**", encryption: "server" },
    { id: 2, from: "Bob", fromUniqueID: "u2", sentAt: at + 60, text: null, encryption: "server" },
    { id: 3, from: "Ada", fromUniqueID: "u1", sentAt: at + 120, text: "", deleted: true, encryption: "server" },
    { id: 4, from: "Eve", fromUniqueID: "u3", sentAt: at + 180, text: "fixed", edited: true, encryption: "e2e" },
];

test("the transcript matches the desktop's line shape and marks gaps", () => {
    const text = transcript(messages, { title: "#general", complete: false });
    const lines = text.trimEnd().split("\n");
    assert.equal(lines[0], "# noXa export — #general");
    assert.equal(lines[1], "# 4 messages, 1 unreadable (no key), TRUNCATED: only the loaded history");
    assert.equal(lines[2], `[${stamp(at)}] [server encrypted] Ada: hello **there**`);
    assert.equal(lines[3], `[${stamp(at + 60)}] [server encrypted] Bob: (could not be decrypted)`);
    assert.equal(lines[4], `[${stamp(at + 120)}] Ada: (deleted)`);
    assert.equal(lines[5], `[${stamp(at + 180)}] [end-to-end encrypted] Eve: fixed (edited)`);
    assert.equal(stamp(at), "2026-10-24 13:05:09");
    assert.equal(transcript([]), "");
});

test("the JSON export keeps sender IDs and nulls unreadable bodies", () => {
    const data = JSON.parse(exportJSON(messages, { title: "g", kind: "channel", exportedAt: 5 }));
    assert.equal(data.format, "noxa-chat-transcript");
    assert.equal(data.exported_at, 5);
    assert.equal(data.messages.length, 4);
    assert.deepEqual(data.messages[0], { id: 1, from: "Ada", from_unique_id: "u1", sent_at: at, text: "hello **there**", deleted: false, edited: false, encryption: "server" });
    assert.equal(data.messages[1].text, null);
    assert.equal(data.messages[2].text, null);
});

test("the encrypted container round-trips and rejects a wrong passphrase", async () => {
    const plain = exportJSON(messages, { title: "g" });
    const sealed = await sealExport(plain, "correct horse", { iterations: 1000 });
    const box = JSON.parse(sealed);
    assert.equal(box.format, "noxa-chat-export");
    assert.equal(box.v, 1);
    assert.equal(box.kdf, "pbkdf2-sha256");
    assert.equal(box.cipher, "aes-256-gcm");
    assert.equal(box.iter, 1000);
    assert.equal(Buffer.from(box.salt, "base64").length, 16);
    assert.equal(Buffer.from(box.iv, "base64").length, 12);
    assert.ok(!sealed.includes("Ada"));
    assert.equal(await openExport(sealed, "correct horse"), plain);
    await assert.rejects(openExport(sealed, "wrong"), /wrong passphrase/);
    await assert.rejects(openExport({ format: "other" }, "x"), /not a noXa/);
    await assert.rejects(sealExport(plain, ""), /passphrase is required/);
});

test("exports default to 600k PBKDF2 rounds", async () => {
    assert.equal(EXPORT_ITERATIONS, 600000);
    const box = JSON.parse(await sealExport("x", "passphrase"));
    assert.equal(box.iter, 600000);
    assert.equal(await openExport(box, "passphrase"), "x");
});

test("file names are slugged and dated", () => {
    assert.equal(exportFileName("# General Chat!", "txt", new Date(2026, 0, 5)), "noxa-general-chat-20260105.txt");
    assert.equal(exportFileName("", "json", new Date(2026, 0, 5)), "noxa-chat-20260105.json");
});
