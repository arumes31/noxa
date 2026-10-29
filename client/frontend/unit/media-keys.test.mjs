import assert from "node:assert/strict";
import { test } from "node:test";
import { MediaKeyEngine, parseMediaKeyControl, recipientsOf, buildMediaKeyBody, publicationPid, senderPid, MEDIA_KEY_KIND } from "../src/media-keys.js";

const flush = async (n = 30) => { for (let i = 0; i < n; i++) await new Promise(r => setTimeout(r, 0)); };
// Delays collapse to zero; the send timeout is disabled (Infinity).
const fastTimers = { setTimeout: (fn, ms) => setTimeout(fn, 0), clearTimeout, setInterval: () => 0, clearInterval: () => {} };

function fakeMedia() {
    const log = [];
    const keys = new Map();
    const retired = new Map();
    return {
        log, keys,
        async setKey(pid, epoch, kid, key) {
            if (epoch <= (retired.get(pid) || 0)) throw new Error("retired media epoch");
            keys.set(`${pid}#${epoch}`, { kid, key: [...key] }); log.push(["key", pid, epoch, kid]);
        },
        async activate(pid, epoch) { log.push(["activate", pid, epoch]); },
        async retire(pid, through) { retired.set(pid, Math.max(retired.get(pid) || 0, through)); log.push(["retire", pid, through]); },
        async retireOne(pid, epoch) { log.push(["retireOne", pid, epoch]); },
        async forget(pid) { log.push(["forget", pid]); },
    };
}

const member = (client, principal, device, channel = 5) => ({ client_id: client, unique_id: principal, media_device: device, channel_id: channel });
const me = { channelID: 5, clientID: "c-me", principal: "u-me", device: "d-me" };

function engine({ send, media = fakeMedia(), now } = {}) {
    let clock = 1_700_000_000_000;
    const sent = [];
    const e = new MediaKeyEngine({
        media, timers: fastTimers, sendTimeoutMs: Infinity, now: now || (() => clock++),
        send: send || (async control => { sent.push(control); return { ok: true }; }),
    });
    return { e, media, sent };
}

test("recipients are other media devices in my channel, never me or another channel", () => {
    const list = recipientsOf([member("c-me", "u-me", "d-me"), member("c1", "u1", "d1"), member("c2", "u2", "d2", 9), member("c3", "u3", ""), member("c4", "u-me", "d-me2"), member("c5", "u5", "d-me")], me);
    assert.deepEqual(list.map(r => r.device), ["d-me2", "d1"]);
});

test("a voice session keys every slot, distributes only to current devices, then activates", async () => {
    const { e, media, sent } = engine();
    e.setRoster([member("c-me", "u-me", "d-me"), member("c1", "u1", "d1"), member("c2", "u2", "d2")]);
    e.start(me);
    await flush();
    assert.equal(sent.length, 2);
    for (const control of sent) {
        assert.equal(control.body.kind, MEDIA_KEY_KIND);
        const parsed = parseMediaKeyControl(control.body, 5);
        assert.equal(parsed.ok, true);
        assert.equal(parsed.value.client, "c-me");
        assert.equal(parsed.value.n, 3);
        assert.deepEqual([...parsed.value.keys.mic], media.keys.get(`${senderPid("mic")}#${control.body.epoch}`).key);
    }
    const activations = media.log.filter(([op]) => op === "activate");
    assert.equal(activations.length, 4);
    assert.ok(media.log.findIndex(([op]) => op === "activate") > media.log.findIndex(([op]) => op === "key"));
    assert.equal(e.status().state, "encrypted");
    assert.ok(media.keys.has(`${publicationPid("c-me", "cam")}#${sent[0].body.epoch}`), "self-echo key installed locally");
    e.stop();
});

