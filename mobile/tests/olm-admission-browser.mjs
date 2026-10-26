import assert from "node:assert/strict";
import { test } from "node:test";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chromium } from "@playwright/test";
const root = path.resolve(fileURLToPath(new URL("../", import.meta.url)));
test(
  "browser durable immutable attempts and metadata-only resolution",
  { timeout: 60000 },
  async () => {
    const server = createServer(async (req, res) => {
      try {
        const url = new URL(req.url, "http://localhost").pathname;
        if (url === "/") {
          res.setHeader("Content-Type", "text/html");
          res.end("<!doctype html><title>admission</title>");
          return;
        }
        const f = path.resolve(root, "." + url);
        if (!f.startsWith(root + path.sep)) {
          res.writeHead(404).end();
          return;
        }
        res.setHeader("Content-Type", "application/javascript");
        res.end(await readFile(f));
      } catch {
        res.writeHead(404).end();
      }
    });
    await new Promise((r) => server.listen(0, "127.0.0.1", r));
    let browser;
    try {
      browser = await chromium.launch();
      const page = await browser.newPage();
      await page.goto("http://127.0.0.1:" + server.address().port);
      const outcome = await page.evaluate(async () => {
        const { openRatchetStore } = await import("/src/core/ratchet-store.js"),
          wire = await import("/src/core/olm-admission.js"),
          enc = new TextEncoder();
        const ns = {
            server: "tls-sha256:" + "a".repeat(64),
            principal: "alice",
            device: "a",
          },
          s = await openRatchetStore(ns);
        let snap = await s.read([["trust", "bob"]]);
        await s.setTrust(snap, "bob", ["verified"], false);
        const m = {
          version: wire.ADMISSION_VERSION,
          server: ns.server,
          sender: "alice",
          device: "a",
          account_epoch: 1,
          attempt_id: "1",
          logical_id: "logical",
          purpose: "chat",
          scope: "direct",
          peer: "bob",
          deadline: 1900000000,
          rosters: [
            { principal: "alice", version: 1 },
            { principal: "bob", version: 1 },
          ],
          targets: [
            {
              principal: "alice",
              device: "a",
              account_epoch: 1,
              identity: "a-key",
              local: true,
            },
            {
              principal: "bob",
              device: "b",
              account_epoch: 1,
              identity: "b-key",
            },
          ],
        };
        const envelope = {
            version: "noxa-olm-v1",
            session_id: "session",
            binding: wire.expectedBinding(m, m.targets[1]),
            message: { type: 0, body: "cipher" },
          },
          frozen = await wire.freezeAttempt(m, [
            {
              principal: "bob",
              device: "b",
              account_epoch: 1,
              session_id: "session",
              envelope,
            },
          ]);
        snap = await s.read([
          ["trust", "bob"],
          ["session", "bob"],
          ["logical", "logical"],
        ]);
        const p = {
          sessionID: "bob",
          trustID: "bob",
          state: enc.encode("protected"),
          envelope: wire.jsonBytes(envelope),
          context: {
            local_account_id: "local",
            local_epoch: 1,
            peer_epoch: 1,
            peer_principal: "bob",
            peer_device: "b",
            peer_identity: "b-key",
            conversation: envelope.binding.conversation,
            protocol: "noxa-olm-v1",
          },
        };
        const result = await s.commitAttempt(
          snap,
          frozen.manifest,
          frozen.parts,
          [p],
          enc.encode("readable secret"),
        );
        snap = await s.read([["trust", "bob"]]);
        await s.setTrust(snap, "bob", ["new verified value"], false);
        snap = await s.read();
        let retry;
        try {
          await s.retrySend(snap, "1:1");
        } catch (e) {
          retry = e.code;
        }
        const metadata = await s.attemptStatusRequest(snap, "1:1");
        const receipt = {
          state: "accepted",
          attempt_id: "1",
          digest: await wire.digestBytes(
            wire.validateManifest(frozen.manifest),
          ),
          server: ns.server,
          account_epoch: 1,
          logical_id: "logical",
          message_id: "accepted",
        };
        snap = await s.read([
          ["outbox", "1:1"],
          ["logical", "logical"],
        ]);
        await s.applyAttemptResult(snap, "1:1", receipt);
        const ack = await s.attemptRetirementRequest(await s.read(), "1:1");
        return { retry, metadata, ack, reused: result.reused };
      });
      assert.equal(outcome.retry, "conflict");
      assert.equal(outcome.reused, false);
      assert.equal(outcome.ack.attempt_id, "1");
      assert(!JSON.stringify(outcome.metadata).includes("readable secret"));
      assert(!JSON.stringify(outcome.metadata).includes("cipher"));
    } finally {
      await browser?.close();
      await new Promise((r) => server.close(r));
    }
  },
);
