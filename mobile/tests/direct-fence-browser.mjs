import assert from "node:assert/strict";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { chromium } from "@playwright/test";

test("real two-page IndexedDB fences retired DM sends, trust, imports and deletes", { timeout: 60000 }, async () => {
    const vite = await createServer({ configFile: false, root: fileURLToPath(new URL("../", import.meta.url)), server: { host: "127.0.0.1", port: 0 } });
    await vite.listen();
    const browser = await chromium.launch();
    try {
        const context = await browser.newContext();
        const pages = await Promise.all([context.newPage(), context.newPage()]);
        for (const page of pages) {
            await page.route("**/__fence__", route => route.fulfill({ contentType: "text/html", body: "<!doctype html><title>DM ownership test</title>" }));
            await page.goto(`http://127.0.0.1:${vite.httpServer.address().port}/__fence__`);
            await page.evaluate(async () => {
                window.dbmod = await import("/src/core/db.js");
                window.idmod = await import("/src/core/identity-store.js");
                window.cryptoMod = await import("/src/core/crypto.js");
                window.DirectMessages = (await import("/src/core/direct.js")).DirectMessages;
                window.createDM = async () => {
                    const s = Object.assign(new EventTarget(), { conn: { open: true, send() {} }, identity: cryptoMod.generateIdentity(), uniqueID: "old", deviceID: "old-device", nickname: "Old" });
                    window.dm = new DirectMessages(s, {}, { fence: await dbmod.currentFence() });
                    await dm.ready;
                };
            });
        }
        const [old, replacement] = pages;
        const replace = () => replacement.evaluate(async () => {
            await idmod.saveIdentity(cryptoMod.generateIdentity(), {}, { onboarding: true, replaceAccount: true, fence: await dbmod.currentFence() });
            await dbmod.putSealed(dbmod.Stores.dm, "peer", { peer: "peer", messages: [{ id: "new", body: "replacement private history" }] });
            await dbmod.putSealed(dbmod.Stores.pins, "peer", { signingKey: "new-key", verified: true });
        });
        const unchanged = async () => {
            const result = await replacement.evaluate(async () => ({ history: await dbmod.getSealed(dbmod.Stores.dm, "peer"), pin: await dbmod.getSealed(dbmod.Stores.pins, "peer"), imports: await dbmod.getSealed(dbmod.Stores.device, "history-imports") }));
            assert.deepEqual(result.history.messages, [{ id: "new", body: "replacement private history" }]);
            assert.deepEqual(result.pin, { signingKey: "new-key", verified: true });
            assert.equal(result.imports, undefined);
        };

        await old.evaluate(async () => {
            await dbmod.putSealed(dbmod.Stores.device, "warmup", true);
            await createDM();
            await dm.attachRatchet({ closed: false, project() {}, async history() { return []; }, send() { window.entered = true; return new Promise((_, reject) => { window.refuse = reject; }); } });
            window.sending = dm.send("peer", "old private message", "", { id: "old-op" }).then(() => "success", e => e.code);
        });
        await old.waitForFunction(() => window.entered);
        await replace();
        assert.equal(await old.evaluate(async () => { refuse(Object.assign(new Error("fenced"), { code: "fenced" })); return sending; }), "fenced");
        await unchanged();
        assert.equal(await old.evaluate(() => dm.deleteHistory("peer").then(() => "deleted", e => e.code)), "fenced");
        await unchanged();

        // Hold an actual encrypted pin read, replace from another tab, resume.
        await old.evaluate(async () => {
            await createDM();
            const decrypt = crypto.subtle.decrypt.bind(crypto.subtle);
            crypto.subtle.decrypt = async (...args) => { window.readingPin = true; await new Promise(r => { window.releasePin = r; }); return decrypt(...args); };
            window.pinWork = dm.setVerified("peer", false).then(() => "changed", e => e.code).finally(() => { crypto.subtle.decrypt = decrypt; });
        });
        await old.waitForFunction(() => window.readingPin);
        await replace();
        assert.equal(await old.evaluate(async () => { releasePin(); return pinWork; }), "fenced");
        await unchanged();

        // A correctly signed/encrypted page arriving after replacement cannot
        // restore either its messages or its import marker, nor be ACKed.
        await old.evaluate(async () => {
            await createDM();
            const sender = cryptoMod.generateIdentity();
            const { encodeHistoryChunk } = await import("/src/core/history-envelope.js");
            const plain = encodeHistoryChunk({ source: "source-device", target: dm.deviceID, transfer: "transfer-1", seq: 0, total: 1 }, JSON.stringify([{ peer: "peer", from: "peer", body: "old transferred history", sent_at: 1, client_msg_id: "old-history", protocol: "noxa-olm-v1", provenance: "authenticated" }]), sender.edSecret);
            window.pageData = { history: [{ seq: 0, total: 1, from_device_id: "source-device", data: cryptoMod.sealDM(plain, dm.identity.xPublic, sender.xSecret) }] };
            dm.devicesOf = async () => ({ devices: [{ deviceID: "source-device", x25519: sender.xPublic, signingKey: sender.edPublic }] });
            window.actions = [];
            dm.conn.request = (_, request) => { actions.push(request.action); return new Promise(r => { window.releaseHistory = r; }); };
            window.importing = dm.importHistory().then(() => "imported", e => e.code);
        });
        await old.waitForFunction(() => typeof window.releaseHistory === "function");
        await replace();
        assert.equal(await old.evaluate(async () => { releaseHistory(pageData); return importing; }), "fenced");
        assert.deepEqual(await old.evaluate(() => actions), ["history_get"]);
        await unchanged();

        // Same-account deletion must order behind an already encrypting save.
        await old.evaluate(async () => {
            await createDM();
            const encrypt = crypto.subtle.encrypt.bind(crypto.subtle);
            crypto.subtle.encrypt = async (...args) => { window.saving = true; await new Promise(r => { window.releaseSave = r; }); return encrypt(...args); };
            dm.showQueued("delete-peer", { id: "queued", text: "old", sentAt: 1 });
            window.restoreEncrypt = () => { crypto.subtle.encrypt = encrypt; };
        });
        await old.waitForFunction(() => window.saving);
        await old.evaluate(() => { window.deleting = dm.deleteHistory("delete-peer"); });
        assert.equal(await old.evaluate(async () => { restoreEncrypt(); releaseSave(); await deleting; return await dbmod.getSealed(dbmod.Stores.dm, "delete-peer"); }), undefined);
        await old.evaluate(() => { dm.showQueued("flush-peer", { id: "last", text: "keep me", sentAt: 2 }); dm.close(); });
        await old.waitForFunction(async () => (await dbmod.getSealed(dbmod.Stores.dm, "flush-peer"))?.messages[0]?.body === "keep me");
    } finally { await browser.close(); await vite.close(); }
});
