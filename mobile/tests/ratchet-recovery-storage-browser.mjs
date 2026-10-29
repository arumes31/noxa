import assert from "node:assert/strict";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { chromium } from "@playwright/test";

test("abandoned recovery returns candidate quota and preserves receive keys", { timeout: 60000 }, async () => {
  // Serve source modules so this exercises the actual IndexedDB store without
  // relying on a stale application build or an in-memory transaction mock.
  const server = await createServer({
    configFile: false, root: fileURLToPath(new URL("../", import.meta.url)),
    logLevel: "error", server: { host: "127.0.0.1", port: 0 },
  });
  server.middlewares.use((req, res, next) => {
    if (req.url !== "/__recovery-test") return next();
    res.setHeader("Content-Type", "text/html");
    res.end("<!doctype html><title>recovery storage</title>");
  });
  await server.listen();
  let browser;
  try {
    browser = await chromium.launch();
    const page = await browser.newPage();
    await page.goto(`http://127.0.0.1:${server.httpServer.address().port}/__recovery-test`);
    const result = await page.evaluate(async () => {
      const { openRatchetStore } = await import("/src/core/ratchet-store.js");
      const { generateIdentity, toBase64 } = await import("/src/core/crypto.js");
      const { signBundle } = await import("/src/core/olm-directory.js");
      const { signReset, publicationDigest } = await import("/src/core/olm-reset.js");
      const { resetDigest } = await import("/src/core/olm-admission.js");
      const enc = new TextEncoder(), dec = new TextDecoder(), key = JSON.stringify;
      const check = (ok, message) => { if (!ok) throw new Error(message); };
      const parsed = (snapshot, ref) => JSON.parse(dec.decode(snapshot.records[key(ref)].data));
      const same = (a, b) => a.revision === b.revision && key(Array.from(a.data)) === key(Array.from(b.data));
      const ns = { server: "tls-sha256:" + "c".repeat(64), principal: "alice", device: "device" };
      const store = await openRatchetStore(ns), root = generateIdentity(), old = generateIdentity();
      const raw = value => toBase64(value).replace(/=+$/, "");
      const oldOTKs = Object.fromEntries(Array.from({ length: 20 }, (_, i) => ["old-" + i, { publication_id: "old", key_id: String(i), public_key: "old-key-" + i }]));
      const oldRefs = [["account", "local"], ["publication", "old"], ...Object.keys(oldOTKs).map(id => ["obligation", id])];
      await store.commitPublication(await store.read([["account", "local"]]), "old", enc.encode("old account"), enc.encode("old publication"), oldOTKs);
      const preserved = await store.read(oldRefs);
      let abandoned = 0;
      for (let cycle = 0; cycle < 9; cycle++) {
        const accountID = "candidate-" + cycle, id = "recovery-" + cycle, candidate = generateIdentity();
        const bundle = signBundle({
          version: "noxa-olm-directory-v1", protocol: "noxa-olm-v1", server: ns.server, principal: ns.principal, device_id: ns.device,
          account_epoch: 4, generation: 8, issued_at: 1900000000, expires_at: 1900003600,
          identity: raw(candidate.xPublic), signing: raw(candidate.edPublic),
          one_time_keys: Array.from({ length: 20 }, (_, i) => ({ id: raw(Uint8Array.of(0, 0, 0, 0, 0, 0, 0, i + 1)), key: raw(generateIdentity().xPublic) })),
        }, root.edSecret);
        const reset = signReset({
          version: "noxa-olm-reset-v1", server: ns.server, principal: ns.principal, device: ns.device, transition_id: id,
          old_epoch: 3, old_identity: raw(old.xPublic), old_signing: raw(old.edPublic), new_epoch: 4,
          new_identity: bundle.identity, new_signing: bundle.signing, generation: 8,
          publication_digest: await publicationDigest(bundle), reason: "restored",
        }, root.edSecret);
        // Obligation IDs are opaque, not necessarily accountID:keyID.
        const otks = Object.fromEntries(bundle.one_time_keys.map((k, i) => [`opaque-${cycle}-${i}`, {
          account_id: accountID, account_epoch: 4, publication_id: id, key_id: k.id, public_key: k.key,
        }]));
        const refs = [["transition", id], ["account", accountID], ...Object.keys(otks).map(k => ["obligation", k])];
        const account = enc.encode("candidate account " + cycle);
        await store.prepareRecoveryTransition(await store.read([]), accountID, account, reset, bundle, otks);
        const pending = await store.read(refs);
        for (const oid of Object.keys(otks)) {
          const o = parsed(pending, ["obligation", oid]);
          check(!o.closed && !o.consumed, "uncertain recovery lost a key");
        }
        if (cycle === 8) {
          const receipt = { server: ns.server, device: ns.device, transition_id: id, account_epoch: 4,
            digest: await resetDigest(reset), publication_digest: reset.publication_digest };
          await store.promoteAccount(pending, id, receipt);
          let rejected;
          try { await store.abandonRecoveryTransition(await store.read(refs), id); } catch (e) { rejected = e.code; }
          check(rejected === "invalid", "applied recovery was abandoned");
          const applied = await store.read(refs);
          for (const oid of Object.keys(otks)) check(same(pending.records[key(["obligation", oid])], applied.records[key(["obligation", oid])]), "applied key changed");
        } else {
          // Call only after the consumer establishes the reset was not applied.
          await store.abandonRecoveryTransition(pending, id);
          const closed = await store.read(refs);
          for (const oid of Object.keys(otks)) {
            const o = parsed(closed, ["obligation", oid]);
            check(o.closed && !o.consumed && o.closure_proof === "abandoned-recovery:" + id, "candidate key not closed");
          }
          await store.abandonRecoveryTransition(closed, id);
          const repeated = await store.read(refs);
          for (const r of refs) check(same(closed.records[key(r)], repeated.records[key(r)]), "abandonment not idempotent");
          check(dec.decode(closed.records[key(["account", accountID])].data) === dec.decode(account), "candidate account erased");
          abandoned++;
        }
        const retained = await store.read(oldRefs);
        for (const r of oldRefs) check(same(preserved.records[key(r)], retained.records[key(r)]), "old receive state changed");
      }
      const ctl = parsed(await store.read([]), ["control", "identity"]);
      return { abandoned, account: ctl.accountID, epoch: ctl.accountEpoch, blocked: ctl.sendingBlocked, outstanding: ctl.obligations };
    });
    assert.deepEqual(result, { abandoned: 8, account: "candidate-8", epoch: 4, blocked: false, outstanding: 40 });
  } finally {
    await browser?.close();
    await server.close();
  }
});
