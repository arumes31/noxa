import assert from "node:assert/strict";
import { test } from "node:test";
import { spawn, execFileSync } from "node:child_process";
import { createServer } from "node:http";
import { readFile, readdir, mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import readline from "node:readline";
import { chromium } from "@playwright/test";

const root = fileURLToPath(new URL("../../", import.meta.url));
const storageKey = Buffer.alloc(32, 42).toString("base64"); // synthetic test material
const binding = (from, to, id) => ({ server: "test-server-certificate", conversation: "alice:bob", sender: from, sender_device: `${from}-device`, recipient: to, recipient_device: `${to}-device`, message_id: id, purpose: "message" });

test("packaged Chromium worker and native Go exchange authenticated ratchet messages", { timeout: 120000 }, async () => {
    const temp = await mkdtemp(path.join(os.tmpdir(), "noxa-ratchet-test-"));
    let child, browser, server;
    try {
        const binary = path.join(temp, process.platform === "win32" ? "host.exe" : "host");
        execFileSync(process.env.GO_EXE || "go", ["build", "-o", binary, "./internal/e2ee/ratchet/testdata/host"], { cwd: root, stdio: "pipe" });
        child = spawn(binary, [], { stdio: ["pipe", "pipe", "pipe"] });
        const waiting = [];
        const lines = readline.createInterface({ input: child.stdout });
        lines.on("line", line => waiting.shift()?.resolve(JSON.parse(line)));
        child.on("exit", () => { for (const waiter of waiting.splice(0)) waiter.reject(new Error("native test host exited")); });
        const native = request => new Promise((resolve, reject) => { waiting.push({ resolve, reject }); child.stdin.write(JSON.stringify({ key: storageKey, ...request }) + "\n"); });
        const assets = await readdir(path.join(root, "web/app/assets"));
        const entry = assets.find(name => /^ratchet-[\w-]+\.js$/.test(name));
        assert.ok(entry, "run npm run build before testing the packaged core");
        server = createServer(async (req, res) => {
            const url = new URL(req.url, "http://localhost");
            const relative = url.pathname.startsWith("/app/") ? `web${url.pathname}` : "";
            const name = path.resolve(root, relative.slice(0));
            if (!relative || !name.startsWith(path.join(root, "web") + path.sep)) { res.writeHead(404).end(); return; }
            try {
                const data = await readFile(name);
                res.setHeader("Content-Type", name.endsWith(".js") ? "application/javascript" : name.endsWith(".wasm") ? "application/wasm" : "text/html");
                res.setHeader("Content-Security-Policy", "script-src 'self' 'wasm-unsafe-eval'; worker-src 'self'; object-src 'none'");
                res.end(data);
            } catch { res.writeHead(404).end(); }
        });
        await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
        browser = await chromium.launch({ headless: true });
        const page = await browser.newPage();
        await page.goto(`http://127.0.0.1:${server.address().port}/app/index.html`);
        await page.evaluate(async entry => {
            const { createRatchetCore } = await import(`/app/assets/${entry}`);
            globalThis.ratchetTest = await createRatchetCore();
        }, entry);
        const web = request => page.evaluate(async request => {
            try { return await globalThis.ratchetTest.apply({ ...request, key: request.key ? Uint8Array.from(atob(request.key), c => c.charCodeAt(0)) : new Uint8Array(32).fill(42) }); }
            catch { return { error: "rejected" }; }
        }, request);
        const ok = value => { assert.equal(value.error, undefined); return value; };
        const vector = JSON.parse(await readFile(path.join(root, "internal/e2ee/ratchet/testdata/interop-vector.json"), "utf8"));
        for (const [request, plaintext] of [[vector.inbound, vector.first_plaintext], [vector.decrypt, vector.reply_plaintext]]) {
            assert.equal(ok(await web(request)).plaintext, plaintext);
            assert.equal(ok(await native(request)).plaintext, plaintext);
        }
        const alice = ok(await native({ op: "new_account", count: 1 }));
        const bob = ok(await web({ op: "new_account", count: 1 }));
        const firstBinding = binding("alice", "bob", "initial");
        let a = ok(await native({ op: "outbound", account: alice.account, peer_identity: bob.bundle.identity, one_time_key: bob.bundle.one_time_keys[0].key, binding: firstBinding, plaintext: "Go to Chromium" }));
        const first = a.envelope;
        let b = ok(await web({ op: "inbound", account: bob.account, peer_identity: alice.bundle.identity, binding: firstBinding, envelope: first }));
        assert.equal(b.plaintext, "Go to Chromium"); assert.equal(a.session_id, b.session_id);
        assert.ok((await web({ op: "inbound", account: b.account, peer_identity: alice.bundle.identity, binding: firstBinding, envelope: first })).error, "prekey is consumed");
        assert.ok((await web({ op: "inbound", account: bob.account, peer_identity: bob.bundle.identity, binding: firstBinding, envelope: first })).error, "sender identity substitution is rejected");
        // Change every outer routing field and the expected binding together:
        // authentication must still find that the encrypted binding disagrees.
        for (const field of Object.keys(firstBinding)) {
            const altered = structuredClone(first); altered.binding[field] = field === "purpose" ? "edit" : "substituted";
            assert.ok((await web({ op: "inbound", account: bob.account, peer_identity: alice.bundle.identity, binding: altered.binding, envelope: altered })).error, `authenticated ${field}`);
        }
        for (let step = 0; step < 6; step++) {
            const reply = binding("bob", "alice", `web-${step}`);
            b = ok(await web({ op: "encrypt", session: b.session, binding: reply, plaintext: `web reply ${step}` }));
            a = ok(await native({ op: "decrypt", session: a.session, binding: reply, envelope: b.envelope }));
            assert.equal(a.plaintext, `web reply ${step}`);
            const response = binding("alice", "bob", `native-${step}`);
            a = ok(await native({ op: "encrypt", session: a.session, binding: response, plaintext: `native reply ${step}` }));
            b = ok(await web({ op: "decrypt", session: b.session, binding: response, envelope: a.envelope }));
            assert.equal(b.plaintext, `native reply ${step}`);
        }
        const pending = [];
        for (let i = 0; i < 4; i++) {
            const context = binding("alice", "bob", `pending-${i}`);
            a = ok(await native({ op: "encrypt", session: a.session, binding: context, plaintext: `pending ${i}` }));
            pending.push(a.envelope);
        }
        for (const i of [3, 1, 0, 2]) {
            b = ok(await web({ op: "decrypt", session: b.session, binding: pending[i].binding, envelope: pending[i] }));
            assert.equal(b.plaintext, `pending ${i}`);
        }
        assert.ok((await web({ op: "decrypt", session: b.session, binding: pending[1].binding, envelope: pending[1] })).error, "duplicate rejected");
        a = ok(await native({ op: "encrypt", session: a.session, binding: binding("alice", "bob", "tamper"), plaintext: "recoverable" }));
        const corrupt = structuredClone(a.envelope);
        const bytes = Buffer.from(corrupt.message.body, "base64"); bytes[bytes.length - 1] ^= 1;
        corrupt.message.body = bytes.toString("base64").replace(/=+$/, "");
        assert.ok((await web({ op: "decrypt", session: b.session, binding: corrupt.binding, envelope: corrupt })).error, "tamper rejected");
        const valid = ok(await web({ op: "decrypt", session: b.session, binding: a.envelope.binding, envelope: a.envelope }));
        assert.equal(valid.plaintext, "recoverable", "failed operation leaves input snapshot usable");
        assert.ok((await native({ op: "decrypt", key: Buffer.alloc(32, 99).toString("base64"), session: a.session, binding: a.envelope.binding, envelope: a.envelope })).error, "wrong storage key rejected");
        assert.match(await readFile(path.join(root, "web/app/ratchet-licenses.txt"), "utf8"), /vodozemac 0\.11\.1/);
        await page.evaluate(async () => {
            const controller = new AbortController();
            const pending = globalThis.ratchetTest.apply({ op: "new_account", count: 1, key: new Uint8Array(32).fill(42) }, { signal: controller.signal });
            controller.abort();
            try { await pending; throw new Error("cancel accepted"); } catch (error) { if (error.message !== "ratchet operation cancelled") throw error; }
            globalThis.ratchetTest.close();
        });
        assert.ok((await web({ op: "new_account", count: 1 })).error, "closed browser core rejects work");
    } finally {
        child?.stdin.end(); child?.kill();
        await browser?.close();
        await new Promise(resolve => server ? server.close(resolve) : resolve());
        await rm(temp, { recursive: true, force: true });
    }
});
