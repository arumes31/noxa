// Disappearing messages (plan item 28): RatchetStore.redactArchive on real
// Chromium IndexedDB. A redacted inbox record keeps its digest, so a
// redelivery of the same copy returns the redacted archive, never the
// original text; a stale snapshot is refused; only inbox/outbox records that
// exist can be redacted; an unsent outbox batch is refused. Mirrors
// internal/e2ee/durable/redact_test.go.
import assert from "node:assert/strict";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { chromium } from "@playwright/test";

test("redactArchive on real IndexedDB keeps dedupe and refuses stale or unsent records", { timeout: 60000 }, async (t) => {
    const vite = await createServer({ configFile: false, root: fileURLToPath(new URL("../", import.meta.url)), server: { host: "127.0.0.1", port: 0 } });
    await vite.listen();
    let browser, launch, cleaning;
    const cleanup = () => cleaning ||= (async () => {
        try { await (browser || await launch)?.close(); }
        finally { await vite.close(); }
    })();
    t.after(cleanup);
    launch = chromium.launch();
    browser = await launch;
    const page = await browser.newPage();
    await page.route("**/__redact__", route => route.fulfill({ contentType: "text/html", body: "<!doctype html><title>redaction test</title>" }));
    await page.goto(`http://127.0.0.1:${vite.httpServer.address().port}/__redact__`);
    const out = await page.evaluate(async () => {
        const { openRatchetStore, DEVICE_PAIR_SCOPE } = await import("/src/core/ratchet-store.js");
        const enc = new TextEncoder(), dec = new TextDecoder();
        const code = p => p.then(() => "ok", e => e.code || e.message);
        const context = { local_account_id: "local", local_epoch: 1, peer_epoch: 1, peer_principal: "bob", peer_device: "bob-device", peer_identity: "bob-olm", scope: DEVICE_PAIR_SCOPE, protocol: "noxa-olm-v1" };
        const store = await openRatchetStore({ server: "srv", principal: "alice", device: "alice-device" });
        await store.initialize();
        await store.setTrust(await store.read([["trust", "bob"]]), "bob", { verified: true });
        const candidate = { trustID: "bob", sessionID: "bob", session: enc.encode("session state"), context, envelope: enc.encode("envelope"),
            archive: enc.encode(JSON.stringify({ scope: "direct", sender: "bob", logicalID: "m1", body: { text: "secret", ttl: 60 } })) };
        await store.commitReceive(await store.read([["trust", "bob"], ["session", "bob"], ["inbox", "m1"]]), "m1", candidate);
        const inbox = ["inbox", "m1"];
        const redacted = enc.encode(JSON.stringify({ scope: "direct", sender: "bob", logicalID: "m1", body: { expired: true } }));
        const before = await store.read([inbox]);
        const result = { first: await code(store.redactArchive(before, inbox, redacted)) };
        result.stale = await code(store.redactArchive(before, inbox, redacted));
        const again = await store.commitReceive(await store.read([["trust", "bob"], ["session", "bob"], inbox]), "m1", candidate);
        result.redelivered = JSON.parse(dec.decode(again)).body;
        result.session = await code(store.redactArchive(await store.read([["session", "bob"]]), ["session", "bob"], redacted));
        result.missing = await code(store.redactArchive(await store.read([["inbox", "missing"]]), ["inbox", "missing"], redacted));
        result.empty = await code(store.redactArchive(await store.read([inbox]), inbox, new Uint8Array()));
        return result;
    });
    assert.deepEqual(out, { first: "ok", stale: "conflict", redelivered: { expired: true }, session: "invalid", missing: "invalid", empty: "invalid" });
});