test("a joiner gets only the new epoch and a departed device gets nothing after it left", async () => {
    const { e, sent } = engine();
    e.setRoster([member("c1", "u1", "d1")]);
    e.start(me);
    await flush();
    const first = sent[0].body.epoch;
    e.setRoster([member("c1", "u1", "d1"), member("c2", "u2", "d2")]);
    await flush();
    const toJoiner = sent.filter(c => c.device === "d2");
    assert.equal(toJoiner.length, 1);
    assert.ok(toJoiner[0].body.epoch > first, "the joiner never receives the earlier epoch");
    const second = toJoiner[0].body.epoch;
    assert.notDeepEqual(sent.find(c => c.device === "d1" && c.body.epoch === second).body.data.keys.mic, sent[0].body.data.keys.mic, "fresh key material per epoch");
    e.setRoster([member("c2", "u2", "d2")]);
    await flush();
    const third = sent.at(-1).body.epoch;
    assert.ok(third > second);
    assert.equal(sent.filter(c => c.device === "d1" && c.body.epoch >= third).length, 0, "the departed device gets no new key");
    e.stop();
});

test("a recipient whose control fails is reported and retried, never sent plaintext", async () => {
    let attempts = 0;
    const sent = [];
    const states = [];
    const e = new MediaKeyEngine({ media: fakeMedia(), timers: fastTimers, sendTimeoutMs: Infinity, onStatus: st => states.push(st),
        send: async c => { sent.push(c); if (c.device === "d2" && attempts++ < 2) throw Object.assign(new Error("x"), { code: "device_unavailable" }); return { ok: true }; } });
    e.setRoster([member("c1", "u1", "d1"), member("c2", "u2", "d2")]);
    e.start(me);
    await flush(80);
    const partial = states.find(st => st.state === "partial");
    assert.deepEqual(partial.failed.map(f => [f.device, f.reason]), [["d2", "device_unavailable"]]);
    assert.equal(e.status().state, "encrypted");
    const toD2 = sent.filter(c => c.device === "d2");
    assert.equal(toD2.length, 3);
    assert.equal(new Set(toD2.map(c => c.body.epoch)).size, 1, "retries carry the same current epoch");
    assert.equal(new Set(toD2.map(c => c.logicalID)).size, 3, "each retry is a new logical operation");
    e.stop();
});

test("stale async completions after stop never activate a key", async () => {
    let release;
    const gate = new Promise(r => { release = r; });
    const { e, media } = engine({ send: async () => { await gate; return { ok: true }; } });
    e.setRoster([member("c1", "u1", "d1")]);
    e.start(me);
    await flush();
    e.stop();
    release();
    await flush();
    assert.equal(media.log.filter(([op]) => op === "activate").length, 0);
});

function control(epoch, { client = "c1", sender = "u1", device = "d1", channel = 5, kid = 0, fill = 1 } = {}) {
    const keys = { mic: new Uint8Array(32).fill(fill), cam: new Uint8Array(32).fill(fill + 1), screen: new Uint8Array(32).fill(fill + 2), screenaudio: new Uint8Array(32).fill(fill + 3) };
    const body = buildMediaKeyBody({ channelID: channel, epoch, nowSeconds: 1, client, session: "s1", kid, members: "m", count: 2, keys });
    return { sender, senderDevice: device, logicalID: "l", ...body, expiresAt: body.expires_at };
}

test("receivers accept only current-scope controls from the roster device, newest epoch only", async () => {
    const { e, media } = engine();
    e.setRoster([member("c1", "u1", "d1"), member("c-me", "u-me", "d-me")]);
    e.start(me);
    await flush();
    assert.equal(e.receive(control(100)).ok, true);
    await flush();
    assert.ok(media.keys.has("c1|mic#100") && media.keys.has("c1|screenaudio#100"));
    assert.equal(e.receive(control(100)).reason, "stale", "replay");
    assert.equal(e.receive(control(99)).reason, "stale", "older or expired retry");
    assert.equal(e.receive(control(200, { channel: 6 })).reason, "scope", "another channel");
    assert.equal(e.receive({ ...control(201), kind: "other" }).reason, "kind");
    const forged = control(202, { device: "d-evil" });
    assert.equal(e.receive(forged).reason, "pending", "a device the roster does not name for c1 is held, not used");
    await flush();
    assert.equal(media.keys.has("c1|mic#202"), false);
    assert.equal(e.receive(control(150)).ok, true);
    await flush();
    assert.ok(media.log.some(([op, pid, through]) => op === "retire" && pid === "c1|mic" && through === 100), "previous epoch retires after grace");
    e.setRoster([member("c-me", "u-me", "d-me")]);
    await flush();
    assert.ok(media.log.some(([op, pid]) => op === "forget" && pid === "c1|cam"), "a departed publisher loses its keys");
    e.stop();
});

