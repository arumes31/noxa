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
    assert.ok(delays.includes(1000) && !delays.includes(2500), `removal is urgent: ${delays}`);
    e.stop();
});
