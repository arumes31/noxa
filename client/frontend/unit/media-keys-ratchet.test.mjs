// Join ratchet: a join never interrupts existing listeners, and a joiner is
// heard (and hears) as soon as its single key control lands.
import assert from "node:assert/strict";
import { test } from "node:test";
import { MediaKeyEngine, buildMediaKeyBody, parseMediaKeyControl, ratchetKeys, sessionFeatures, RATCHET_FEATURE, SWITCH_GRACE_MS } from "../src/media-keys.js";

const flush = async (n = 30) => { for (let i = 0; i < n; i++) await new Promise(r => setTimeout(r, 0)); };
const fastTimers = { setTimeout: (fn) => setTimeout(fn, 0), clearTimeout, setInterval: () => 0, clearInterval: () => {} };
const SLOTS = ["mic", "cam", "screen", "screenaudio"];
const unb64 = t => [...Uint8Array.from(atob(t), c => c.charCodeAt(0))];
const keysOf = body => Object.fromEntries(SLOTS.map(s => [s, Uint8Array.from(unb64(body.data.keys[s]))]));
const TAGGED = `x.${RATCHET_FEATURE}`;

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
        // Retirement removes key material like the real worker does, so a key
        // still present in `keys` really is usable.
        async retire(pid, through) {
            retired.set(pid, Math.max(retired.get(pid) || 0, through)); log.push(["retire", pid, through]);
            for (const k of [...keys.keys()]) { const [p, e] = k.split("#"); if (p === pid && Number(e) <= through) keys.delete(k); }
        },
        async retireOne(pid, epoch) { keys.delete(`${pid}#${epoch}`); log.push(["retireOne", pid, epoch]); },
        async forget(pid) { for (const k of [...keys.keys()]) if (k.split("#")[0] === pid) keys.delete(k); log.push(["forget", pid]); },
    };
}

const member = (client, principal, device, channel = 5) => ({ client_id: client, unique_id: principal, media_device: device, channel_id: channel });
const me = { channelID: 5, clientID: "c-me", principal: "u-me", device: "d-me" };

function control(epoch, { client = "c1", sender = "u1", device = "d1", kid = 0, fill = 1, session = "s1" } = {}) {
    const keys = Object.fromEntries(SLOTS.map((s, i) => [s, new Uint8Array(32).fill(fill + i)]));
    const body = buildMediaKeyBody({ channelID: 5, epoch, nowSeconds: 1, client, session, kid, members: "m", count: 2, keys });
    return { sender, senderDevice: device, logicalID: "l", ...body, expiresAt: body.expires_at };
}

// A recipient announces ratchet support through the session tag of its own control.
const announce = (e, client, principal, device, epoch, session = TAGGED) => e.receive(control(epoch, { client, sender: principal, device, session }));

// Deliveries can be held open to prove that activation does not wait for them.
function gatedEngine({ media = fakeMedia(), timers = fastTimers } = {}) {
    const sent = [];
    let gate = null;
    const e = new MediaKeyEngine({ media, timers, sendTimeoutMs: Infinity, activateMaxWaitMs: Infinity,
        send: async c => { sent.push(c); if (gate) await gate.promise; return { ok: true }; } });
    const hold = () => { let release; const promise = new Promise(r => { release = r; }); gate = { promise, release }; return gate; };
    return { e, media, sent, hold };
}

const activated = (media, epoch) => media.log.some(([op, pid, ep]) => op === "activate" && pid === "self|mic" && ep === epoch);

test("own controls announce ratchet support in a backward-compatible session tag", async () => {
    const { e, sent } = gatedEngine();
    e.setRoster([member("c1", "u1", "d1")]);
    e.start({ ...me, principal: "u-tag", device: "d-tag" });
    await flush();
    assert.deepEqual(sessionFeatures(sent[0].body.data.session), [RATCHET_FEATURE]);
    assert.equal(parseMediaKeyControl(sent[0].body, 5).ok, true, "still a valid v1 control for older receivers");
    e.stop();
});