test("a control that races ahead of the roster is applied once the roster names the device", async () => {
    const { e, media } = engine();
    e.setRoster([member("c-me", "u-me", "d-me")]);
    e.start(me);
    await flush();
    assert.equal(e.receive(control(300, { client: "c9", sender: "u9", device: "d9" })).reason, "pending");
    e.setRoster([member("c-me", "u-me", "d-me"), member("c9", "u9", "d9")]);
    await flush();
    assert.ok(media.keys.has("c9|mic#300"));
    e.stop();
});

test("control parsing is strict", () => {
    const good = control(5);
    assert.equal(parseMediaKeyControl(good, 5).ok, true);
    assert.equal(parseMediaKeyControl({ ...good, data: { ...good.data, extra: 1 } }, 5).ok, false);
    assert.equal(parseMediaKeyControl({ ...good, data: { ...good.data, v: 2 } }, 5).reason, "version");
    assert.equal(parseMediaKeyControl({ ...good, data: { ...good.data, keys: { ...good.data.keys, mic: "AAAA" } } }, 5).reason, "keys");
    assert.equal(parseMediaKeyControl({ ...good, data: { ...good.data, kid: 16 } }, 5).reason, "data");
    assert.equal(parseMediaKeyControl({ ...good, epoch: 0 }, 5).reason, "epoch");
    assert.equal(parseMediaKeyControl(good, "5").ok, true);
    assert.ok(JSON.stringify(good).length < 4096 - 200, "fits the 4 KiB control data bound");
});

test("a removal activates its new epoch sooner than other rekeys", async () => {
    const delays = [];
    const timers = { setTimeout: (fn, ms) => { delays.push(ms); return setTimeout(fn, 0); }, clearTimeout, setInterval: () => 0, clearInterval: () => {} };
    const e = new MediaKeyEngine({ media: fakeMedia(), timers, sendTimeoutMs: Infinity, send: async () => ({ ok: true }) });
    e.setRoster([member("c1", "u1", "d1"), member("c2", "u2", "d2")]);
    e.start(me);
    await flush();
    assert.ok(delays.includes(2500), "ordinary activation waits for receivers to drain");
    delays.length = 0;
    e.setRoster([member("c1", "u1", "d1")]);
    await flush();
    assert.ok(delays.includes(1000) && delays.some(d => d > 1000 && d <= 2200) && !delays.includes(2500), `removal is urgent (1 s after delivery, deadline 2.2 s after the removal): ${delays}`);
    e.stop();
});

// Review 2a-A P2-1 repro: ghost entries outside what the voice UI renders
// (string channel ids, alias fields) never receive keys.
test("only strict integer channel members receive keys (no ghost recipients)", () => {
    const roster = [member("c1", "u1", "d1"), { client_id: "g1", unique_id: "u-ghostA", media_device: "dA", channel_id: "5" },
        { client_id: "g2", unique_id: "u-ghostB", media_device: "dB", channelID: 5 }, { client_id: "g3", unique_id: "u-ghostC", media_device: "dC", channel_id: 5.5 }];
    assert.deepEqual(recipientsOf(roster, me).map(r => r.principal), ["u1"]);
});

