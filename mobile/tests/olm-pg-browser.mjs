import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "@playwright/test";
const root = fileURLToPath(new URL("../../", import.meta.url));
const bridge = process.env.NOXA_ADMISSION_BRIDGE;
if (!bridge) throw new Error("run from TestIntegrationOlmAdmissionBrowser");
const assets = await readdir(path.join(root, "web/app/assets"));
const entry = (name) => {
  const found = assets.find((f) =>
    new RegExp("^" + name + "-[\\w-]+\\.js$").test(f),
  );
  assert(found, `build missing ${name}`);
  return found;
};
const server = createServer(async (req, res) => {
  try {
    if (req.url === "/bridge") {
      const chunks = [];
      for await (const c of req) chunks.push(c);
      const response = await fetch(bridge, {
        method: "POST",
        body: Buffer.concat(chunks),
      });
      res.setHeader("Content-Type", "application/json");
      res.end(await response.text());
      return;
    }
    const p = new URL(req.url, "http://localhost").pathname;
    if (p === "/") {
      res.setHeader("Content-Type", "text/html");
      res.end("<!doctype html><title>Olm PostgreSQL interoperability</title>");
      return;
    }
    const file = path.resolve(root, "web", p.replace(/^\/app\//, "app/"));
    if (!file.startsWith(path.join(root, "web") + path.sep)) {
      res.writeHead(404).end();
      return;
    }
    res.setHeader(
      "Content-Type",
      file.endsWith(".js")
        ? "application/javascript"
        : file.endsWith(".wasm")
          ? "application/wasm"
          : "text/html",
    );
    res.end(await readFile(file));
  } catch (e) {
    res.writeHead(500).end(String(e));
  }
});
await new Promise((r) => server.listen(0, "127.0.0.1", r));
let browser;
try {
  browser = await chromium.launch();
  const page = await browser.newPage();
  await page.goto(`http://127.0.0.1:${server.address().port}/`);
  const result = await page.evaluate(
    async (entries) => {
      const { createRatchetCore } = await import(`/app/assets/${entries.core}`),
        { openRatchetStore } = await import(`/app/assets/${entries.store}`),
        wire = await import(`/app/assets/${entries.wire}`),
        dir = await import(`/app/assets/${entries.directory}`);
      const rpc = async (action, data = {}) => {
        const r = await (
          await fetch("/bridge", {
            method: "POST",
            body: JSON.stringify({ action, ...data }),
          })
        ).json();
        if (r?.error) throw new Error(r.error);
        return r;
      };
      const enc = new TextEncoder(),
        dec = new TextDecoder(),
        bytes = (v) => wire.jsonBytes(v),
        b64 = (b) => btoa(String.fromCharCode(...b)),
        un64 = (s) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
      // Namespace is determined by the authenticated synthetic test identity.
      const ns = await rpc("identity"),
        s = await openRatchetStore(ns);
      const core = await createRatchetCore(),
        temporaryKey = s.coreKey(await s.read());
      const account = await core.apply({
        op: "new_account",
        key: temporaryKey,
        count: 1,
      });
      const fixture = await rpc("setup", { bundle: account.bundle }),
        bob = fixture.browser,
        alice = fixture.native;
      let snap = await s.read([["account", "local"]]);
      await s.commitPublication(
        snap,
        "pub",
        enc.encode(account.account),
        bytes(bob),
        {
          [bob.one_time_keys[0].id]: {
            publication_id: "pub",
            expires_at: bob.expires_at,
            key_id: bob.one_time_keys[0].id,
            public_key: bob.one_time_keys[0].key,
          },
        },
      );
      await rpc("browser_publish");
      snap = await s.read([["trust", "native"]]);
      await s.setTrust(snap, "native", bytes(alice), false);
      const response = await rpc("native_send"),
        delivery = response.data.deliveries[0];
      if (!delivery) throw new Error("no admitted delivery");
      dir.verifyBundleEvidence(delivery.source_bundle, {
        trustedSigningKey: un64(fixture.native_signing),
        server: alice.server,
        principal: alice.principal,
        deviceID: alice.device_id,
        accountEpoch: 1,
        pinnedOlmIdentity: alice.identity,
      });
      wire.checkEnvelope(delivery.manifest, delivery.copy);
      const context = {
        local_account_id: "local",
        local_epoch: 1,
        peer_epoch: 1,
        peer_principal: alice.principal,
        peer_device: alice.device_id,
        peer_identity: alice.identity,
        conversation: wire.expectedBinding(delivery.manifest, delivery.copy)
          .conversation,
        protocol: "noxa-olm-v1",
      };
      const binding = wire.expectedBinding(delivery.manifest, delivery.copy),
        bad = structuredClone(delivery.copy.envelope);
      bad.binding.message_id = "tampered";
      let failed = false;
      try {
        await core.apply({
          op: "inbound",
          key: temporaryKey,
          account: account.account,
          peer_identity: alice.identity,
          binding: bad.binding,
          envelope: bad,
        });
      } catch {
        failed = true;
      }
      if (!failed) throw new Error("tampered initial accepted");
      const incoming = await core.apply({
        op: "inbound",
        key: temporaryKey,
        account: account.account,
        peer_identity: alice.identity,
        binding,
        envelope: delivery.copy.envelope,
      });
      const plain = wire.openPayload(
        delivery.manifest,
        delivery.copy,
        incoming.plaintext,
      );
      if (plain.text !== "native to browser")
        throw new Error("wrong native plaintext");
      snap = await s.read([
        ["trust", "native"],
        ["session", "native"],
        ["account", "local"],
        ["obligation", bob.one_time_keys[0].id],
      ]);
      const candidate = {
        sessionID: "native",
        trustID: "native",
        session: enc.encode(incoming.session),
        account: enc.encode(incoming.account),
        archive: bytes(plain),
        envelope: bytes(delivery.copy.envelope),
        consumedOTK: bob.one_time_keys[0].id,
        context,
      };
      await s.commitReceive(snap, delivery.id, candidate);
      const duplicate = await s.commitReceive(snap, delivery.id, candidate);
      if (dec.decode(duplicate) !== JSON.stringify(plain))
        throw new Error("duplicate archive changed");
      await rpc("browser_request", {
        request: {
          action: "receive_ack",
          delivery_id: delivery.id,
          digest: delivery.digest,
        },
      });
      const roster = (
        await rpc("browser_request", {
          request: {
            action: "snapshot",
            scope: "direct",
            peer: alice.principal,
          },
        })
      ).data;
      const manifest = {
        version: wire.ADMISSION_VERSION,
        server: bob.server,
        sender: bob.principal,
        device: bob.device_id,
        account_epoch: 1,
        attempt_id: "1",
        logical_id: "browser-message",
        purpose: "chat",
        scope: "direct",
        peer: alice.principal,
        deadline: Math.floor(Date.now() / 1000) + 300,
        rosters: roster.rosters,
        targets: roster.targets,
      };
      const target = manifest.targets.find((t) => t.device === alice.device_id);
      const reply = await core.apply({
        op: "encrypt",
        key: temporaryKey,
        session: incoming.session,
        binding: wire.expectedBinding(manifest, target),
        plaintext: wire.sealPayload(manifest, target, {
          text: "browser to native",
        }),
      });
      const frozen = await wire.freezeAttempt(manifest, [
        {
          principal: alice.principal,
          device: alice.device_id,
          account_epoch: 1,
          session_id: reply.session_id,
          envelope: reply.envelope,
        },
      ]);
      snap = await s.read([
        ["trust", "native"],
        ["session", "native"],
        ["logical", manifest.logical_id],
      ]);
      await s.commitAttempt(
        snap,
        frozen.manifest,
        frozen.parts,
        [
          {
            sessionID: "native",
            trustID: "native",
            state: enc.encode(reply.session),
            envelope: bytes(reply.envelope),
            context,
          },
        ],
        bytes({ text: "browser to native" }),
      );
      const send = await s.attemptWire(await s.read(), "1:1");
      if (JSON.stringify(send).includes("browser to native"))
        throw new Error("readable archive leaked to wire");
      await rpc("browser_request", {
        request: { action: "begin", manifest: frozen.manifest },
      });
      for (let i = 0; i < frozen.parts.length; i++)
        await rpc("browser_request", {
          request: {
            action: "put_part",
            attempt_id: "1",
            part: i,
            data: b64(frozen.parts[i]),
          },
        });
      const accepted = (
        await rpc("browser_request", {
          request: { action: "finalize", attempt_id: "1" },
        })
      ).data;
      snap = await s.read([
        ["outbox", "1:1"],
        ["logical", manifest.logical_id],
      ]);
      await s.applyAttemptResult(snap, "1:1", accepted);
      const ack = await s.attemptRetirementRequest(await s.read(), "1:1");
      await rpc("browser_request", {
        request: { action: "retire_receipt", ...ack },
      });
      const got = await rpc("native_receive");
      const token = await s.accountAuthority(await s.read()),
        freshAccount = await core.apply({
          op: "new_account",
          key: temporaryKey,
          count: 1,
        }),
        transition = await rpc("prepare_reset", {
          bundle: freshAccount.bundle,
        });
      const resetAPI = await import(`/app/assets/${entries.reset}`);
      resetAPI.verifyReset(transition.reset, {
        server: bob.server,
        principal: bob.principal,
        device: bob.device_id,
        epoch: 1,
        identity: bob.identity,
        signing: bob.signing,
        noxaSigning: un64(fixture.browser_signing),
      });
      const newKey = transition.bundle.one_time_keys[0];
      await s.prepareAccountTransition(
        await s.read(),
        "epoch-2",
        enc.encode(freshAccount.account),
        transition.reset,
        transition.bundle,
        {
          ["epoch-2:" + newKey.id]: {
            account_id: "epoch-2",
            account_epoch: 2,
            publication_id: transition.reset.transition_id,
            key_id: newKey.id,
            public_key: newKey.key,
            expires_at: transition.bundle.expires_at,
          },
        },
      );
      let fenced = false;
      try {
        await s.checkAccountAuthority(await s.read(), token);
      } catch (e) {
        fenced = e.code === "fenced";
      }
      if (!fenced) throw new Error("old active account not fenced");
      const resetReceipt = (
        await rpc("browser_request", {
          request: {
            action: "reset",
            account_epoch: 1,
            reset: transition.reset,
            bundle: transition.bundle,
          },
        })
      ).data;
      snap = await s.read([["transition", transition.reset.transition_id]]);
      await s.promoteAccount(
        snap,
        transition.reset.transition_id,
        resetReceipt,
      );
      const current = await s.accountAuthority(await s.read());
      if (
        current.epoch !== 2 ||
        current.blocked ||
        current.accountID !== "epoch-2"
      )
        throw new Error("reset not promoted");
      const metadata = await s.attemptStatusRequest(await s.read(), "1:1");
      if (metadata.receipt.state !== "accepted")
        throw new Error("reset removed historical receipt");
      core.close();
      return { native: plain.text, browser: got.text, tamperRejected: failed };
    },
    {
      core: entry("ratchet"),
      store: entry("ratchetStorage"),
      wire: entry("admission"),
      directory: entry("directory"),
      reset: entry("accountReset"),
    },
  );
  assert.deepEqual(result, {
    native: "native to browser",
    browser: "browser to native",
    tamperRejected: true,
  });
  console.log(
    "PASS real PostgreSQL framed handler + native bbolt + packaged Chromium IndexedDB interoperability",
  );
} finally {
  await browser?.close();
  await new Promise((r) => server.close(r));
}