test("a join with ratchet-capable listeners switches at once to the one-way successor epoch", async () => {
    const { e, media, sent, hold } = gatedEngine();
    const roster = [member("c1", "u1", "d1"), member("c2", "u2", "d2")];
    e.setRoster(roster);
    e.start({ ...me, principal: "u-rat", device: "d-rat" });
    await flush();
    announce(e, "c1", "u1", "d1", 8_000_000_000_000); announce(e, "c2", "u2", "d2", 8_000_000_000_000);
    await flush();
    const first = sent[0].body;
    const gate = hold(); // deliveries of the join epoch stay pending
    e.setRoster([...roster, member("c3", "u3", "d3")]);
    await flush();
    const join = sent.filter(c => c.body.epoch !== first.epoch);
    assert.deepEqual(join.map(c => c.device).sort(), ["d1", "d2", "d3"], "everyone current gets the join epoch, the joiner included");
    const body = join[0].body;
    assert.equal(body.epoch, first.epoch + 1, "successor numbering");
    assert.equal(body.data.kid, (first.data.kid + 1) % 16);
    const expected = await ratchetKeys(keysOf(first));
    for (const s of SLOTS) assert.deepEqual(unb64(body.data.keys[s]), [...expected[s]], `HKDF successor for ${s}`);
    assert.notDeepEqual(unb64(body.data.keys.mic), unb64(first.data.keys.mic));
    assert.ok(activated(media, body.epoch), "activated without waiting for any delivery");
    assert.equal(e.status().epoch, body.epoch);
    gate.release(); await flush();
    e.stop();
});

test("a join falls back to a fresh, waiting epoch when a listener never announced ratchet support", async () => {
    const { e, media, sent, hold } = gatedEngine();
    const roster = [member("c1", "u1", "d1"), member("c2", "u2", "d2")];
    e.setRoster(roster);
    e.start({ ...me, principal: "u-rat2", device: "d-rat2" });
    await flush();
    announce(e, "c1", "u1", "d1", 8_100_000_000_000);
    announce(e, "c2", "u2", "d2", 8_100_000_000_000, "legacy-session"); // an older client
    await flush();
    const first = sent[0].body.epoch;
    const gate = hold();
    e.setRoster([...roster, member("c3", "u3", "d3")]);
    await flush();
    const join = sent.find(c => c.body.epoch !== first).body;
    assert.ok(join.epoch >= first + 2, "fresh epochs never take the reserved successor number");
    assert.ok(!activated(media, join.epoch), "a fresh replacement still waits for its deliveries");
    gate.release(); await flush();
    assert.ok(activated(media, join.epoch));
    e.stop();
});

test("a removal always starts a fresh chain; later joins ratchet only from it", async () => {
    const { e, sent } = gatedEngine();
    const a = member("c1", "u1", "d1"), b = member("c2", "u2", "d2"), c = member("c3", "u3", "d3");
    e.setRoster([a, b]);
    e.start({ ...me, principal: "u-rat3", device: "d-rat3" });
    await flush();
    for (const [m, p, d] of [["c1", "u1", "d1"], ["c2", "u2", "d2"], ["c3", "u3", "d3"]]) announce(e, m, p, d, 8_200_000_000_000);
    await flush();
    e.setRoster([a, b, c]); await flush(); // join: ratchet
    const ratchet = sent.at(-1).body;
    e.setRoster([a, c]); await flush(80); // d2 leaves; it holds the chain
    const removal = sent.at(-1).body;
    assert.ok(removal.epoch >= ratchet.epoch + 2, "fresh after a removal");
    assert.equal(removal.data.kid, (ratchet.data.kid + 2) % 16, "skips the key slot of the successor the removed device pre-derived");
    const derived = await ratchetKeys(keysOf(ratchet));
    assert.notDeepEqual(unb64(removal.data.keys.mic), [...derived.mic], "not derivable by the removed device");
    assert.equal(sent.filter(x => x.device === "d2" && x.body.epoch >= removal.epoch).length, 0, "the departed device gets nothing newer");
    e.setRoster([a, c, b]); await flush(80); // d2 rejoins: a ratchet of the new chain
    assert.equal(sent.at(-1).body.epoch, removal.epoch + 1);
    e.stop();
});

test("a join seen together with a removal is fresh, never a ratchet", async () => {
    const { e, sent } = gatedEngine();
    const a = member("c1", "u1", "d1"), b = member("c2", "u2", "d2"), c = member("c3", "u3", "d3");
    e.setRoster([a, b]);
    e.start({ ...me, principal: "u-rat4", device: "d-rat4" });
    await flush();
    for (const [m, p, d] of [["c1", "u1", "d1"], ["c2", "u2", "d2"]]) announce(e, m, p, d, 8_300_000_000_000);
    await flush();
    const first = sent[0].body.epoch;
    e.setRoster([a, b, c]); // join ...
    e.setRoster([a, c]);    // ... and a leave inside the same debounce window
    await flush(80);
    const later = sent.filter(x => x.body.epoch > first);
    assert.ok(later.length && later.every(x => x.body.epoch >= first + 2), `no successor epoch: ${later.map(x => x.body.epoch - first)}`);
    assert.equal(later.filter(x => x.device === "d2").length, 0);
    e.stop();
});