test("a removal during distribution aborts that epoch and rekeys without the removed device", async () => {
    let release;
    const gate = new Promise(r => { release = r; });
    const sent = [];
    const media = fakeMedia();
    const e = new MediaKeyEngine({ media, timers: fastTimers, sendTimeoutMs: Infinity, send: async c => { sent.push(c); if (sent.length <= 2) await gate; return { ok: true }; } });
    e.setRoster([member("c1", "u1", "d1"), member("c2", "u2", "d2")]);
    e.start({ ...me, principal: "u-me-abort", device: "d-me-abort" });
    await flush();
    const first = sent[0].body.epoch;
    e.setRoster([member("c1", "u1", "d1")]); // d2 kicked while the first epoch is in flight
    release();
    await flush(80);
    const activated = media.log.filter(([op]) => op === "activate").map(([, , epoch]) => epoch);
    assert.ok(!activated.includes(first), `the epoch known to the removed device is never activated: ${activated}`);
    const later = sent.filter(c => c.body.epoch > first);
    assert.ok(later.length && later.every(c => c.device !== "d2"), "the new epoch excludes the removed device");
    assert.ok(activated.some(epoch => epoch > first));
    e.stop();
});

test("a membership digest that differs after the roster settled is a warning, not a gate", async () => {
    const states = [];
    const media = fakeMedia();
    const e = new MediaKeyEngine({ media, timers: fastTimers, sendTimeoutMs: Infinity, onStatus: s => states.push(s), send: async () => ({ ok: true }) });
    e.setRoster([member("c-me", "u-me", "d-me"), member("cm", "u-m", "d-m")]);
    e.start(me);
    await flush();
    e.receive({ ...control(5_000_000_000_000, { client: "cm", sender: "u-m", device: "d-m" }), data: { ...control(5_000_000_000_000).data, client: "cm", members: "x".repeat(43) } });
    await flush(60);
    assert.ok(media.keys.has("cm|mic#5000000000000"), "the key is still installed");
    assert.deepEqual(e.status().membershipMismatch.map(m => m.principal), ["u-m"]);
    e.stop();
});

test("epochs never go backwards across engines, even when the clock steps back", async () => {
    const id = { channelID: 5, clientID: "c-mono", principal: "u-mono", device: "d-mono" };
    let clock = 2_000_000_000_000;
    const sent = [];
    const mk = () => new MediaKeyEngine({ media: fakeMedia(), timers: fastTimers, sendTimeoutMs: Infinity, now: () => clock, send: async c => { sent.push(c); return { ok: true }; } });
    const a = mk(); a.setRoster([member("c1", "u1", "d1")]); a.start(id); await flush();
    const first = sent.at(-1).body.epoch;
    a.stop();
    clock -= 3_600_000; // the wall clock steps back an hour
    const b = mk(); b.setRoster([member("c1", "u1", "d1")]); b.start(id); await flush();
    assert.ok(sent.at(-1).body.epoch > first);
    b.stop();
});

test("the epoch line is per (principal, device): another principal on the same device id is separate", async () => {
    const { e, media } = engine();
    e.setRoster([member("c1", "u-line1", "d-shared"), member("c2", "u-line2", "d-shared")]);
    e.start(me);
    await flush();
    assert.equal(e.receive(control(7_000_000_000_000, { client: "c1", sender: "u-line1", device: "d-shared" })).ok, true);
    assert.equal(e.receive(control(6_000_000_000_000, { client: "c2", sender: "u-line2", device: "d-shared" })).ok, true);
    await flush();
    assert.ok(media.keys.has("c2|mic#6000000000000"));
    e.stop();
});

test("an invisible session sends no key controls and stops", async () => {
    let invisible = 0;
    const { e, sent } = engine();
    e.onInvisible = () => { invisible++; };
    e.setRoster([member("c1", "u1", "d1"), { ...member("c-me", "u-me", "d-me"), status: "invisible" }]);
    e.start(me);
    e.setRoster([member("c1", "u1", "d1"), { ...member("c-me", "u-me", "d-me"), status: "invisible" }]);
    await flush();
    assert.ok(invisible >= 1);
    e.stop();
    assert.equal(sent.length, 0, "no key control leaves an invisible session");
});

