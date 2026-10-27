// Packaged Chromium product vertical: actual PWA DirectMessages/Groups/
// RatchetConsumer + WASM core + IndexedDB per device context, against the real
// framed key/directory/admission handlers and PostgreSQL (bridged by
// TestIntegrationRatchetProductPWA). No handwritten crypto in this harness.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "@playwright/test";

const root = fileURLToPath(new URL("../../", import.meta.url));
const bridge = process.env.NOXA_PRODUCT_BRIDGE;
if (!bridge) throw new Error("run from TestIntegrationRatchetProductPWA");
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
            res.end("<!doctype html><title>Ratchet product vertical</title>");
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

// Runs inside a device page: product classes over a frame-level bridge conn.
async function boot({ entries, device, binding }) {
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
            if (r.lost) throw new Error("connection lost"); // handled but response lost
            if (r.error) throw new Error(r.error);
            const body = JSON.parse(JSON.stringify(r.payload));
            if (r.type === 7) throw Object.assign(new Error(body.message), { code: body.code });
            if (r.type !== expected) throw new Error(`unexpected frame ${r.type}`);
            return body;
        },
    };
    const edSecret = Uint8Array.from(atob(device.edSecret), c => c.charCodeAt(0));
    const session = Object.assign(new EventTarget(), { conn, identity: { edSecret }, uniqueID: device.principal, deviceID: device.device, nickname: device.principal, capabilities: new Set() });
    const namespace = { server: binding, principal: device.principal, device: device.device };
    const stats = { unread: 0, incoming: 0, ops: {} };
    let generation = 1;
    const make = async () => {
        const mine = generation;
        const store = await openRatchetStore(namespace), core = await createRatchetCore();
        const counted = { apply: (r, o) => { stats.ops[r.op] = (stats.ops[r.op] || 0) + 1; return core.apply(r, o); }, close: () => core.close() };
        const dm = new DirectMessages(session, {});
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
            assertCurrent: () => { if (mine !== generation) throw Object.assign(new Error("fenced"), { code: "fenced" }); },
        });
        // As in app.js, a failed readiness never aborts the session: sends and
        // drains re-attempt readiness and fail explicitly meanwhile.
        await dm.attachRatchet(consumer).catch(() => {});
        await groups.attachRatchet(consumer).catch(() => {});
        return { store, core: counted, dm, groups, consumer };
    };
    const T = { device, stats, ...(await make()) };
    // Simulated process crash/restart: drop the in-memory instances and their
    // generation; protected IndexedDB state survives.
    // A second consumer over the same IndexedDB namespace (another tab, or
    // an old consumer during reconnect), still current.
    T.spawn = () => make();
    T.restart = async () => { generation++; T.consumer.closed = true; Object.assign(T, await make()); stats.ops = {}; };
    T.view = peer => (T.dm.conversations.get(peer)?.messages || []).map(m => ({ id: m.id, from: m.from, body: m.body, protocol: m.protocol || "", self: !!m.self, copied: !!m.copied, pending: !!m.pending, queued: !!m.queued, failed: !!m.failed, edited: !!m.edited, deleted: !!m.deleted, key: m.key || "" }));
    T.groupView = id => (T.groups.messages.get(id) || []).map(m => ({ id: m.id, text: m.text, from: m.from_unique_id, protocol: m.protocol || "", local: !!m.local, deleted: !!m.deleted, reference: m.reference }));
    globalThis.T = T;
    return (await T.consumer.ready()).epoch;
}