test("a receiver pre-installs the successor of a ratchet-capable sender and drops it on a fresh epoch", async () => {
    const { e, media } = gatedEngine();
    e.setRoster([member("c1", "u1", "d1"), member("c-me", "u-me", "d-me")]);
    e.start(me);
    await flush();
    const E = 9_000_000_000_000;
    assert.equal(e.receive(control(E, { kid: 3, session: TAGGED })).ok, true);
    await flush(60);
    const next = media.keys.get(`c1|mic#${E + 1}`);
    assert.ok(next, "successor installed before the sender switches");
    assert.equal(next.kid, 4);
    const expected = await ratchetKeys(Object.fromEntries(SLOTS.map((s, i) => [s, new Uint8Array(32).fill(1 + i)])));
    assert.deepEqual(next.key, [...expected.mic]);
    assert.equal(e.receive({ ...control(E + 1, { kid: 4, session: TAGGED }), data: { ...control(E + 1, { session: TAGGED }).data, kid: 4,
        keys: Object.fromEntries(SLOTS.map(s => [s, btoa(String.fromCharCode(...expected[s]))])) } }).ok, true, "the sender's own control for it is accepted");
    await flush(60);
    assert.ok(media.keys.has(`c1|mic#${E + 2}`), "and the chain advances");
    assert.equal(e.receive(control(E + 50, { kid: 5, fill: 9, session: TAGGED })).ok, true);
    await flush(60);
    assert.ok(media.log.some(([op, pid, ep]) => op === "retireOne" && pid === "c1|mic" && ep === E + 2), "the unused successor is retired by a fresh epoch");
    e.stop();
});

test("a receiver does not pre-derive for senders without ratchet support", async () => {
    const { e, media } = gatedEngine();
    e.setRoster([member("c1", "u1", "d1"), member("c-me", "u-me", "d-me")]);
    e.start(me);
    await flush();
    const E = 9_100_000_000_000;
    e.receive(control(E, { session: "legacy" }));
    await flush(60);
    assert.ok(media.keys.has(`c1|mic#${E}`));
    assert.equal(media.keys.has(`c1|mic#${E + 1}`), false, "an older sender may number a fresh epoch e+1");
    e.stop();
});

test("a receiver keeps the previous epoch until the sender has certainly switched", async () => {
    const delays = [];
    const timers = { setTimeout: (fn, ms) => { delays.push(ms); return setTimeout(fn, 0); }, clearTimeout, setInterval: () => 0, clearInterval: () => {} };
    const { e } = gatedEngine({ timers });
    e.setRoster([member("c1", "u1", "d1"), member("c-me", "u-me", "d-me")]);
    e.start(me);
    await flush();
    e.receive(control(9_200_000_000_000));
    await flush();
    delays.length = 0;
    e.receive(control(9_200_000_000_100, { kid: 1 }));
    await flush();
    assert.ok(delays.includes(SWITCH_GRACE_MS), `previous epoch retires after the switch grace: ${delays}`);
    assert.ok(SWITCH_GRACE_MS >= 8_000 + 2_500 + 10_000, "covers the sender's longest activation wait");
    e.stop();
});

test("end to end: a listener already holds the key a sender switches to on a join", async () => {
    const sender = { channelID: 5, clientID: "c-s", principal: "u-s", device: "d-s" };
    const listener = { channelID: 5, clientID: "c-l", principal: "u-l", device: "d-l" };
    const ms = fakeMedia(), ml = fakeMedia();
    const route = new Map();
    const deliver = from => async c => {
        const peer = route.get(c.device);
        if (peer) peer.receive({ sender: from.principal, senderDevice: from.device, ...c.body, expiresAt: c.body.expires_at });
        return { ok: true };
    };
    const S = new MediaKeyEngine({ media: ms, timers: fastTimers, sendTimeoutMs: Infinity, send: deliver(sender) });
    const L = new MediaKeyEngine({ media: ml, timers: fastTimers, sendTimeoutMs: Infinity, send: deliver(listener) });
    route.set("d-s", S); route.set("d-l", L);
    const roster = [member("c-s", "u-s", "d-s"), member("c-l", "u-l", "d-l")];
    S.setRoster(roster); L.setRoster(roster);
    S.start(sender); L.start(listener);
    await flush(80);
    route.delete("d-l"); // the listener's copy of the join epoch is delayed indefinitely
    const joined = [...roster, member("c-j", "u-j", "d-j")];
    S.setRoster(joined); L.setRoster(joined);
    await flush(80);
    const epoch = S.status().epoch;
    const own = ms.keys.get(`self|mic#${epoch}`);
    const held = ml.keys.get(`c-s|mic#${epoch}`);
    assert.ok(activated(ms, epoch));
    assert.ok(held && own, "the listener can decrypt the epoch the sender just switched to");
    assert.deepEqual(held.key, own.key);
    assert.equal(held.kid, own.kid);
    S.stop(); L.stop();
});