test("the status names exactly who holds the current keys", async () => {
    const { e } = engine();
    e.setRoster([{ ...member("c1", "u1", "d1"), nickname: "Alice" }, member("c2", "u2", "d2")]);
    e.start(me);
    await flush();
    assert.deepEqual(e.status().keyHolders.map(k => k.nickname || k.principal).sort(), ["Alice", "u2"]);
    e.stop();
});

test("a removal preempts an in-flight rekey: its epoch is retired, the previous one stays live", async () => {
    let release;
    const gate = new Promise(r => { release = r; });
    const sent = [];
    const media = fakeMedia();
    const e = new MediaKeyEngine({ media, timers: fastTimers, sendTimeoutMs: Infinity, send: async c => { sent.push(c); if (sent.length > 2 && sent.length <= 4) await gate; return { ok: true }; } });
    e.setRoster([member("c1", "u1", "d1"), member("c2", "u2", "d2")]);
    e.start({ ...me, principal: "u-keep", device: "d-keep" });
    await flush(40);
    const live = e.status().epoch;
    e.publicationsChanged(); await flush(10);
    const aborted = sent.at(-1).body.epoch;
    e.setRoster([member("c1", "u1", "d1")]);
    release(); await flush(80);
    assert.ok(media.log.some(([op, , ep]) => op === "retireOne" && ep === aborted));
    // Until a newer epoch activates, nothing retires the live epoch.
    const next = media.log.findIndex(([op, pid, ep]) => op === "activate" && pid === "self|mic" && ep > aborted);
    assert.ok(next > 0, "a newer epoch activates");
    assert.ok(!media.log.slice(0, next).some(([op, pid, through]) => op === "retire" && pid === "self|mic" && through >= live), "the live epoch stays usable until then");
    e.stop();
});

test("invisible entries neither receive keys nor count in the membership digest", () => {
    const roster = [member("c1", "u1", "d1"), { ...member("c2", "u2", "d2"), status: "invisible" }];
    assert.deepEqual(recipientsOf(roster, me).map(r => r.principal), ["u1"]);
});

test("a tile's verified account is the roster account whose device delivered the installed key", async () => {
    const { e } = engine();
    e.setRoster([member("c7", "u7", "d7"), member("c-me", "u-me", "d-me")]);
    e.start(me);
    await flush();
    assert.equal(e.verifiedPublisher("c7"), "", "no key installed yet");
    assert.equal(e.receive(control(700, { client: "c7", sender: "u7", device: "d7" })).ok, true);
    await flush();
    assert.equal(e.verifiedPublisher("c7"), "u7");
    // A forged control from another device of a different account never verifies c7.
    assert.equal(e.receive(control(701, { client: "c7", sender: "u-evil", device: "d-evil" })).reason, "pending");
    await flush();
    assert.equal(e.verifiedPublisher("c7"), "u7");
    // The roster moves c7 to another device: the old key no longer ties the tile.
    e.setRoster([member("c7", "u7", "d7b"), member("c-me", "u-me", "d-me")]);
    await flush();
    assert.equal(e.verifiedPublisher("c7"), "");
    e.stop();
});

