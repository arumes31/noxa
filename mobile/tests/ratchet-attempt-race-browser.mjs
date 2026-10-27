// Review P1-1: two writers (tabs, or an old and new consumer) that pick the
// same attempt sequence must conflict, never "reuse" each other's attempt.
import assert from "node:assert/strict";
import { test } from "node:test";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chromium } from "@playwright/test";
const root = fileURLToPath(new URL("../", import.meta.url));

test("same attempt sequence with different content conflicts; exact retry reuses", { timeout: 60000 }, async () => {
    const server = createServer(async (req, res) => {
        const p = new URL(req.url, "http://localhost").pathname;
        if (p === "/") { res.setHeader("Content-Type", "text/html"); res.end("<!doctype html><title>attempt race</title>"); return; }
        const file = path.resolve(root, "." + p);
        if (!file.startsWith(root) && !file.startsWith(path.resolve(root))) { res.writeHead(404).end(); return; }
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
            const enc = new TextEncoder(), dec = new TextDecoder();
            const ns = { server: "tls-sha256:" + "a".repeat(64), principal: "alice", device: "a" };
            const s = await openRatchetStore(ns);
            await s.setTrust(await s.read([["trust", "bob"]]), "bob", ["v"], false);
            const build = async (logical, sid) => {
                const m = { version: wire.ADMISSION_VERSION, server: ns.server, sender: "alice", device: "a", account_epoch: 1, attempt_id: "1",
                    logical_id: logical, purpose: "chat", scope: "direct", peer: "bob", deadline: 1900000000,
                    rosters: [{ principal: "alice", version: 1 }, { principal: "bob", version: 1 }],
                    targets: [{ principal: "alice", device: "a", account_epoch: 1, identity: "a-key", local: true }, { principal: "bob", device: "b", account_epoch: 1, identity: "b-key" }] };
                const envelope = { version: "noxa-olm-v1", session_id: sid, binding: wire.expectedBinding(m, m.targets[1]), message: { type: 0, body: "c" } };
                const frozen = await wire.freezeAttempt(m, [{ principal: "bob", device: "b", account_epoch: 1, session_id: sid, claim_id: "claim-" + sid, envelope }]);
                const p = { sessionID: "olm:" + sid, trustID: "bob", claim: { claim_id: "claim-" + sid, claim_until: 1900000000 }, state: enc.encode("st"), envelope: wire.jsonBytes(envelope),
                    context: { local_account_id: "local", local_epoch: 1, peer_epoch: 1, peer_principal: "bob", peer_device: "b", peer_identity: "b-key", scope: "device-pair", protocol: "noxa-olm-v1" } };
                return { frozen, p };
            };
            // Both writers read before either commits (nextAttempt=0 -> attempt 1).
            const A = await build("tab-A-logical", "sA"), B = await build("tab-B-logical", "sB");
            const snapA = await s.read([["trust", "bob"], ["session", "olm:sA"], ["logical", "tab-A-logical"]]);
            const snapB = await s.read([["trust", "bob"], ["session", "olm:sB"], ["logical", "tab-B-logical"]]);
            const ra = await s.commitAttempt(snapA, A.frozen.manifest, A.frozen.parts, [A.p], enc.encode("A"));
            let bError = null, bReused = null;
            try { bReused = (await s.commitAttempt(snapB, B.frozen.manifest, B.frozen.parts, [B.p], enc.encode("B"))).reused; }
            catch (e) { bError = e.code; }
            // The exact same frozen attempt (crash/retry of A) is still reusable.
            const again = await s.commitAttempt(await s.read([["trust", "bob"], ["session", "olm:sA"], ["logical", "tab-A-logical"]]), A.frozen.manifest, A.frozen.parts, [A.p], enc.encode("A"));
            // Device-pair scope: a pre-rule per-conversation context is invalid
            // (a valid context would reach the reuse check and give "conflict").
            const L = await build("legacy-logical", "sL");
            L.p.context = { ...L.p.context, scope: undefined, conversation: "direct:[\"alice\",\"bob\"]" };
            delete L.p.context.scope;
            let legacy = null;
            try { await s.commitAttempt(await s.read([["trust", "bob"], ["session", "olm:sL"], ["logical", "legacy-logical"]]), L.frozen.manifest, L.frozen.parts, [L.p], enc.encode("L")); }
            catch (e) { legacy = e.code; }
            const k = o => JSON.stringify(o);
            const stored = await s.read([["outbox", "1:1"], ["logical", "tab-B-logical"], ["session", "olm:sB"]]);
            const outbox = JSON.parse(dec.decode(stored.records[k(["outbox", "1:1"])].data));
            return { legacy, aReused: ra.reused, bReused, bError, againReused: again.reused, outboxLogical: outbox.attempt.manifest.logical_id,
                bLogical: stored.records[k(["logical", "tab-B-logical"])].revision, bSession: stored.records[k(["session", "olm:sB"])].revision };
        });
        assert.deepEqual(out, { legacy: "invalid", aReused: false, bReused: null, bError: "conflict", againReused: true, outboxLogical: "tab-A-logical", bLogical: 0, bSession: 0 });
    } finally {
        await browser.close();
        server.close();
    }
});
