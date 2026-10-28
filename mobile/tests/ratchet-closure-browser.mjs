// 1c-iii-a item 3 (PWA): the one-time-key closure lifecycle runs before each
// publication refresh. Only expired, unconsumed, unclosed obligations are
// asked about; only keys the server authenticates as "closed" are retired in
// the core and committed (real RatchetStore on Chromium IndexedDB) with their
// closure receipts; a forged proof commits nothing. Recording fake core.
import assert from "node:assert/strict";
import { test } from "node:test";
import { createServer } from "node:http";
import { readFile, readdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chromium } from "@playwright/test";
// Built product chunks (npm run build): real WASM core, store and consumer.
const root = fileURLToPath(new URL("../../", import.meta.url));
const assets = await readdir(path.join(root, "web/app/assets"));
const entry = name => {
    const found = assets.find(f => new RegExp("^" + name + "-[\\w-]+\\.js$").test(f));
    assert(found, `build missing ${name}`);
    return found;
};
const entries = { core: entry("ratchet"), store: entry("ratchetStorage"), consumer: entry("ratchetConsumer"), admission: entry("admission") };

test("closure commits exactly the server-closed one-time keys", { timeout: 60000 }, async () => {
    const server = createServer(async (req, res) => {
        const p = new URL(req.url, "http://localhost").pathname;
        if (p === "/") { res.setHeader("Content-Type", "text/html"); res.end("<!doctype html><title>otk closure</title>"); return; }
        const file = path.resolve(root, "web", p.replace(/^\/app\//, "app/"));
        if (!file.startsWith(path.join(root, "web") + path.sep)) { res.writeHead(404).end(); return; }
        try { res.setHeader("Content-Type", file.endsWith(".wasm") ? "application/wasm" : "application/javascript"); res.end(await readFile(file)); } catch { res.writeHead(404).end(); }
    });
    await new Promise(r => server.listen(0, "127.0.0.1", r));
    const browser = await chromium.launch();
    try {
        const page = await browser.newPage();
        await page.goto(`http://127.0.0.1:${server.address().port}/`);
        const out = await page.evaluate(async entries => {
            const [{ createRatchetCore }, { openRatchetStore }, { RatchetConsumer }] = await Promise.all([
                import(`/app/assets/${entries.core}`), import(`/app/assets/${entries.store}`), import(`/app/assets/${entries.consumer}`)]);
            const jsonBytes = v => new TextEncoder().encode(JSON.stringify(v));
            const toBase64 = b => btoa(String.fromCharCode(...b));
            const sha = async bytes => Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", bytes)), x => x.toString(16).padStart(2, "0")).join("");
            const ns = { server: "tls-sha256:" + "ef".repeat(32), principal: "alice", device: "a1" };
            const store = await openRatchetStore(ns);
            const real = await createRatchetCore();
            let otks = null;
            const retired = [];
            const core = {
                async apply(r) {
                    const out = await real.apply(r);
                    if (r.op === "new_account") otks = out.bundle.one_time_keys.slice(0, 3).map(k => ({ id: k.id, key: k.key }));
                    if (r.op === "retire_prekey") retired.push(r.one_time_key);
                    return out;
                },
            };
            const asked = [];
            let closedKey = null, forge = false;
            const admission = async r => {
                if (r.action !== "close_keys") throw new Error("unexpected " + r.action);
                // Only the first three keys are reported; the rest stay "available".
                r = { ...r, keys: r.keys.filter(k => otks.some(o => o.id === k)) };
                asked.push([...r.keys].sort());
                return Promise.all(r.keys.map(async (key_id, i) => {
                    const rec = { server: ns.server, device: ns.device, account_epoch: 1, key_id, state: key_id === closedKey ? "closed" : ["available", "reserved"][i % 2], proof: "" };
                    return { ...rec, proof: forge ? "0".repeat(64) : await sha(jsonBytes(rec)) };
                }));
            };
            const consumer = new RatchetConsumer({ store, core, namespace: ns, admission, directory: async () => ({}),
                deviceKey: async () => null, signBundle: async b => ({ ...b, signature: toBase64(new Uint8Array(64)) }), assertCurrent: () => {} });
            const result = { initialAsked: 0 };
            await consumer.ready();
            result.initialAsked = asked.length;
            closedKey = otks[0].id;
            const obligations = async () => {
                const refs = await store.pendingRefs("obligation", "", 200);
                const s = await store.read(refs);
                return Object.fromEntries(refs.map(ref => { const o = JSON.parse(new TextDecoder().decode(s.records[JSON.stringify(ref)].data)); return [o.key_id, o]; }));
            };
            const realNow = Date.now;
            const again = async () => { consumer.readiness = null; await consumer.ready(); };
            Date.now = () => realNow() + 7 * 86400 * 1000;
            try {
                await again();
                result.firstAsked = asked[0];
                result.retired = [...retired];
                const o1 = await obligations();
                result.closedAfterFirst = otks.map(k => !!o1[k.id].closed);
                result.proofSet = /^[a-f0-9]{64}$/.test(o1[otks[0].id].closure_proof || "");
                await again();
                result.secondAsked = asked[1];
                closedKey = otks[1].id; forge = true;
                await again();
                { const o = await obligations(); result.closedAfterForged = otks.map(k => !!o[k.id].closed); }
                forge = false;
                await again();
                { const o = await obligations(); result.closedAfterHonest = otks.map(k => !!o[k.id].closed); }
            } finally { Date.now = realNow; }
            result.otkIDs = otks.map(k => k.id);
            result.otk0 = otks[0].key;
            // The retired private key is gone from the committed account.
            const s = await store.read([["account", "local"]]);
            const acct = new TextDecoder().decode(s.records[JSON.stringify(["account", "local"])].data);
            try { await real.apply({ op: "retire_prekey", key: store.coreKey(s), account: acct, one_time_key: otks[0].key }); result.stillThere = true; } catch { result.stillThere = false; }
            real.close();
            return result;
        }, entries);
        const ids = out.otkIDs;
        assert.equal(out.initialAsked, 0, "an unexpired publication asks nothing (a timer is never evidence)");
        assert.deepEqual(out.firstAsked, [...ids].sort());
        assert.deepEqual(out.retired, [out.otk0], "only the server-closed key is retired in the core");
        assert.equal(out.stillThere, false, "the committed account no longer holds the retired private key");
        assert.deepEqual(out.closedAfterFirst, [true, false, false]);
        assert.equal(out.proofSet, true);
        assert.deepEqual(out.secondAsked, [ids[1], ids[2]], "closed obligations are not asked again");
        assert.deepEqual(out.closedAfterForged, [true, false, false], "a forged proof commits nothing");
        assert.deepEqual(out.closedAfterHonest, [true, true, false]);
    } finally {
        await browser.close();
        server.close();
    }
});