// Review 2b P2-1 (repro removal-latency.mjs adopted): a removal epoch activates
// within REMOVAL_ACTIVATE_DEADLINE_MS of the removal even when the server
// stalls key deliveries to one or all remaining recipients; the sender never
// keeps encrypting under the epoch the removed device holds. Stalled
// recipients are reported as waiting, then get the key when it arrives.
test("a removal epoch activates on a short deadline even when deliveries stall", async () => {
    const { REMOVAL_ACTIVATE_DEADLINE_MS, DEBOUNCE_MS } = await import("../src/media-keys.js");
    // The roster debounce plus the activation deadline: at most 2.2 s after the removal.
    const bound = DEBOUNCE_MS + REMOVAL_ACTIVATE_DEADLINE_MS;
    assert.ok(bound <= 2500, `removal bound ${bound} ms`);
    for (const [label, stalled] of [["none", () => false], ["one", d => d === "rl-d1"], ["all", () => true]]) {
        let now = 1_800_000_000_000 + (label === "none" ? 0 : label === "one" ? 1e9 : 2e9);
        const queue = [];
        const timers = {
            setTimeout(fn, ms) { const t = { at: now + ms, fn }; queue.push(t); return t; },
            clearTimeout(t) { const i = queue.indexOf(t); if (i >= 0) queue.splice(i, 1); },
            setInterval: () => 0, clearInterval: () => {},
        };
        const tick = () => new Promise(r => setImmediate(r));
        const advance = async total => {
            const end = now + total;
            for (;;) {
                // Real async work (WebCrypto digests) settles before virtual time moves.
                for (let i = 0; i < 20; i++) await tick();
                await new Promise(r => setTimeout(r, 5));
                for (let i = 0; i < 20; i++) await tick();
                queue.sort((a, b) => a.at - b.at);
                const next = queue[0];
                if (!next || next.at > end) { now = end; return; }
                queue.shift(); now = Math.max(now, next.at); next.fn();
            }
        };
        const activations = [];
        const releases = [];
        const media = { async setKey() {}, async activate(pid, e) { activations.push({ at: now, pid, e }); }, async retire() {}, async retireOne() {}, async forget() {} };
        let stallOn = false;
        let lastStatus = null;
        const e = new MediaKeyEngine({ media, timers, now: () => now, sendTimeoutMs: 20_000, onStatus: s => { lastStatus = s; },
            send: c => (stallOn && stalled(c.device)) ? new Promise(resolve => releases.push(() => resolve({ ok: true }))) : Promise.resolve({ ok: true }) });
        const m = (c, u, d) => ({ client_id: c, unique_id: u, media_device: d, channel_id: 5 });
        const roster = [m("rl-me", `rl-u-me-${label}`, `rl-d-me-${label}`), ...Array.from({ length: 64 }, (_, i) => m("rl-c" + i, `rl-u${i}-${label}`, i === 1 ? "rl-d1" : `rl-d${i}-${label}`))];
        e.setRoster(roster);
        e.start({ channelID: 5, clientID: "rl-me", principal: `rl-u-me-${label}`, device: `rl-d-me-${label}` });
        await advance(10_000);
        const before = activations.length;
        const oldEpoch = e.activeEpoch;
        stallOn = true;
        const t0 = now;
        e.setRoster(roster.filter(r => r.client_id !== "rl-c0")); // remove one member: 63 remain
        await advance(bound + 100);
        const act = activations.slice(before).find(x => x.pid.endsWith("|mic"));
        assert.ok(act, `${label}: removal epoch never activated`);
        assert.ok(act.at - t0 <= bound, `${label}: activation ${act.at - t0} ms after the removal`);
        assert.ok(e.activeEpoch > oldEpoch, `${label}: still encrypting under the removed device's epoch`);
        const waiting = label === "none" ? 0 : label === "one" ? 1 : 63;
        assert.equal(lastStatus.waiting, waiting, `${label}: waiting recipients shown`);
        // A joiner was never sent the active epoch: it is not counted as waiting.
        e.setRoster([...roster.filter(r => r.client_id !== "rl-c0"), m("rl-cj", `rl-uj-${label}`, `rl-dj-${label}`)]);
        assert.equal(e.status().waiting, waiting, `${label}: a joiner is not waiting for the active epoch`);
        // Late deliveries complete: nobody waits any more.
        stallOn = false;
        for (let wave = 0; wave < 20 && (releases.length || e.status().waiting); wave++) { for (const release of releases.splice(0)) release(); await advance(50); }
        await advance(300);
        // Pushed as deliveries settle (throttled), not only read on demand.
        assert.equal(lastStatus.waiting, 0, `${label}: late deliveries pushed`);
        assert.equal(e.status().waiting, 0, `${label}: late deliveries counted`);
        assert.ok(e.status().keyHolders.length >= 63, `${label}: every remaining device holds the new key`);
        e.stop();
    }
});