test("a back-to-back join waits for the previous ratchet to land: fresh epoch instead of a second ratchet", async () => {
    const { e, sent, hold } = gatedEngine();
    const a = member("c1", "u1", "d1"), b = member("c2", "u2", "d2");
    e.setRoster([a]);
    e.start({ ...me, principal: "u-b2b", device: "d-b2b" });
    await flush();
    announce(e, "c1", "u1", "d1", 9_400_000_000_000); announce(e, "c2", "u2", "d2", 9_400_000_000_000);
    await flush();
    const first = sent[0].body.epoch;
    const gate = hold(); // the first ratchet's controls are not delivered yet
    e.setRoster([a, b]); await flush();
    const ratchet = sent.at(-1).body.epoch;
    assert.equal(ratchet, first + 1);
    e.setRoster([a, b, member("c3", "u3", "d3")]); await flush();
    const next = sent.at(-1).body.epoch;
    assert.ok(next >= ratchet + 2, "d1 may not have pre-derived the next successor yet: fresh");
    gate.release(); await flush(80);
    e.stop();
});

test("a pending renewal is never turned into a ratchet by a join in the same window", async () => {
    let tick = null;
    const timers = { setTimeout: fn => setTimeout(fn, 0), clearTimeout, setInterval: fn => { tick = fn; return 1; }, clearInterval: () => {} };
    const { e, sent } = gatedEngine({ timers });
    const a = member("c1", "u1", "d1");
    e.setRoster([a]);
    e.start({ ...me, principal: "u-renew", device: "d-renew" });
    await flush();
    announce(e, "c1", "u1", "d1", 9_500_000_000_000); announce(e, "c2", "u2", "d2", 9_500_000_000_000);
    await flush();
    const first = sent[0].body.epoch;
    tick();                                   // renewal pending ...
    e.setRoster([a, member("c2", "u2", "d2")]); // ... and a join coalesces into it
    await flush(80);
    const later = sent.filter(x => x.body.epoch > first);
    assert.ok(later.length && later.every(x => x.body.epoch >= first + 2), "renewal stays a fresh random epoch");
    e.stop();
});

test("listeners that pre-derived a ratchet epoch count as key holders at once", async () => {
    const { e, sent, hold } = gatedEngine();
    const a = member("c1", "u1", "d1");
    e.setRoster([a]);
    e.start({ ...me, principal: "u-inh", device: "d-inh" });
    await flush();
    announce(e, "c1", "u1", "d1", 9_600_000_000_000);
    await flush();
    const gate = hold();
    e.setRoster([a, member("c2", "u2", "d2")]); await flush();
    assert.equal(sent.at(-1).body.epoch, sent[0].body.epoch + 1);
    const st = e.status();
    assert.deepEqual(st.keyHolders.map(k => k.device), ["d1"], "d1 already holds the successor");
    assert.equal(st.waiting, 1, "only the joiner waits");
    assert.equal(st.participants.find(p => p.clientID === "c1").sent, true);
    gate.release(); await flush();
    e.stop();
});

test("a failed delivery is retried once the epoch is active, before the rest of the distribution settles", async () => {
    const media = fakeMedia();
    const sent = [];
    let release;
    const slow = new Promise(r => { release = r; });
    const e = new MediaKeyEngine({ media, timers: fastTimers, sendTimeoutMs: Infinity, activateMaxWaitMs: Infinity,
        send: async c => {
            sent.push(c);
            if (c.device === "d1" && sent.filter(x => x.device === "d1").length === 1) throw Object.assign(new Error("x"), { code: "device_unavailable" });
            if (c.device === "d2") await slow; // never settles during the check
            return { ok: true };
        } });
    e.setRoster([member("c1", "u1", "d1"), member("c2", "u2", "d2")]);
    e.start({ ...me, principal: "u-retry", device: "d-retry" });
    await flush(80);
    assert.ok(sent.filter(c => c.device === "d1").length >= 2, "d1 retried while d2 is still pending");
    release(); await flush();
    e.stop();
});

test("a legacy session sharing a ratchet-capable device blocks the ratchet", async () => {
    const { e, sent } = gatedEngine();
    const a = member("c1", "u1", "d1"), twin = member("c1b", "u1", "d1");
    e.setRoster([a, twin]);
    e.start({ ...me, principal: "u-twin", device: "d-twin" });
    await flush();
    announce(e, "c1", "u1", "d1", 9_700_000_000_000);
    announce(e, "c1b", "u1", "d1", 9_700_000_000_001, "legacy");
    await flush();
    const first = sent[0].body.epoch;
    e.setRoster([a, twin, member("c2", "u2", "d2")]); await flush(80);
    assert.ok(sent.at(-1).body.epoch >= first + 2, "fresh: the legacy session did not pre-derive");
    e.stop();
});
