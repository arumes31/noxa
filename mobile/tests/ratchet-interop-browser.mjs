// Native<->PWA interop, PWA side: the actual PWA DirectMessages/Groups/
// RatchetConsumer + WASM core + IndexedDB per device context, against the real
// framed key/directory/admission handlers and PostgreSQL bridged by
// TestIntegrationRatchetProductNative. The native client test is the
// orchestrator: it posts steps ("pwa_eval") that run here in a device page.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "@playwright/test";
import { ownLinkMeta } from "./own-link-meta.mjs";

const root = fileURLToPath(new URL("../../", import.meta.url));
const bridge = process.env.NOXA_PRODUCT_BRIDGE;
if (!bridge) throw new Error("run from TestIntegrationRatchetProductNative");
const assets = await readdir(path.join(root, "web/app/assets"));
const entry = name => {
    const found = assets.find(f => new RegExp("^" + name + "-[\\w-]+\\.js$").test(f));
    assert(found, `build missing ${name}`);
    return found;
};
const entries = { core: entry("ratchet"), store: entry("ratchetStorage"), consumer: entry("ratchetConsumer"), directory: entry("directory"),
    direct: entry("directMessages"), groups: entry("groups") };
const post = async body => (await fetch(bridge, { method: "POST", body: JSON.stringify(body) })).json();
const server = createServer(async (req, res) => {
    try {
        if (req.url === "/bridge") {
            const chunks = [];
            for await (const c of req) chunks.push(c);
            const response = await fetch(bridge, { method: "POST", body: Buffer.concat(chunks) });
            res.setHeader("Content-Type", "application/json");
            res.end(await response.text());
            return;
        }
        const p = new URL(req.url, "http://localhost").pathname;
        if (p === "/") {
            res.setHeader("Content-Type", "text/html");
            res.end("<!doctype html><title>Ratchet interop</title>");
            return;
        }
        const file = path.resolve(root, "web", p.replace(/^\/app\//, "app/"));
        if (!file.startsWith(path.join(root, "web") + path.sep)) { res.writeHead(404).end(); return; }
        res.setHeader("Content-Type", file.endsWith(".js") ? "application/javascript" : file.endsWith(".wasm") ? "application/wasm" : "text/html");
        res.end(await readFile(file));
    } catch (e) {
        res.writeHead(500).end(String(e));
    }
});

// Runs inside a device page: product classes over a frame-level bridge conn
// (same construction as ratchet-product-browser.mjs).
async function boot({ entries, device, binding, own }) {
    const [{ createRatchetCore }, { openRatchetStore }, consumerAPI, dirAPI, { DirectMessages }, { Groups }] = await Promise.all([
        import(`/app/assets/${entries.core}`), import(`/app/assets/${entries.store}`), import(`/app/assets/${entries.consumer}`),
        import(`/app/assets/${entries.directory}`), import(`/app/assets/${entries.direct}`), import(`/app/assets/${entries.groups}`)]);
    const bridge = async body => (await fetch("/bridge", { method: "POST", body: JSON.stringify(body) })).json();
    const conn = {
        open: true,
        send() {},
        async request(type, payload, expected) {
            if (!conn.open) throw new Error("connection closed");
            const r = await bridge({ op: "request", device: device.name, type, payload });
            if (r.lost) throw new Error("connection lost");
            if (r.error) throw new Error(r.error);
            const body = JSON.parse(JSON.stringify(r.payload));
            if (r.type === 7) throw Object.assign(new Error(body.message), { code: body.code });
            if (r.type !== expected) throw new Error(`unexpected frame ${r.type}`);
            return body;
        },
    };
    const edSecret = Uint8Array.from(atob(device.edSecret), c => c.charCodeAt(0));
    // This account's own root comes from the desktop-signed link statement (own-link-meta.mjs).
    const identity = { edSecret, edPublic: edSecret.slice(32), xPublic: Uint8Array.from(atob(own.xPublic), c => c.charCodeAt(0)) };
    if (device.x25519_private) identity.xSecret = Uint8Array.from(atob(device.x25519_private), c => c.charCodeAt(0));
    const session = Object.assign(new EventTarget(), { conn, identity, uniqueID: device.principal, deviceID: device.device, nickname: device.principal, capabilities: new Set() });
    const namespace = { server: binding, principal: device.principal, device: device.device };
    const stats = { unread: 0, incoming: 0, ops: {} };
    const store = await openRatchetStore(namespace), core = await createRatchetCore();
    const counted = { apply: (r, o) => { stats.ops[r.op] = (stats.ops[r.op] || 0) + 1; return core.apply(r, o); }, close: () => core.close() };
    const dm = new DirectMessages(session, { linkBinding: own.linkBinding, bindingAnchor: own.bindingAnchor });
    const groups = new Groups(session, dm);
    dm.addEventListener("messages", e => { const { message, isNew } = e.detail; if (isNew && message && !message.self && !message.copied) stats.unread++; });
    groups.addEventListener("incoming", () => stats.incoming++);
    const consumer = new consumerAPI.RatchetConsumer({
        store, core: counted, namespace, ...consumerAPI.connectionTransport(conn, { OlmAdmissionRequest: 202, OlmAdmissionResult: 203, OlmDirectoryRequest: 200, OlmDirectoryResult: 201 }),
        deviceKey: async (principal, id) => {
            for (const refresh of [false, true]) {
                const entry = await dm.devicesOf(principal, { refresh });
                const found = entry.devices.find(d => d.deviceID === id);
                if (found) return found.signingKey;
            }
            throw Object.assign(new Error("untrusted device"), { code: "untrusted_device" });
        },
        signBundle: async bundle => dirAPI.signBundle(bundle, edSecret),
        assertCurrent: () => {},
    });
    await dm.attachRatchet(consumer).catch(() => {});
    await groups.attachRatchet(consumer).catch(() => {});
    const T = { device, stats, store, core: counted, dm, groups, consumer };
    T.view = peer => (T.dm.conversations.get(peer)?.messages || []).map(m => ({ id: m.id, from: m.from, body: m.body, protocol: m.protocol || "", self: !!m.self, copied: !!m.copied, pending: !!m.pending, queued: !!m.queued, failed: !!m.failed, edited: !!m.edited, deleted: !!m.deleted, key: m.key || "" }));
    T.groupView = id => (T.groups.messages.get(id) || []).map(m => ({ id: m.id, text: m.text, from: m.from_unique_id, protocol: m.protocol || "", local: !!m.local, deleted: !!m.deleted, reference: m.reference }));
    globalThis.T = T;
    return (await T.consumer.ready()).epoch;
}

// Evaluates one orchestrator step: an async function body using T.
async function step(body) {
    try {
        const fn = new Function("return (async () => {" + body + "\n})()");
        return { ok: await fn() ?? null };
    } catch (e) {
        return { error: String(e?.message || e), code: e?.code || "", committed: e?.committed ? { state: e.committed.receipt?.state || "", text: e.committed.record?.body?.text ?? null } : null };
    }
}

await new Promise(r => server.listen(0, "127.0.0.1", r));
let browser;
const started = Date.now();
let steps = 0;
try {
    const fixture = await post({ op: "fixture" });
    browser = await chromium.launch();
    const pages = {};
    const open = async (name, device, devices = fixture.devices) => {
        const page = await (await browser.newContext()).newPage();
        page.on("pageerror", e => console.error(name, e));
        await page.goto(`http://127.0.0.1:${server.address().port}/`);
        assert.equal(await page.evaluate(boot, { entries, device, binding: fixture.binding, own: await ownLinkMeta(devices, device) }), 1);
        pages[name] = page;
    };
    for (const name of ["nalice-phone", "pbob-phone"]) await open(name, fixture.devices[name]);
    // The real PWA (built app from the real web listener, app.js guest path),
    // driven through its UI: "#app:join", "#app:send <peer>\t<text>",
    // "#app:has <peer>\t<text>".
    let appPage = null;
    const appGuest = async cmd => {
        if (cmd === "#app:join") {
            const context = await browser.newContext({ serviceWorkers: "block" });
            appPage = await context.newPage();
            appPage.on("pageerror", e => console.error("app-guest", e));
            appPage.on("console", m => { if (m.type() === "error" || m.type() === "warning") console.error("app-guest console:", m.text()); });
            await appPage.goto(`${fixture.web_url}/app/`);
            await appPage.locator("summary").filter({ hasText: "Continue as guest" }).click();
            await appPage.getByLabel("Display name", { exact: true }).fill("App guest");
            await appPage.getByRole("button", { name: "Continue as guest", exact: true }).click();
            await appPage.getByRole("navigation", { name: "Main" }).waitFor({ timeout: 30000 });
            // The one-time secure-messaging explanation.
            const close = appPage.getByRole("button", { name: "Close", exact: true });
            await close.click({ timeout: 10000 }).catch(() => {});
            return "ok";
        }
        const [verb, rest] = [cmd.slice(0, cmd.indexOf(" ")), cmd.slice(cmd.indexOf(" ") + 1)];
        const [peer, text] = rest.split("\t");
        await appPage.evaluate(p => { location.hash = `#/dm/${encodeURIComponent(p)}`; }, peer);
        if (verb === "#app:send") {
            // Only with the active Olm path (never a legacy send): the composer
            // unlocks once both device chains verify.
            const composer = appPage.getByRole("textbox", { name: /Message/ });
            const until = Date.now() + 45000;
            while (!(await composer.isEnabled().catch(() => false))) {
                if (Date.now() > until) throw new Error("composer stayed disabled");
                await new Promise(r => setTimeout(r, 250));
            }
            const banner = await appPage.getByRole("note").first().innerText();
            if (/not active/i.test(banner)) throw new Error(`secure messaging inactive: ${banner}`);
            await composer.fill(text);
            await composer.press("Enter");
            await appPage.getByRole("log").filter({ hasText: text }).waitFor({ timeout: 30000 });
            return "ok";
        }
        if (verb === "#app:has") {
            await appPage.getByRole("log").filter({ hasText: text }).waitFor({ timeout: 45000 });
            return "yes";
        }
        throw new Error(`unknown app command ${verb}`);
    };
    for (;;) {
        const cmd = await post({ op: "pwa_next" });
        if (cmd.idle) continue;
        if (cmd.done) break;
        if (cmd.device === "app-guest") {
            let result;
            try { result = { ok: await appGuest(cmd.js) }; } catch (e) {
                const shown = await appPage?.evaluate(() => document.body.innerText.slice(0, 600)).catch(() => "") || "";
                const first = String(e?.message || e).split(/\r?\n/)[0];
                result = { error: `${first} | page: ${shown.replace(/\s+/g, " ")}` };
            }
            steps++;
            await post({ op: "pwa_result", id: cmd.id, result });
            continue;
        }
        // A PWA device linked during the run boots on its first step.
        if (!pages[cmd.device]) {
            const devices = (await post({ op: "fixture" })).devices, device = devices[cmd.device];
            if (device?.edSecret) await open(cmd.device, device, devices);
        }
        const page = pages[cmd.device];
        const result = page ? await page.evaluate(step, cmd.js) : { error: `unknown PWA device ${cmd.device}` };
        steps++;
        await post({ op: "pwa_result", id: cmd.id, result });
    }
    console.log(`PASS PWA interop side: ${steps} orchestrated steps in ${Date.now() - started}ms`);
} finally {
    await browser?.close();
    await new Promise(r => server.close(r));
}