// Review 2b fix P2-A/P2-B, parent ruling: for every removed device D (removed
// at tD), the sender encrypts only under an epoch never sent to D no later than
// tD + REMOVAL_BOUND_MS, whatever the server stalls, concurrent rekeys (join,
// publication, renewal) or repeated removals; and D never receives an epoch
// created after its removal. Real engine, virtual clock.
async function removalScenario(name, { members = 20, script }) {
    const { REMOVAL_BOUND_MS } = await import("../src/media-keys.js");
    let now = 1_900_000_000_000 + Math.floor(Math.random() * 1e9);
    const queue = [];
    const timers = {
        setTimeout(fn, ms) { const t = { at: now + ms, fn }; queue.push(t); return t; },
        clearTimeout(t) { const i = queue.indexOf(t); if (i >= 0) queue.splice(i, 1); },
        setInterval(fn, ms) { const t = { at: now + ms, fn: () => { t.at = now + ms; queue.push(t); fn(); }, interval: true }; queue.push(t); return t; },
        clearInterval(t) { const i = queue.indexOf(t); if (i >= 0) queue.splice(i, 1); },
    };
    const tick = () => new Promise(r => setImmediate(r));
    const advance = async total => {
        const end = now + total;
        for (;;) {
            for (let i = 0; i < 10; i++) await tick();
            await new Promise(r => setTimeout(r, 1));
            for (let i = 0; i < 10; i++) await tick();
            queue.sort((a, b) => a.at - b.at);
            const next = queue[0];
            if (!next || next.at > end) { now = end; return; }
            queue.shift(); now = Math.max(now, next.at); next.fn();
        }
    };
    const active = []; // [time, epoch]
    const sends = [];  // { at, device, epoch }
    const media = { async setKey() {}, async activate(pid, e) { if (pid === "self|mic") active.push([now, e]); }, async retire() {}, async retireOne() {}, async forget() {} };
    let stall = () => false;
    const tag = name.replace(/\W+/g, "-");
    const e = new MediaKeyEngine({ media, timers, now: () => now, sendTimeoutMs: 20_000,
        send: c => { sends.push({ at: now, device: c.device, epoch: c.body.epoch }); return stall(c.device) ? new Promise(() => {}) : Promise.resolve({ ok: true }); } });
    const m = i => ({ client_id: `${tag}-c${i}`, unique_id: `${tag}-u${i}`, media_device: `${tag}-d${i}`, channel_id: 5 });
    let roster = [{ client_id: `${tag}-me`, unique_id: `${tag}-u-me`, media_device: `${tag}-d-me`, channel_id: 5 }, ...Array.from({ length: members }, (_, i) => m(i))];
    e.setRoster(roster);
    e.start({ channelID: 5, clientID: `${tag}-me`, principal: `${tag}-u-me`, device: `${tag}-d-me` });
    await advance(10_000);
    const removed = []; // { device, at }
    const ctl = {
        advance, get now() { return now; },
        stall: fn => { stall = fn; },
        join: i => { roster = [...roster, m(i)]; e.setRoster(roster); },
        publish: () => e.publicationsChanged(),
        remove: i => { removed.push({ device: `${tag}-d${i}`, at: now }); roster = roster.filter(r => r.client_id !== `${tag}-c${i}`); e.setRoster(roster); },
    };
    await script(ctl);
    await advance(REMOVAL_BOUND_MS + 500);
    for (const { device, at } of removed) {
        const held = new Set(sends.filter(s => s.device === device).map(s => s.epoch));
        // The active epoch at time t is the last activation at or before t.
        const activeAt = t => { let ep = 0; for (const [time, epoch] of active) if (time <= t) ep = epoch; return ep; };
        const safeFrom = active.find(([time, epoch]) => time >= at && !held.has(epoch));
        assert.ok(safeFrom, `${name}: ${device} — the sender never left its epochs`);
        assert.ok(safeFrom[0] - at <= REMOVAL_BOUND_MS, `${name}: ${device} — off its epochs ${safeFrom[0] - at} ms after its removal (bound ${REMOVAL_BOUND_MS})`);
        for (const [time] of active.filter(([time]) => time >= safeFrom[0])) assert.ok(!held.has(activeAt(time)), `${name}: ${device} — an epoch it holds became active again`);
        assert.ok(!held.has(activeAt(now)), `${name}: ${device} holds the final active epoch`);
        const late = sends.filter(s => s.device === device && s.epoch >= at);
        assert.equal(late.length, 0, `${name}: ${device} received an epoch created after its removal`);
    }
    e.stop();
    return { removed: removed.length, activations: active.length };
}