await new Promise(r => server.listen(0, "127.0.0.1", r));
let browser;
const started = Date.now();
try {
    const fixture = await post({ op: "fixture" });
    const group = fixture.group;
    browser = await chromium.launch();
    const pages = {};
    for (const name of ["alice-phone", "alice-tablet", "bob-phone"]) {
        const page = await (await browser.newContext()).newPage();
        page.on("pageerror", e => console.error(name, e));
        await page.goto(`http://127.0.0.1:${server.address().port}/`);
        assert.equal(await page.evaluate(boot, { entries, device: fixture.devices[name], binding: fixture.binding }), 1);
        pages[name] = page;
    }
    // On failure, print per-device request counts for diagnosis.
    globalThis.dumpCounts = async () => console.log(JSON.stringify(await post({ op: "counts" })));
    const run = (name, fn, arg) => pages[name].evaluate(fn, arg);
    const drain = name => run(name, () => T.consumer.drain());
    const counts = () => post({ op: "counts" });
    const transport = (c, d) => ["begin", "put_part", "finalize"].reduce((n, a) => n + (c[`${d}/${a}`] || 0), 0);

    // 1. Direct send through the product entry; own linked copy; typed receipt.
    const sent = await run("alice-phone", () => T.dm.send("bob", "hello bob", "", { id: "m1" }).then(o => ({ state: o.receipt.state, message: o.receipt.message_id, key: o.record.key, reused: o.reused })));
    assert.equal(sent.state, "accepted");
    assert.match(sent.message, /^[0-9a-f-]{36}$/, "transport ID stays an opaque UUID string");
    assert.equal(sent.key, JSON.stringify(["noxa-olm-v1", "alice", "m1"]));
    await drain("bob-phone");
    let bob = await run("bob-phone", () => ({ view: T.view("alice"), unread: T.stats.unread }));
    assert.deepEqual(bob.view.map(m => [m.id, m.from, m.body, m.protocol]), [["m1", "alice", "hello bob", "noxa-olm-v1"]]);
    assert.equal(bob.unread, 1);
    await drain("alice-tablet");
    const tablet = await run("alice-tablet", () => ({ view: T.view("bob"), unread: T.stats.unread }));
    assert.deepEqual(tablet.view.map(m => [m.id, m.body, m.self, m.copied]), [["m1", "hello bob", true, true]]);
    assert.equal(tablet.unread, 0, "own linked copy is not unread");

    // 2. Reply reaches both of alice's devices (existing + newly claimed session).
    await run("bob-phone", () => T.dm.send("alice", "hi alice", "", { id: "b1" }));
    await drain("alice-phone");
    await drain("alice-tablet");
    for (const name of ["alice-phone", "alice-tablet"]) {
        const v = await run(name, () => T.view("bob").filter(m => m.from === "bob").map(m => m.body));
        assert.deepEqual(v, ["hi alice"], name);
    }

    // 3. Uncertain accepted response, then a changed draft on the same ID:
    //    exact status resolution shows the ORIGINAL committed text, no reseal.
    await post({ op: "drop", device: "alice-phone", action: "finalize" });
    const lost = await run("alice-phone", () => T.dm.send("bob", "original draft", "", { id: "m2" }).then(() => null, e => ({ retryable: e.retryable, view: T.view("bob").find(m => m.id === "m2") })));
    assert.equal(lost.retryable, true);
    assert.equal(lost.view.body, "original draft");
    assert.equal(lost.view.queued, true);
    let before = await counts();
    const reused = await run("alice-phone", () => T.dm.send("bob", "CHANGED TEXT", "", { id: "m2" }).then(o => ({ reused: o.reused, body: o.record.body.text, state: o.receipt.state, view: T.view("bob").find(m => m.id === "m2") })));
    assert.deepEqual([reused.reused, reused.body, reused.state, reused.view.body, reused.view.pending, reused.view.queued], [true, "original draft", "accepted", "original draft", false, false]);
    let after = await counts();
    assert.equal(transport(after, "alice-phone") - transport(before, "alice-phone"), 0, "accepted status needs no exact retry");
    await drain("bob-phone");
    bob = await run("bob-phone", () => T.view("alice").filter(m => m.id === "m2").map(m => m.body));
    assert.deepEqual(bob, ["original draft"]);

    // 4. Local commit failure -> zero transport; the next send commits normally.
    before = await counts();
    const commitFailure = await run("alice-phone", async () => {
        const original = T.consumer.store.commitAttempt;
        T.consumer.store.commitAttempt = async () => { T.consumer.store.commitAttempt = original; throw Object.assign(new Error("disk full"), { code: "exhausted" }); };
        return T.dm.send("bob", "never sent", "", { id: "m3" }).then(() => "sent", e => e.code);
    });
    assert.equal(commitFailure, "exhausted");
    after = await counts();
    assert.equal(transport(after, "alice-phone") - transport(before, "alice-phone"), 0, "no transport before durable commit");
    assert.equal(await run("alice-phone", () => T.dm.send("bob", "now sent", "", { id: "m3" }).then(o => o.receipt.state)), "accepted");

    // 5. Receive committed, ACK lost, process restart: stored outcome + exact
    //    ACK without re-decrypt or duplicate unread/notification effects.
    await drain("bob-phone");
    await run("alice-phone", () => T.dm.send("bob", "survives crash", "", { id: "m4" }));
    await post({ op: "fail", device: "bob-phone", action: "receive_ack" });
    const unreadBefore = await run("bob-phone", () => T.stats.unread);
    assert.equal(await run("bob-phone", () => T.consumer.drain().then(() => "ok", e => e.message)), "connection lost");
    assert.equal(await run("bob-phone", () => T.stats.unread), unreadBefore + 1);
    await run("bob-phone", () => T.restart());
    const unreadRestart = await run("bob-phone", () => T.stats.unread);
    await drain("bob-phone");
    bob = await run("bob-phone", () => ({ m4: T.view("alice").filter(m => m.id === "m4").map(m => m.body), ops: T.stats.ops, unread: T.stats.unread }));
    assert.deepEqual(bob.m4, ["survives crash"], "restored from the protected archive once");
    assert.equal(bob.ops.decrypt || 0, 0, "no ratchet re-decrypt after commit");
    assert.equal(bob.ops.inbound || 0, 0);
    assert.equal(bob.unread, unreadRestart, "no duplicate unread after replay");
    after = await counts();
    assert.equal(after["bob-phone/receive_ack"] >= 2, true);

    // 6. Authorship: edits/deletes address (sender, logical ID); another sender
    //    reusing the same logical string never collides or edits alice's record.
    await run("alice-phone", () => T.dm.editMessage("bob", "m1", "hello bob (edited)"));
    await drain("bob-phone");
    await run("bob-phone", () => T.dm.send("alice", "bob reuses m1", "", { id: "m1" }));
    await run("bob-phone", () => T.consumer.send({ logicalID: "bob-edit", scope: "direct", peer: "alice", purpose: "edit", reference: "m1", body: { text: "bob edited his own m1" } }));
    await drain("alice-phone");
    const alice = await run("alice-phone", () => T.view("bob").filter(m => m.id === "m1").map(m => [m.from, m.body, m.edited]));
    assert.deepEqual(alice.sort(), [["alice", "hello bob (edited)", true], ["bob", "bob edited his own m1", true]]);
    await run("alice-phone", () => T.dm.deleteMessage("bob", "m1"));
    await drain("bob-phone");
    bob = await run("bob-phone", () => T.view("alice").filter(m => m.id === "m1").map(m => [m.from, m.deleted, m.body]));
    assert.deepEqual(bob.sort(), [["alice", true, ""], ["bob", false, "bob edited his own m1"]]);
    const forged = await run("bob-phone", () => T.consumer.send({ logicalID: "forge", scope: "direct", peer: "alice", purpose: "delete", reference: "b-unknown", body: {} }).then(() => "accepted", e => e.code));
    assert.notEqual(forged, "accepted", "server rejects a reference the sender never sent");

    // 7. Existing encrypted attachment token rides inside the authenticated body.
    const token = "[file:storage-1#" + "A".repeat(43) + "=#nx1;s=12#report.pdf]";
    await run("alice-phone", t => T.dm.send("bob", `see ${t}`, "", { id: "file-1" }), token);
    await drain("bob-phone");
    assert.deepEqual(await run("bob-phone", () => T.view("alice").filter(m => m.id === "file-1").map(m => m.body)), [`see ${token}`]);

    // 8. Group vertical with mixed legacy numeric history and opaque records.
    await run("bob-phone", g => T.groups.messages.set(g, [{ id: 7, conversation_id: g, from_unique_id: "alice", text: "legacy numeric", created_at: 1 }]), group);
    const claimsBeforeGroups = (await counts())["alice-phone/claim"] || 0;
    const gs = await run("alice-phone", g => T.groups.send(g, "group hello", { reference: "g1" }).then(o => ({ state: o.receipt.state, message: o.receipt.message_id })), group);
    assert.equal(gs.state, "accepted");
    assert.equal(typeof gs.message, "string");
    await drain("bob-phone");
    let gv = await run("bob-phone", g => ({ view: T.groupView(g), incoming: T.stats.incoming }), group);
    assert.deepEqual(gv.view.map(m => [typeof m.id, m.text, m.protocol]), [["number", "legacy numeric", ""], ["string", "group hello", "noxa-olm-v1"]]);
    assert.equal(gv.incoming, 1);
    await drain("alice-tablet");
    const tg = await run("alice-tablet", g => ({ view: T.groupView(g), incoming: T.stats.incoming }), group);
    assert.deepEqual(tg.view.map(m => m.text), ["group hello"]);
    assert.equal(tg.incoming, 0);
    const olmID = gv.view[1].id;
    await run("alice-phone", ([g, id]) => T.groups.deleteMessage(g, id), [group, "olm:" + JSON.stringify(["noxa-olm-v1", "alice", "g1"])]);
    await drain("bob-phone");
    gv = await run("bob-phone", g => T.groupView(g), group);
    assert.deepEqual(gv.find(m => m.id === olmID).deleted, true);

    // 8b. Device-pair sessions: the DM and BOTH groups reuse the same
    //     alice-phone<->bob-phone and alice-phone<->alice-tablet sessions, so
    //     three conversations need no further claims (budget is 2/pair/day).
    const g2 = await run("alice-phone", g => T.groups.send(g, "second group", { reference: "g2-1" }).then(o => o.receipt.state), fixture.group2);
    assert.equal(g2, "accepted");
    assert.equal(((await counts())["alice-phone/claim"] || 0) - claimsBeforeGroups, 0, "no claim for further conversations with the same devices");
    await drain("bob-phone");
    assert.deepEqual(await run("bob-phone", g => T.groupView(g).map(m => m.text), fixture.group2), ["second group"]);
    await drain("alice-tablet");

    // 9. Stale roster: an approved device without Olm publication blocks the
    //    whole send (no silent omission); after revocation it succeeds.
    await post({ op: "link_unpublished", device: "bob-laptop", action: "bob" });
    before = await counts();
    const blocked = await run("alice-phone", () => T.dm.send("bob", "needs every device", "", { id: "m9" }).then(() => "sent", e => e.code));
    assert.equal(blocked, "exhausted");
    after = await counts();
    assert.equal(transport(after, "alice-phone") - transport(before, "alice-phone"), 0);
    assert.deepEqual(await post({ op: "revoke", device: "bob-laptop" }), true);
    assert.equal(await run("alice-phone", () => T.dm.send("bob", "needs every device", "", { id: "m9" }).then(o => o.receipt.state)), "accepted");

    // 10. P1-1: two current consumers on one namespace race for the same
    //     attempt sequence; each operation ends with its OWN content.
    const raced = await run("alice-phone", async () => {
        const other = await T.spawn();
        const [a, b] = await Promise.all([
            T.consumer.send({ logicalID: "race-A", scope: "direct", peer: "bob", body: { text: "A text" } }),
            other.consumer.send({ logicalID: "race-B", scope: "direct", peer: "bob", body: { text: "B text" } }),
        ]);
        other.consumer.closed = true;
        return [[a.record.logicalID, a.record.body.text, a.receipt.state, a.receipt.logical_id], [b.record.logicalID, b.record.body.text, b.receipt.state, b.receipt.logical_id]];
    });
    assert.deepEqual(raced, [["race-A", "A text", "accepted", "race-A"], ["race-B", "B text", "accepted", "race-B"]]);
    await drain("bob-phone");
    assert.deepEqual(await run("bob-phone", () => T.view("alice").filter(m => m.id.startsWith("race-")).map(m => [m.id, m.body]).sort()), [["race-A", "A text"], ["race-B", "B text"]]);

    // 11. P2-4: committed, never begun, deadline passes -> metadata-only abort,
    //     then a reseal of the ORIGINAL content (not the changed draft).
    await post({ op: "fail", device: "alice-phone", action: "begin" });
    const parked = await run("alice-phone", async () => {
        T.consumer.attemptTTL = 6;
        try { return await T.dm.send("bob", "before deadline", "", { id: "late-1" }).then(() => "sent", e => e.retryable); }
        finally { T.consumer.attemptTTL = 300; }
    });
    assert.equal(parked, true);
    await new Promise(r => setTimeout(r, 7000));
    const resolved = await run("alice-phone", async () => {
        const first = await T.dm.send("bob", "CHANGED", "", { id: "late-1" }).then(o => o.receipt.state, e => [e.code, e.retryable]);
        const second = await T.dm.send("bob", "CHANGED", "", { id: "late-1" }).then(o => [o.receipt.state, o.record.body.text]);
        return { first, second, view: T.view("bob").filter(m => m.id === "late-1").map(m => [m.body, m.queued, m.failed]) };
    });
    assert.deepEqual(resolved.first, ["aborted", true], "the expired attempt is fenced, not stuck");
    assert.deepEqual(resolved.second, ["accepted", "before deadline"]);
    assert.deepEqual(resolved.view, [["before deadline", false, false]]);

    // 12. P2-6: retirement progress is durable; a restarted session replays
    //     no retire_receipt (production rate limits are on in this test).
    await run("alice-phone", () => T.consumer.drain());
    before = await counts();
    await run("alice-phone", () => T.restart());
    await drain("alice-phone");
    after = await counts();
    assert.equal((after["alice-phone/retire_receipt"] || 0) - (before["alice-phone/retire_receipt"] || 0), 0);

    // 13. P2-5: a transient readiness failure is not cached for the session.
    await post({ op: "fail", device: "bob-phone", action: "publish" });
    const retried = await run("bob-phone", async () => {
        await T.restart();
        return T.consumer.ready().then(r => r.epoch, e => e.code || e.message);
    });
    assert.equal(retried, 1);

    // 14. P2-7: a copy that fails authentication is quarantined (locked
    //     placeholder, no unread, no ACK), then opens on the next session.
    await drain("bob-phone");
    await run("alice-phone", () => T.dm.send("bob", "tampered in transit", "", { id: "q-1" }));
    await post({ op: "corrupt", device: "bob-phone" });
    const unreadQ = await run("bob-phone", () => T.stats.unread);
    const q = await run("bob-phone", async () => {
        const result = await T.consumer.drain();
        await T.dm.showQuarantined(result.quarantined);
        return { list: result.quarantined.map(x => [x.sender, x.peer, x.scope]), view: T.view("alice").filter(m => m.id.startsWith("quarantine:")).map(m => [m.from, m.body]), unread: T.stats.unread };
    });
    assert.deepEqual(q.list, [["alice", "alice", "direct"]]);
    assert.deepEqual(q.view, [["alice", null]]);
    assert.equal(q.unread, unreadQ);
    const healed = await run("bob-phone", async () => {
        await T.restart();
        const result = await T.consumer.drain();
        await T.dm.showQuarantined(result.quarantined);
        return { quarantined: result.quarantined.length, view: T.view("alice").filter(m => m.id === "q-1" || m.id.startsWith("quarantine:")).map(m => [m.id, m.body]) };
    });
    assert.deepEqual(healed, { quarantined: 0, view: [["q-1", "tampered in transit"]] });

    // 15. P2-7: explicit changed-identity confirmation resets ratchet pins; the
    //     next send re-adopts only via a freshly signed claim.
    // A fresh contact (one Go-published device with two one-time keys).
    assert.equal(await post({ op: "fake_devices", action: "dave:1" }), true);
    assert.equal(await run("alice-phone", () => T.dm.send("dave", "first contact", "", { id: "dave-1" }).then(o => o.receipt.state)), "accepted");
    before = await counts();
    const reset = await run("alice-phone", async () => {
        await T.consumer.resetTrust("dave");
        return T.dm.send("dave", "after confirmation", "", { id: "trust-1" }).then(o => o.receipt.state);
    });
    after = await counts();
    assert.equal(reset, "accepted");
    assert.equal((after["alice-phone/claim"] || 0) - (before["alice-phone/claim"] || 0), 1, "fresh signed claim, no silent reuse");

    // 16. Client-side authorship refusal (no request reaches the server).
    before = await counts();
    const refused = await run("alice-phone", () => T.dm.editMessage("bob", "b1", "forged").then(() => "edited", e => e.message));
    assert.match(refused, /only your sent messages/);
    after = await counts();
    assert.equal(after["alice-phone/snapshot"], before["alice-phone/snapshot"]);

    // 17. Direct fanout bound: alice(2) + carol(18) = 20 targets is accepted;
    //     a 19th carol device makes 21 and fails before any transport.
    assert.equal(await post({ op: "fake_devices", action: "carol:18" }), true);
    const twenty = await run("alice-phone", () => T.dm.send("carol", "to twenty devices", "", { id: "c20" }).then(o => o.receipt.state, e => e.code));
    assert.equal(twenty, "accepted");
    assert.equal(await post({ op: "fake_devices", action: "carol:1" }), true);
    before = await counts();
    const twentyOne = await run("alice-phone", () => T.dm.send("carol", "to twenty-one", "", { id: "c21" }).then(() => "sent", e => [e.code, e.retryable]));
    after = await counts();
    assert.deepEqual(twentyOne, ["fanout_limit", false]);
    assert.equal(transport(after, "alice-phone") - transport(before, "alice-phone"), 0);

    // 19. N1: a transient receive failure (sender key lookup lost) is NOT
    //     quarantined; the next poll opens the message.
    await run("alice-phone", () => T.dm.send("bob", "transient lookup", "", { id: "n1-msg" }));
    await run("bob-phone", () => T.restart()); // fresh directory cache
    await post({ op: "fail", device: "bob-phone", action: "" }); // next KeyRequest lost
    const n1 = await run("bob-phone", async () => {
        const first = await T.consumer.drain();
        const after1 = T.view("alice").filter(m => m.id === "n1-msg").length;
        const second = await T.consumer.drain();
        return { q1: first.quarantined.length, after1, q2: second.quarantined.length, view: T.view("alice").filter(m => m.id === "n1-msg").map(m => m.body) };
    });
    assert.deepEqual(n1, { q1: 0, after1: 0, q2: 0, view: ["transient lookup"] });

    // 20. N2: accepted receipt is success even if retire_receipt is lost;
    //     retirement is repeated later, the message never shows failed.
    await post({ op: "drop", device: "alice-phone", action: "retire_receipt" });
    const n2 = await run("alice-phone", () => T.dm.send("bob", "accepted then housekeeping lost", "", { id: "n2-msg" })
        .then(o => ({ state: o.receipt.state, view: T.view("bob").filter(m => m.id === "n2-msg").map(m => [m.failed, m.queued, m.pending]) }), e => ({ error: e.code || e.message })));
    assert.deepEqual(n2, { state: "accepted", view: [[false, false, false]] });
    before = await counts();
    await run("alice-phone", () => T.restart());
    await drain("alice-phone");
    after = await counts();
    assert.equal((after["alice-phone/retire_receipt"] || 0) - (before["alice-phone/retire_receipt"] || 0), 1, "the skipped retirement is completed later");

    // 21. N3: persistent clock skew (server refuses an unexpired deadline)
    //     fails the message without aborting, resealing or burning IDs.
    before = await counts();
    const n3 = await run("alice-phone", async () => {
        T.consumer.attemptTTL = 4000; // > server's 3600 s window = "fast clock"
        try {
            const one = await T.dm.send("bob", "skewed", "", { id: "n3-msg" }).then(() => "sent", e => [e.code, e.retryable]);
            const two = await T.dm.send("bob", "skewed again", "", { id: "n3-msg" }).then(() => "sent", e => [e.code, e.retryable]);
            return { one, two, view: T.view("bob").filter(m => m.id === "n3-msg").map(m => [m.body, m.failed, m.queued]) };
        } finally { T.consumer.attemptTTL = 300; }
    });
    after = await counts();
    assert.deepEqual(n3, { one: ["clock_skew", false], two: ["clock_skew", false], view: [["skewed", true, false]] });
    assert.equal((after["alice-phone/begin"] || 0) - (before["alice-phone/begin"] || 0), 1, "one begin, no retry loop");
    assert.equal((after["alice-phone/abort"] || 0) - (before["alice-phone/abort"] || 0), 0, "no abort/reseal while unexpired");

    // 22. N5 smoke: two current consumers race on first contact with a fresh
    //     device; both succeed and a third send needs no new claim.
    assert.equal(await post({ op: "fake_devices", action: "erin:1" }), true);
    const n5 = await run("alice-phone", async () => {
        const other = await T.spawn();
        const r = await Promise.all([
            T.consumer.send({ logicalID: "erin-A", scope: "direct", peer: "erin", body: { text: "A" } }),
            other.consumer.send({ logicalID: "erin-B", scope: "direct", peer: "erin", body: { text: "B" } }),
        ]);
        other.consumer.closed = true;
        return r.map(o => [o.record.body.text, o.receipt.state]);
    });
    assert.deepEqual(n5, [["A", "accepted"], ["B", "accepted"]]);
    before = await counts();
    assert.equal(await run("alice-phone", () => T.dm.send("erin", "third", "", { id: "erin-C" }).then(o => o.receipt.state)), "accepted");
    after = await counts();
    assert.equal((after["alice-phone/claim"] || 0) - (before["alice-phone/claim"] || 0), 0);

    // 23. Logout/session replacement fences the consumer before any transport.
    before = await counts();
    const fenced = await run("alice-tablet", async () => { await T.consumer.close(); return T.dm.send("bob", "after logout", "", { id: "t1" }).then(() => "sent", e => e.code); });
    assert.equal(fenced, "fenced");
    after = await counts();
    assert.equal(transport(after, "alice-tablet") - transport(before, "alice-tablet"), 0);

    console.log(`PASS PWA product vertical with production rate limits (direct+group, linked copies, uncertainty, commit failure, crash/ACK, authorship, attachment, mixed projection, stale roster, two-writer race, deadline abort, durable retirement, readiness retry, quarantine, trust reset, client refusal, direct-20 bound, logout) in ${Date.now() - started}ms`);
} catch (e) { await globalThis.dumpCounts?.(); throw e; } finally {
    await browser?.close();
    await new Promise(r => server.close(r));
}
