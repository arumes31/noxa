import assert from "node:assert/strict";
import { test } from "node:test";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chromium } from "@playwright/test";
const root = fileURLToPath(new URL("../", import.meta.url));
test(
  "real IndexedDB wrapping-key winner and identity logout fence",
  { timeout: 60000 },
  async () => {
    const server = createServer(async (req, res) => {
      try {
        const p = new URL(req.url, "http://localhost").pathname;
        if (p === "/") {
          res.setHeader("Content-Type", "text/html");
          res.end("<!doctype html><title>durability test</title>");
          return;
        }
        const f =
          p === "/internal/e2ee/ratchet/core.wasm"
            ? path.resolve(root, "../internal/e2ee/ratchet/core.wasm")
            : path.resolve(root, "." + p);
        if (!f.startsWith(root) && p !== "/internal/e2ee/ratchet/core.wasm") {
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
      const ctx = await browser.newContext();
      const pages = await Promise.all([ctx.newPage(), ctx.newPage()]);
      await Promise.all(
        pages.map((p) => p.goto(`http://127.0.0.1:${server.address().port}`)),
      );
      // Upgrade a real preexisting version 7 database without rewriting its data.
      await pages[0].evaluate(
        () =>
          new Promise((resolve, reject) => {
            const req = indexedDB.open("noxa", 7);
            req.onupgradeneeded = () => {
              for (const name of [
                "device",
                "dm",
                "pins",
                "outbox",
                "saved",
                "activity",
                "calls",
                "share",
              ])
                req.result.createObjectStore(name);
            };
            req.onsuccess = () => {
              const db = req.result,
                tx = db.transaction("device", "readwrite");
              tx.objectStore("device").put(
                { legacy: "preserved" },
                "old-message",
              );
              tx.oncomplete = () => {
                db.close();
                resolve();
              };
              tx.onabort = () => reject(tx.error);
            };
            req.onerror = () => reject(req.error);
          }),
      );
      // Hold both first-tab generateKey operations until both observed no key.
      await Promise.all(
        pages.map((p) =>
          p.evaluate(async () => {
            globalThis.dbmod = await import("/src/core/db.js");
            globalThis.idmod = await import("/src/core/identity-store.js");
            const original = crypto.subtle.generateKey.bind(crypto.subtle);
            crypto.subtle.generateKey = async (...a) => {
              globalThis.generating = true;
              await new Promise((r) => (globalThis.releaseKey = r));
              crypto.subtle.generateKey = original;
              return original(...a);
            };
            globalThis.pending = (async () => {
              const db = await dbmod.openDB();
              try {
                return await dbmod.seal(
                  db,
                  new TextEncoder().encode("protected"),
                );
              } finally {
                db.close();
              }
            })();
          }),
        ),
      );
      await Promise.all(
        pages.map((p) => p.waitForFunction(() => globalThis.generating)),
      );
      await Promise.all(pages.map((p) => p.evaluate(() => releaseKey())));
      const records = await Promise.all(
        pages.map((p) =>
          p.evaluate(async () => ({
            iv: Array.from((await pending).iv),
            sealed: Array.from((await pending).sealed),
          })),
        ),
      );
      for (const r of records)
        assert.equal(
          await pages[0].evaluate(async (r) => {
            const db = await dbmod.openDB();
            try {
              return new TextDecoder().decode(
                await dbmod.unseal(db, {
                  iv: new Uint8Array(r.iv),
                  sealed: new Uint8Array(r.sealed),
                }),
              );
            } finally {
              db.close();
            }
          }, r),
          "protected",
        );
      assert.equal(
        await pages[0].evaluate(async () => {
          const db = await dbmod.openDB();
          try {
            return (
              await dbmod.run(db, dbmod.Stores.device, "readonly", (s) =>
                s.get("old-message"),
              )
            ).legacy;
          } finally {
            db.close();
          }
        }),
        "preserved",
      );
      // A save paused in WebCrypto must not resurrect a device cleared by another tab.
      await pages[0].evaluate(async () => {
        globalThis.identity = {
          edPublic: new Uint8Array(32).fill(1),
          edSecret: new Uint8Array(64).fill(2),
          xPublic: new Uint8Array(32).fill(3),
          xSecret: new Uint8Array(32).fill(4),
        };
        const original = crypto.subtle.encrypt.bind(crypto.subtle);
        globalThis.restoreEncrypt = () => {
          crypto.subtle.encrypt = original;
        };
        crypto.subtle.encrypt = async (...args) => {
          globalThis.encrypting = true;
          await new Promise((r) => (globalThis.releaseEncrypt = r));
          return original(...args);
        };
        globalThis.saving = idmod
          .saveIdentity(identity, { nickname: "before logout" })
          .then(
            () => ({ ok: true }),
            (e) => ({ code: e.code }),
          );
      });
      await pages[0].waitForFunction(() => globalThis.encrypting);
      await pages[1].evaluate(() => idmod.clearIdentity());
      await pages[0].evaluate(() => releaseEncrypt());
      assert.equal((await pages[0].evaluate(() => saving)).code, "fenced");
      assert.equal(await pages[1].evaluate(() => idmod.loadIdentity()), null);
      await pages[0].evaluate(async () => {
        restoreEncrypt();
        await idmod.saveIdentity(
          identity,
          { nickname: "onboarded" },
          { onboarding: true, fence: await idmod.beginIdentity() },
        );
      });
      assert.equal(
        (await pages[1].evaluate(() => idmod.loadIdentity())).meta.nickname,
        "onboarded",
      );
      // updateMeta is fenced across its asynchronous read, without decrypting/resealing identity.
      await pages[0].evaluate(() => {
        const descriptor = Object.getOwnPropertyDescriptor(
          IDBTransaction.prototype,
          "oncomplete",
        );
        globalThis.restoreComplete = () =>
          Object.defineProperty(
            IDBTransaction.prototype,
            "oncomplete",
            descriptor,
          );
        let held = false;
        Object.defineProperty(IDBTransaction.prototype, "oncomplete", {
          ...descriptor,
          set(fn) {
            const tx = this;
            if (!held && tx.mode === "readonly") {
              held = true;
              descriptor.set.call(tx, (event) => {
                globalThis.metaWaiting = true;
                globalThis.releaseMeta = () => fn.call(tx, event);
              });
            } else descriptor.set.call(tx, fn);
          },
        });
        globalThis.metaPending = idmod.updateMeta({ nickname: "stale" }).then(
          () => ({ ok: true }),
          (e) => ({ code: e.code }),
        );
      });
      await pages[0].waitForFunction(() => globalThis.metaWaiting);
      await pages[1].evaluate(() => idmod.clearIdentity());
      await pages[0].evaluate(() => releaseMeta());
      assert.equal((await pages[0].evaluate(() => metaPending)).code, "fenced");
      await pages[0].evaluate(() => restoreComplete());
      assert.equal(await pages[1].evaluate(() => idmod.loadIdentity()), null);
      // Explicit new onboarding is allowed with a fresh fence, old token is rejected.
      const oldToken = await pages[0].evaluate(() => idmod.beginIdentity());
      await pages[1].evaluate(() => idmod.clearIdentity());
      assert.equal(
        await pages[0].evaluate(async (fence) => {
          try {
            await idmod.saveIdentity(identity, {}, { onboarding: true, fence });
            return "bad";
          } catch (e) {
            return e.code;
          }
        }, oldToken),
        "fenced",
      );
      await pages[0].evaluate(async () =>
        idmod.saveIdentity(
          identity,
          { nickname: "ready" },
          { onboarding: true, fence: await idmod.beginIdentity() },
        ),
      );
      const firstStores = await Promise.all(
        pages.map((page) =>
          page.evaluate(async () => {
            const m = await import("/src/core/ratchet-store.js");
            const s = await m.openRatchetStore({
              server: "parallel",
              principal: "alice",
              device: "shared",
            });
            return Array.from(s.coreKey(await s.read()));
          }),
        ),
      );
      assert.deepEqual(firstStores[0], firstStores[1]);
      // Real core snapshots, account publication, and authenticated receive candidates.
      for (const page of pages)
        await page.evaluate(async () => {
          globalThis.storage = await import("/src/core/ratchet-store.js");
          globalThis.core = await (
            await import("/src/core/ratchet-core.js")
          ).createRatchetCore();
          globalThis.a = await storage.openRatchetStore({
            server: "tls-bound-server",
            principal: "alice",
            device: "alice-device",
          });
          globalThis.b = await storage.openRatchetStore({
            server: "tls-bound-server",
            principal: "bob",
            device: "bob-device",
          });
          globalThis.enc = new TextEncoder();
          globalThis.dec = new TextDecoder();
          globalThis.k = (r) => JSON.stringify(r);
          globalThis.j = (v) => JSON.parse(dec.decode(v));
          globalThis.e = (v) => enc.encode(JSON.stringify(v));
          globalThis.context = {
            local_account_id:"local",local_epoch:1,peer_epoch:1,
            peer_principal: "bob",
            peer_device: "bob-device",
            peer_identity: "verified-olm-bob",
            conversation: "alice:bob",
            protocol: "noxa-olm-v1",
          };
        });
      await pages[0].evaluate(async () => {
        await a.setTrust(
          await a.read([["trust", "bob"]]),
          "bob",
          "verified identity",
        );
        await b.setTrust(
          await b.read([["trust", "alice"]]),
          "alice",
          "verified identity",
        );
        const sa = await a.read([["account", "local"]]),
          sb = await b.read([["account", "local"]]);
        globalThis.alice = await core.apply({
          op: "new_account",
          key: a.coreKey(sa),
          count: 1,
        });
        globalThis.bob = await core.apply({
          op: "new_account",
          key: b.coreKey(sb),
          count: 1,
        });
        await a.commitPublication(
          sa,
          "pub-a",
          enc.encode(alice.account),
          enc.encode("exact signed publication a"),
          {
            [alice.bundle.one_time_keys[0].id]: {
              publication_id: "pub-a",
              expires_at: 1,
            },
          },
        );
        await b.commitPublication(
          sb,
          "pub-b",
          enc.encode(bob.account),
          enc.encode("exact signed publication b"),
          {
            [bob.bundle.one_time_keys[0].id]: {
              publication_id: "pub-b",
              expires_at: 1,
            },
          },
        );
        globalThis.binding = {
          server: "tls-bound-server",
          conversation: "alice:bob",
          sender: "alice",
          sender_device: "alice-device",
          recipient: "bob",
          recipient_device: "bob-device",
          message_id: "initial",
          purpose: "message",
        };
        globalThis.sendSnap = await a.read([
          ["trust", "bob"],
          ["session", "bob"],
        ]);
        globalThis.first = await core.apply({
          op: "outbound",
          key: a.coreKey(sendSnap),
          account: alice.account,
          peer_identity: bob.bundle.identity,
          one_time_key: bob.bundle.one_time_keys[0].key,
          binding,
          plaintext: "original readable message",
        });
        globalThis.packet = {
          sessionID: "bob",
          trustID: "bob",
          state: enc.encode(first.session),
          envelope: e(first.envelope),
          context,
        };
        globalThis.sent = await a.commitSend(
          sendSnap,
          "initial",
          [packet],
          enc.encode("original readable message"),
        );
      });
      const retry = await pages[0].evaluate(async () => {
        const r = await a.commitSend(
          sendSnap,
          "initial",
          [
            {
              ...packet,
              envelope: enc.encode("different new plaintext ciphertext"),
            },
          ],
          enc.encode("changed draft"),
        );
        return {
          reused: r.reused,
          same: dec.decode(r.data) === dec.decode(sent.data),
          archive: dec.decode(new Uint8Array(j(r.data).archive)),
        };
      });
      assert.deepEqual(retry, {
        reused: true,
        same: true,
        archive: "original readable message",
      });
      await pages[0].evaluate(async () => {
        globalThis.receiveSnap = await b.read([
          ["account", "local"],
          ["session", "alice"],
          ["trust", "alice"],
          ["obligation", bob.bundle.one_time_keys[0].id],
        ]);
        globalThis.received = await core.apply({
          op: "inbound",
          key: b.coreKey(receiveSnap),
          account: bob.account,
          peer_identity: alice.bundle.identity,
          binding,
          envelope: first.envelope,
        });
        globalThis.receive = {
          sessionID: "alice",
          trustID: "alice",
          session: enc.encode(received.session),
          account: enc.encode(received.account),
          archive: enc.encode(received.plaintext),
          envelope: e(first.envelope),
          consumedOTK: bob.bundle.one_time_keys[0].id,
          context: {
            ...context,
            peer_principal: "alice",
            peer_device: "alice-device",
            peer_identity: "verified-olm-alice",
          },
        };
        globalThis.archive = await b.commitReceive(
          receiveSnap,
          "initial",
          receive,
        );
      });
      assert.equal(
        await pages[0].evaluate(async () =>
          dec.decode(
            await b.commitReceive(receiveSnap, "initial", {
              ...receive,
              archive: enc.encode("wrong replacement"),
            }),
          ),
        ),
        "original readable message",
      );
      // Two pages derive candidates from the same real stored ratchet revision.
      await Promise.all(
        pages.map((page, i) =>
          page.evaluate(async (i) => {
            globalThis.raceSnap = await a.read([
              ["session", "bob"],
              ["trust", "bob"],
            ]);
            const state = j(raceSnap.records[k(["session", "bob"])].data);
            const binding = {
              server: "tls-bound-server",
              conversation: "alice:bob",
              sender: "alice",
              sender_device: "alice-device",
              recipient: "bob",
              recipient_device: "bob-device",
              message_id: `race-${i}`,
              purpose: "message",
            };
            const next = await core.apply({
              op: "encrypt",
              key: a.coreKey(raceSnap),
              session: dec.decode(new Uint8Array(state.snapshot)),
              binding,
              plaintext: `race text ${i}`,
            });
            globalThis.racePacket = {
              sessionID: "bob",
              trustID: "bob",
              state: enc.encode(next.session),
              envelope: e(next.envelope),
              context,
            };
          }, i),
        ),
      );
      const races = await Promise.all(
        pages.map((page, i) =>
          page.evaluate(async (i) => {
            try {
              await a.commitSend(
                raceSnap,
                `race-${i}`,
                [racePacket],
                enc.encode(`race text ${i}`),
              );
              return "committed";
            } catch (e) {
              return e.code;
            }
          }, i),
        ),
      );
      assert.equal(races.filter((x) => x === "committed").length, 1);
      assert.equal(races.filter((x) => x === "conflict").length, 1);
      // Abort the real multi-record transaction after its first put; neither record survives.
      assert.equal(
        await pages[0].evaluate(async () => {
          const snap = await a.read([
            ["session", "bob"],
            ["trust", "bob"],
          ]);
          const original = IDBObjectStore.prototype.put;
          let puts = 0;
          IDBObjectStore.prototype.put = function (...args) {
            const result = original.apply(this, args);
            if (this.name === "ratchet" && ++puts === 1)
              this.transaction.abort();
            return result;
          };
          try {
            await a.commitSend(
              snap,
              "aborted",
              [racePacket],
              enc.encode("must never publish"),
            );
            return "bad";
          } catch {
            return "aborted";
          } finally {
            IDBObjectStore.prototype.put = original;
          }
        }),
        "aborted",
      );
      assert.equal(
        await pages[1].evaluate(
          async () =>
            (await a.read([["outbox", "aborted"]])).records[
              k(["outbox", "aborted"])
            ].revision,
        ),
        0,
      );
      // Trust updates fence both candidate commits and previously saved retry packets.
      await pages[0].evaluate(async () => {
        await a.setTrust(
          await a.read([["trust", "bob"]]),
          "bob",
          "revoked",
          true,
        );
      });
      assert.equal(
        await pages[0].evaluate(async () => {
          try {
            await a.commitSend(
              sendSnap,
              "initial",
              [packet],
              enc.encode("original"),
            );
            return "bad";
          } catch (e) {
            return e.code;
          }
        }),
        "revoked",
      );
      // Logout during awaited ratchet encryption cannot recreate any ratchet record.
      await pages[0].evaluate(async () => {
        globalThis.logoutSnap = await b.read([["trust", "alice"]]);
        const original = crypto.subtle.encrypt.bind(crypto.subtle);
        crypto.subtle.encrypt = async (...args) => {
          globalThis.ratchetEncrypting = true;
          await new Promise((r) => (globalThis.releaseRatchet = r));
          return original(...args);
        };
        globalThis.undoEncrypt = () => {
          crypto.subtle.encrypt = original;
        };
        globalThis.logoutWork = b
          .reserveClaim(
            logoutSnap,
            "late",
            "alice",
            enc.encode("fixed claim request"),
          )
          .then(
            () => "bad",
            (e) => e.code,
          );
      });
      await pages[0].waitForFunction(() => globalThis.ratchetEncrypting);
      await pages[1].evaluate(() => idmod.clearIdentity());
      await pages[0].evaluate(() => releaseRatchet());
      // The control record encryption is a second asynchronous operation.
      await pages[0].evaluate(() => {
        undoEncrypt();
        releaseRatchet();
      });
      assert.equal(await pages[0].evaluate(() => logoutWork), "fenced");
      assert.equal(
        await pages[1].evaluate(async () => {
          const db = await dbmod.openDB();
          try {
            return (
              await dbmod.run(db, dbmod.Stores.ratchet, "readonly", (s) =>
                s.getAllKeys(),
              )
            ).length;
          } finally {
            db.close();
          }
        }),
        0,
      );
      await pages[0].evaluate(async () => {
        await idmod.saveIdentity(
          identity,
          {},
          { onboarding: true, fence: await idmod.beginIdentity() },
        );
        const s = await storage.openRatchetStore({
          server: "corruption",
          principal: "a",
          device: "d",
        });
        await s.setTrust(await s.read([["trust", "peer"]]), "peer", "verified");
        const db = await dbmod.openDB();
        try {
          const ref = s.dbkey(["trust", "peer"]);
          const raw = await dbmod.run(
            db,
            dbmod.Stores.ratchet,
            "readonly",
            (st) => st.get(ref),
          );
          raw.sealed[raw.sealed.length - 1] ^= 1;
          await dbmod.run(db, dbmod.Stores.ratchet, "readwrite", (st) =>
            st.put(raw, ref),
          );
        } finally {
          db.close();
        }
        globalThis.corruptStore = s;
      });
      assert.equal(
        await pages[0].evaluate(async () => {
          try {
            await corruptStore.read([["trust", "peer"]]);
            return "bad";
          } catch (e) {
            return e.code;
          }
        }),
        "corrupt",
      );
      assert.equal(
        await pages[0].evaluate(async () => {
          const db = await dbmod.openDB();
          try {
            await dbmod.run(db, dbmod.Stores.ratchet, "readwrite", (st) =>
              st.delete(
                IDBKeyRange.bound(
                  [corruptStore.namespace],
                  [corruptStore.namespace, []],
                ),
              ),
            );
          } finally {
            db.close();
          }
          try {
            await storage.openRatchetStore({
              server: "corruption",
              principal: "a",
              device: "d",
            });
            return "bad";
          } catch (e) {
            return e.code;
          }
        }),
        "corrupt",
      );
      assert.equal(
        await pages[0].evaluate(async () => {
          const db = await dbmod.openDB();
          try {
            await dbmod.run(db, dbmod.Stores.device, "readwrite", (s) =>
              s.delete("wrapping-key"),
            );
            let rejected = false;
            try {
              await dbmod.seal(db, new Uint8Array([1]));
            } catch {
              rejected = true;
            }
            return (
              rejected &&
              !(await dbmod.run(db, dbmod.Stores.device, "readonly", (s) =>
                s.get("wrapping-key"),
              ))
            );
          } finally {
            db.close();
          }
        }),
        true,
      );
      for (const page of pages) await page.evaluate(() => core.close());
    } finally {
      await browser?.close();
      await new Promise((r) => server.close(r));
    }
  },
);