test("removal bound: idle, during a stalled join rekey (21 and 63 members), during renewal, and in quick succession", async () => {
    // Idle engine, all deliveries stalled.
    await removalScenario("idle", { script: async c => { c.stall(() => true); c.remove(0); } });
    // A join rekey whose deliveries the server stalls is in flight.
    for (const members of [21, 63]) await removalScenario(`join-${members}`, { members, script: async c => { c.stall(() => true); c.join(900); await c.advance(1000); c.remove(0); } });
    // A join rekey in its activation delay (no stalls).
    await removalScenario("join-delay", { script: async c => { c.join(900); await c.advance(400); c.remove(0); } });
    // A publication rekey in flight, deliveries stalled.
    await removalScenario("publication", { script: async c => { c.stall(() => true); c.publish(); await c.advance(700); c.remove(1); } });
    // The 5-minute renewal is distributing (stalled) when the removal lands.
    await removalScenario("renewal", { script: async c => { await c.advance(5 * 60_000 - 10_000 + 50); c.stall(() => true); await c.advance(300); c.remove(2); } });
    // Several devices removed in quick succession (100 ms apart), stalled.
    await removalScenario("burst", { script: async c => { c.stall(() => true); for (let i = 0; i < 6; i++) { c.remove(i); await c.advance(100); } } });
});

test("removal bound holds under removal churn every 1.9 s with all deliveries stalled (long run)", async () => {
    const result = await removalScenario("flapstall", { members: 40, script: async c => {
        await c.advance(4000);
        c.stall(() => true);
        for (let i = 0; i < 35; i++) { c.remove(i); await c.advance(1900); }
    } });
    assert.equal(result.removed, 35);
});

// CodeRabbit (fix2): every control of an epoch carries that epoch's own key
// material, also for retries and after a removal preempted a newer epoch.
test("controls of an epoch always carry that epoch's own keys (retries, preemption)", async () => {
    let failD2 = true;
    let release;
    const gate = new Promise(r => { release = r; });
    const sent = [];
    const media = fakeMedia();
    const e = new MediaKeyEngine({ media, timers: fastTimers, sendTimeoutMs: Infinity, send: async c => {
        sent.push(c);
        if (c.device === "d2" && failD2) return { ok: false, error: { code: "busy" } };
        if (sent.length > 3 && c.device === "d1") await gate; // the publication epoch stalls
        return { ok: true };
    } });
    e.setRoster([member("c1", "u1", "d1"), member("c2", "u2", "d2"), member("c3", "u3", "d3")]);
    e.start({ ...me, principal: "u-mat", device: "d-mat" });
    await flush(40);
    e.publicationsChanged(); await flush(10);   // a newer epoch starts and stalls
    failD2 = false;
    e.setRoster([member("c1", "u1", "d1"), member("c2", "u2", "d2")]); // d3 removed: preempts it
    await flush(80);
    release(); await flush(80);
    const byEpoch = new Map();
    for (const c of sent) {
        const sig = JSON.stringify([c.body.data.kid, c.body.data.keys, c.body.data.n]);
        if (!byEpoch.has(c.body.epoch)) byEpoch.set(c.body.epoch, sig);
        assert.equal(sig, byEpoch.get(c.body.epoch), `epoch ${c.body.epoch} was sent with another epoch's material`);
    }
    assert.ok(byEpoch.size >= 2);
    assert.ok(sent.every(c => c.device !== "d3" || c.body.epoch < e.activeEpoch), "the removed device never gets the newest epoch");
    e.stop();
});
