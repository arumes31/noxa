import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { BackupError, exportBackup, importBackup } from "../src/core/backup.js";
import { fromBase64, generateIdentity, keyFingerprint, openDM, publicKeyPEM, toBase64 } from "../src/core/crypto.js";
import { approvalTranscript, buildHistoryChunks, endorseDevice, historyMessagesSince, needsConfirmation } from "../src/core/devices.js";
import { isForThisServer, parseInviteLink, pendingInvite, setPendingInvite, webInviteURL } from "../src/core/invite.js";
import { classifySignIn } from "../src/core/login-errors.js";
import { addServer, listServers, normalizeServer, removeServer } from "../src/core/servers.js";
import nacl from "tweetnacl";
import { decodeHistoryChunk } from "../src/core/history-envelope.js";
const chunkKey = generateIdentity();
const envelope = { source: "phone", target: "dev-2", transfer: "t1", edSecret: chunkKey.edSecret };
const openChunk = (c, sender, target, seq, total) => decodeHistoryChunk(openDM(c, sender.publicKey, target.secretKey), { source: "phone", target: "dev-2", seq, total, signingKey: chunkKey.edPublic });

class MemoryStorage {
    #items = new Map();
    getItem(key) { return this.#items.has(key) ? this.#items.get(key) : null; }
    setItem(key, value) { this.#items.set(key, String(value)); }
    removeItem(key) { this.#items.delete(key); }
}

const TOKEN = "A".repeat(42) + "w";

describe("invitation links", () => {
    it("parses the desktop's noxa:// form and the web forms", () => {
        assert.deepEqual(parseInviteLink(`noxa://chat.example:12345/invite/42#${TOKEN}`),
            { host: "chat.example:12345", hostname: "chat.example", channelID: 42, token: TOKEN, web: false });
        assert.deepEqual(parseInviteLink(`https://chat.example/app/invite/7#${TOKEN}`),
            { host: "chat.example", hostname: "chat.example", channelID: 7, token: TOKEN, web: true });
        assert.equal(parseInviteLink(`https://chat.example/invite/7#${TOKEN}`)?.channelID, 7);
    });

    it("rejects malformed links", () => {
        for (const link of [
            `noxa://chat.example/invite/0#${TOKEN}`, `noxa://chat.example/invite/x#${TOKEN}`, `noxa://chat.example/invite/4#short`,
            `noxa://chat.example/invite/4?x=1#${TOKEN}`, `noxa://user:pw@chat.example/invite/4#${TOKEN}`, `ftp://chat.example/invite/4#${TOKEN}`,
            `noxa://chat.example/app/invite/4#${TOKEN}`, `https://chat.example/app/other/4#${TOKEN}`, "", "not a url",
            `noxa://chat.example/invite/4#${TOKEN}=`,
        ]) assert.equal(parseInviteLink(link), null, link);
    });

    it("decides whether an invitation belongs to this server", () => {
        const location = { host: "chat.example", hostname: "chat.example" };
        assert.equal(isForThisServer(parseInviteLink(`noxa://chat.example:12345/invite/4#${TOKEN}`), location), true);
        assert.equal(isForThisServer(parseInviteLink(`https://chat.example/app/invite/4#${TOKEN}`), location), true);
        const other = parseInviteLink(`noxa://other.example:12345/invite/4#${TOKEN}`);
        assert.equal(isForThisServer(other, location), false);
        assert.equal(webInviteURL(other), `https://other.example/app/invite/4#${TOKEN}`);
    });

    it("keeps a pending invitation until it is redeemed", () => {
        const store = new MemoryStorage();
        assert.equal(pendingInvite(store), null);
        setPendingInvite({ channelID: 9, token: TOKEN }, store);
        assert.deepEqual(pendingInvite(store), { channelID: 9, token: TOKEN });
        setPendingInvite(null, store);
        assert.equal(pendingInvite(store), null);
        store.setItem("noxa.pendingInvite", "{\"channelID\":-1,\"token\":\"x\"}");
        assert.equal(pendingInvite(store), null);
    });
});

describe("server list", () => {
    it("normalizes addresses to https origins", () => {
        assert.equal(normalizeServer("chat.example"), "https://chat.example");
        assert.equal(normalizeServer("https://chat.example/app/#/home"), "https://chat.example");
        assert.equal(normalizeServer("chat.example:8443"), "https://chat.example:8443");
        assert.equal(normalizeServer("http://127.0.0.1:5174"), "http://127.0.0.1:5174");
        assert.equal(normalizeServer("http://chat.example"), null);
        assert.equal(normalizeServer("https://user:pw@chat.example"), null);
        assert.equal(normalizeServer("chat example"), null);
        assert.equal(normalizeServer(""), null);
    });

    it("adds, renames and removes servers", () => {
        const store = new MemoryStorage();
        assert.deepEqual(listServers(store), []);
        addServer("https://a.example", "Alpha", store);
        addServer("b.example", "", store);
        addServer("https://a.example/app/", "", store); // same origin, name kept
        assert.deepEqual(listServers(store), [{ origin: "https://a.example", name: "Alpha" }, { origin: "https://b.example", name: "" }]);
        addServer("https://b.example", "Beta", store);
        assert.equal(listServers(store)[1].name, "Beta");
        removeServer("https://a.example", store);
        assert.deepEqual(listServers(store), [{ origin: "https://b.example", name: "Beta" }]);
        assert.throws(() => addServer("ftp://x", "", store));
    });

    it("ignores damaged storage", () => {
        const store = new MemoryStorage();
        store.setItem("noxa.servers", "{not json");
        assert.deepEqual(listServers(store), []);
        store.setItem("noxa.servers", JSON.stringify([{ origin: "javascript:alert(1)" }, { origin: "https://ok.example", name: 5 }]));
        assert.deepEqual(listServers(store), [{ origin: "https://ok.example", name: "" }]);
    });
});

describe("device key backup", () => {
    const identity = generateIdentity();
    const meta = { deviceID: "dev-1", uniqueID: "uid-1", nickname: "Rowan", approverFingerprint: "aaaa bbbb cccc dddd", via: "link" };

    it("round-trips the keys and link metadata", async () => {
        const text = await exportBackup(identity, meta, "correct horse", { iterations: 1000, server: "https://chat.example", serverName: "Chat" });
        const container = JSON.parse(text);
        assert.equal(container.v, 1);
        assert.equal(container.kdf, "PBKDF2-SHA256");
        assert.equal(container.iter, 1000);
        assert.equal(fromBase64(container.salt).length, 16);
        assert.equal(fromBase64(container.iv).length, 12);
        assert.ok(!text.includes(toBase64(identity.edSecret)));
        const restored = await importBackup(text, "correct horse");
        for (const field of ["edPublic", "edSecret", "xPublic", "xSecret"]) assert.deepEqual(restored.identity[field], identity[field]);
        assert.equal(restored.server, "https://chat.example");
        assert.equal(restored.serverName, "Chat");
        assert.deepEqual(restored.meta, { ...meta, restored: true });
    });

    it("uses 600,000 PBKDF2 iterations by default", async () => {
        const text = await exportBackup(identity, meta, "correct horse", { server: "https://chat.example" });
        assert.equal(JSON.parse(text).iter, 600000);
    });

    it("refuses a wrong passphrase, tampering and other files", async () => {
        const text = await exportBackup(identity, meta, "correct horse", { iterations: 1000 });
        await assert.rejects(importBackup(text, "wrong horse"), error => error instanceof BackupError && error.code === "passphrase");
        const container = JSON.parse(text);
        const data = fromBase64(container.data);
        data[0] ^= 1;
        await assert.rejects(importBackup(JSON.stringify({ ...container, data: toBase64(data) }), "correct horse"), error => error.code === "passphrase");
        await assert.rejects(importBackup(JSON.stringify({ ...container, iter: 10 }), "correct horse"), error => error.code === "format");
        await assert.rejects(importBackup("{\"public_key\":\"x\"}", "x"), error => error.code === "format");
        await assert.rejects(importBackup("not json", "x"), error => error.code === "format");
        await assert.rejects(exportBackup(identity, meta, "short", { iterations: 1000 }));
    });
});

describe("history copy chunks", () => {
    const sender = nacl.box.keyPair();
    const target = nacl.box.keyPair();
    const conversations = [
        { peer: "uid-sam", nickname: "Sam", messages: [
            { id: "m1", from: "uid-sam", fromNickname: "Sam", body: "old", sentAt: 1000.6, verified: true },
            { id: "m2", from: "uid-me", self: true, body: "newer", sentAt: 3000, verified: true },
            { id: "m3", from: "uid-me", self: true, body: "", deleted: true, sentAt: 3500 },
            { id: "m4", from: "uid-me", self: true, body: "queued", queued: true, sentAt: 3600 },
            { id: "m5", from: "uid-sam", body: null, verified: false, sentAt: 3700 },
        ] },
        { peer: "uid-ivy", nickname: "", messages: [{ id: "i1", from: "uid-ivy", fromNickname: "Ivy", body: "hi", sentAt: 2000, verified: true }] },
    ];

    it("collects sent and received messages newest first, within the range", () => {
        const all = historyMessagesSince(conversations, 0);
        assert.deepEqual(all.map(m => m.client_msg_id), ["m2", "i1", "m1"]);
        assert.deepEqual(all[2], { peer: "uid-sam", peer_nickname: "Sam", from: "uid-sam", from_nickname: "Sam", body: "old", sent_at: 1000, self: undefined, client_msg_id: "m1" });
        assert.equal(all[0].self, true);
        assert.deepEqual(historyMessagesSince(conversations, 1500).map(m => m.client_msg_id), ["m2", "i1"]);
    });

    it("seals chunks the receiving device opens, oldest first", () => {
        const messages = historyMessagesSince(conversations, 0);
        const { chunks, kept } = buildHistoryChunks(messages, target.publicKey, sender.secretKey, { envelope });
        assert.equal(kept, 3);
        assert.equal(chunks.length, 1);
        // The receiving side as in core/direct.js importHistory.
        const plain = openChunk(chunks[0], sender, target, 0, 1);
        assert.deepEqual(plain.map(m => m.client_msg_id), ["m1", "i1", "m2"]);
        assert.equal(openDM(chunks[0], sender.publicKey, nacl.box.keyPair().secretKey), null);
    });

    it("splits into several chunks under the size limit", () => {
        const messages = Array.from({ length: 30 }, (_, i) => ({ peer: "p", from: "p", body: "x".repeat(100), sent_at: 1000 - i }));
        const { chunks, kept } = buildHistoryChunks(messages, target.publicKey, sender.secretKey, { maxChunk: 1700, envelope });
        assert.equal(kept, 30);
        assert.ok(chunks.length > 5);
        const opened = chunks.map((c, i) => openChunk(c, sender, target, i, chunks.length));
        for (const chunk of chunks) assert.ok(fromBase64(chunk).length <= 1700);
        // Oldest chunk first, messages oldest first inside each chunk.
        const order = opened.flat().map(m => m.sent_at);
        assert.deepEqual(order, [...order].sort((a, b) => a - b));
    });
});

describe("device confirmation", () => {
    it("signs the approval transcript with this device's key", async () => {
        const me = generateIdentity();
        const other = generateIdentity();
        const pem = publicKeyPEM(other.edPublic);
        const target = { device_id: "dev-2", name: "Tablet", fingerprint: keyFingerprint(pem), ed25519_public_key: pem, x25519_public_key: toBase64(other.xPublic) };
        assert.equal(needsConfirmation(target), true);
        assert.equal(needsConfirmation({ ...target, confirmed: true }), false);
        let sent;
        const session = {
            uniqueID: "uid-1", deviceID: "dev-1", identity: me,
            conn: { request: async (type, request) => { sent = request; return { action: "endorse", device: { ...target, confirmed: true } }; } },
        };
        const device = await endorseDevice(session, target);
        assert.equal(device.confirmed, true);
        assert.equal(sent.action, "endorse");
        assert.equal(sent.device_id, "dev-2");
        const transcript = approvalTranscript("uid-1", "dev-1", pem, target.x25519_public_key);
        assert.ok(nacl.sign.detached.verify(transcript, fromBase64(sent.signature), me.edPublic));
        await assert.rejects(endorseDevice(session, { ...target, fingerprint: "0000 0000 0000 0000" }));
        // 1c-iii-b item 0: the X25519 key is validated before anything is signed.
        sent = null;
        for (const x of [target.x25519_public_key + "\nextra", "short", "A".repeat(43) + "=", " " + target.x25519_public_key])
            await assert.rejects(endorseDevice(session, { ...target, x25519_public_key: x }), /invalid/);
        assert.equal(sent, null, "nothing signed or sent for invalid keys");
        await assert.rejects(endorseDevice(session, { ...target, ed25519_public_key: pem + "x: y\n", fingerprint: keyFingerprint(pem + "x: y\n") }), /invalid/);
    });
});

describe("sign-in failures", () => {
    it("classifies the server's reasons like the desktop", () => {
        assert.deepEqual(classifySignIn(new Error("client version rejected: installed 1.0.0; required >=2.0.0; update noXa to connect")),
            { kind: "minimumVersion", installed: "1.0.0", required: "2.0.0" });
        assert.equal(classifySignIn("invalid server password").kind, "serverPassword");
        assert.equal(classifySignIn("banned").kind, "banned");
        assert.equal(classifySignIn("banned: spam").kind, "banned");
        assert.equal(classifySignIn("too many failed logins, try again later").kind, "rateLimit");
        assert.equal(classifySignIn(new Error("could not reach the server")).kind, "network");
        assert.equal(classifySignIn("unsupported authorization model; upgrade your noXa client").kind, "version");
        assert.equal(classifySignIn("this device was signed out; link it again").kind, "revoked");
        assert.equal(classifySignIn("server is full").kind, "full");
        assert.equal(classifySignIn("something new").kind, "unknown");
    });
});
