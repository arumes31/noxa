// Fix2-review P3-2: retrySend's final validation commit must re-read on a
// pure revision move (another tab's unrelated write) and fail with
// "conflict" only on a real trust-revision mismatch or non-pending attempt.
import assert from "node:assert/strict";
import { test } from "node:test";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chromium } from "@playwright/test";
const root = fileURLToPath(new URL("../", import.meta.url));

test("retrySend survives an unrelated concurrent write; a real trust change still conflicts", { timeout: 60000 }, async () => {
    const server = createServer(async (req, res) => {
        const p = new URL(req.url, "http://localhost").pathname;
        if (p === "/") { res.setHeader("Content-Type", "text/html"); res.end("<!doctype html><title>retry validation</title>"); return; }
        const file = path.resolve(root, "." + p);
        if (!file.startsWith(path.resolve(root))) { res.writeHead(404).end(); return; }
        try { res.setHeader("Content-Type", "application/javascript"); res.end(await readFile(file)); } catch { res.writeHead(404).end(); }
    });
    await new Promise(r => server.listen(0, "127.0.0.1", r));
    const browser = await chromium.launch();
    try {
        const page = await browser.newPage();
        await page.goto(`http://127.0.0.1:${server.address().port}/`);
        const out = await page.evaluate(async () => {
            const { openRatchetStore } = await import("/src/core/ratchet-store.js");
            const wire = await import("/src/core/olm-admission.js");
            const enc = new TextEncoder();
            const ns = { server: "tls-sha256:" + "b".repeat(64), principal: "alice", device: "a" };
            const s = await openRatchetStore(ns);
            await s.setTrust(await s.read([["trust", "bob"]]), "bob", ["v"], false);
            const m = { version: wire.ADMISSION_VERSION, server: ns.server, sender: "alice", device: "a", account_epoch: 1, attempt_id: "1",
                logical_id: "L1", purpose: "chat", scope: "direct", peer: "bob", deadline: 1900000000,
                rosters: [{ principal: "alice", version: 1 }, { principal: "bob", version: 1 }],
                targets: [{ principal: "alice", device: "a", account_epoch: 1, identity: "a-key", local: true }, { principal: "bob", device: "b", account_epoch: 1, identity: "b-key" }] };
            const envelope = { version: "noxa-olm-v1", session_id: "s1", binding: wire.expectedBinding(m, m.targets[1]), message: { type: 0, body: "c" } };
            const frozen = await wire.freezeAttempt(m, [{ principal: "bob", device: "b", account_epoch: 1, session_id: "s1", claim_id: "claim-s1", envelope }]);
            const p = { sessionID: "olm:s1", trustID: "bob", claim: { claim_id: "claim-s1", claim_until: 1900000000 }, state: enc.encode("st"), envelope: wire.jsonBytes(envelope),
                context: { local_account_id: "local", local_epoch: 1, peer_epoch: 1, peer_principal: "bob", peer_device: "b", peer_identity: "b-key", scope: "device-pair", protocol: "noxa-olm-v1" } };
            await s.commitAttempt(await s.read([["trust", "bob"], ["session", "olm:s1"], ["logical", "L1"]]), frozen.manifest, frozen.parts, [p], enc.encode("hello"));
            // Inject a write by "another tab" right after retrySend's second read,
            // i.e. before its final validation commit.
            const orig = s.extended.bind(s);
            let inject = null, n = 0;
            s.extended = async (a, b) => { const r = await orig(a, b); if (++n === 2 && inject) { const f = inject; inject = null; await f(); } return r; };
            const run = async () => { n = 0; try { return { ok: (await s.attemptWire(await s.read(), "1:1")).manifest.logical_id }; } catch (e) { return { err: e.code }; } };
            inject = async () => s.setTrust(await s.read([["trust", "carol"]]), "carol", ["w"], false);
            const unrelated = await run();
            inject = async () => s.setTrust(await s.read([["trust", "bob"]]), "bob", ["v2"], false);
            const trustMoved = await run();
            return { unrelated, trustMoved };
        });
        assert.deepEqual(out, { unrelated: { ok: "L1" }, trustMoved: { err: "conflict" } });
    } finally {
        await browser.close();
        server.close();
    }
});
