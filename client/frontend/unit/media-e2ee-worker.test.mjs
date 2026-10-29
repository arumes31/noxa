// Runs the ACTUAL pinned LiveKit worker bundle with the noXa wrapper in
// Node (no browser): real AES-GCM frame encryption/decryption, both
// endpoint entry points (posted streams and onrtctransform), and the wrapper
// policy (epochs, retirement, rebinding, replay, plaintext/codec guards).
import assert from "node:assert/strict";
import { before, test } from "node:test";

globalThis.self = globalThis;
globalThis.onmessage = null;
const replies = [];
globalThis.postMessage = () => {};
await import("livekit-client/e2ee-worker");
const library = globalThis.onmessage;
const { installMediaE2EEWorker } = await import("../src/media-e2ee-core.js");
installMediaE2EEWorker(globalThis, { reply: message => replies.push(message) });

let seq = 0;
function call(op, data = {}) {
    const id = ++seq;
    globalThis.onmessage({ data: { op, seq: id, ...data } });
    return (async () => {
        for (let i = 0; i < 400; i++) {
            const found = replies.find(r => r.seq === id);
            if (found) { replies.splice(replies.indexOf(found), 1); if (!found.ok) throw new Error(found.value); return found.value; }
            await new Promise(r => setTimeout(r, 5));
        }
        throw new Error(`timeout ${op}`);
    })();
}

class Frame {
    constructor(bytes, { kind = "audio", type = "key", ssrc = 1111, timestamp = 0, mime } = {}) {
        this.data = new Uint8Array(bytes).buffer;
        this.timestamp = timestamp;
        if (kind === "video") this.type = type;
        this.meta = { synchronizationSource: ssrc, mimeType: mime ?? (kind === "video" ? "video/VP8" : "audio/opus"), payloadType: kind === "video" ? 96 : 111 };
    }
    getMetadata() { return this.meta; }
}
const clone = frame => Object.assign(Object.create(Frame.prototype), frame, { data: frame.data.slice(0), meta: { ...frame.meta } });

let endpointN = 0;
function endpoint(operation, kind = "audio", via = "post") {
    const id = `${operation === "encode" ? "tx" : "rx"}:t${++endpointN}`;
    let push;
    const readable = new ReadableStream({ start(c) { push = c; } });
    const out = [];
    const writable = new WritableStream({ write(frame) { out.push(frame); } });
    if (via === "script") globalThis.onrtctransform({ transformer: { options: { id, operation, kind }, readable, writable } });
    else globalThis.onmessage({ data: { op: "attach", id, operation, kind, readable, writable } });
    return { id, out, push: frame => push.enqueue(frame) };
}
const settle = () => new Promise(r => setTimeout(r, 60));
const key = fill => new Uint8Array(32).fill(fill);
const audio = (n, ts) => new Frame([0xf8, ...Array.from({ length: n }, (_, i) => (i * 7 + ts) & 255)], { timestamp: ts });

before(async () => { await call("init"); });

async function sender(pid, epoch, kid, material, kind = "audio", via) {
    const tx = endpoint("encode", kind, via);
    await call("bind", { id: tx.id, pid });
    await call("key", { pid, epoch, kid, key: material });
    await call("activate", { pid, epoch });
    return tx;
}

test("both entry points encrypt with the library and decrypt only authenticated frames", async () => {
    for (const via of ["post", "script"]) {
        const tx = await sender(`tx|mic-${via}`, 10, 0, key(1), "audio", via);
        const rx = endpoint("decode", "audio", via);
        await call("bind", { id: rx.id, pid: `c1|mic-${via}` });
        await call("key", { pid: `c1|mic-${via}`, epoch: 10, kid: 0, key: key(1) });
        const plain = [audio(40, 960), audio(41, 1920), audio(42, 2880)];
        for (const f of plain) tx.push(clone(f));
        await settle();
        assert.equal(tx.out.length, 3, via);
        for (const [i, f] of tx.out.entries()) {
            const bytes = new Uint8Array(f.data);
            assert.equal(bytes.length, plain[i].data.byteLength + 30);
            assert.equal(bytes[0], 0xf8, "Opus TOC stays clear");
            assert.notDeepEqual(bytes.slice(1, 41), new Uint8Array(plain[i].data).slice(1, 41), "payload is encrypted");
        }
        for (const f of tx.out) rx.push(clone(f));
        await settle();
        assert.deepEqual(rx.out.map(f => [...new Uint8Array(f.data)]), plain.map(f => [...new Uint8Array(f.data)]), via);
    }
});

test("tampering, replay, plaintext injection and a foreign scope key are rejected", async () => {
    const tx = await sender("tx|cam-a", 20, 1, key(2), "video");
    const rx = endpoint("decode", "video");
    await call("bind", { id: rx.id, pid: "c2|cam" });
    await call("key", { pid: "c2|cam", epoch: 20, kid: 1, key: key(2) });
    const frames = [0, 1, 2].map(i => new Frame(Array.from({ length: 300 }, (_, j) => (j + i) & 255), { kind: "video", type: i ? "delta" : "key", timestamp: 3000 * (i + 1) }));
    for (const f of frames) tx.push(clone(f));
    await settle();
    assert.equal(tx.out.length, 3);
    const keyframe = new Uint8Array(tx.out[0].data);
    assert.deepEqual(keyframe.slice(0, 10), new Uint8Array(frames[0].data).slice(0, 10), "VP8 keyframe header stays clear for SFU bounds");
    const before = (await call("stats", { id: rx.id })).endpoint.dropped;
    const tampered = clone(tx.out[1]); new Uint8Array(tampered.data)[20] ^= 1;
    rx.push(tampered);
    rx.push(clone(tx.out[0])); rx.push(clone(tx.out[0]));
    rx.push(new Frame(Array.from({ length: 300 }, (_, j) => j & 255), { kind: "video", timestamp: 9000 }));
    await settle();
    assert.equal(rx.out.length, 1, "only the first copy of the authentic keyframe passes");
    const dropped = (await call("stats", { id: rx.id })).endpoint.dropped;
    assert.equal((dropped.replay || 0) - (before.replay || 0), 1);
    assert.equal((dropped.plaintext || 0) - (before.plaintext || 0), 1);
    // A receiver of another publication holds a different key: nothing decrypts.
    const other = endpoint("decode", "video");
    await call("bind", { id: other.id, pid: "c3|cam" });
    await call("key", { pid: "c3|cam", epoch: 20, kid: 1, key: key(3) });
    other.push(clone(tx.out[2]));
    await settle();
    assert.equal(other.out.length, 0);
});

test("retired epochs never decrypt or reinstall, and rotation keeps media flowing", async () => {
    const pid = "tx|screen-r";
    const tx = await sender(pid, 100, 0, key(4), "audio");
    const rx = endpoint("decode", "audio");
    await call("bind", { id: rx.id, pid: "c4|screenaudio" });
    await call("key", { pid: "c4|screenaudio", epoch: 100, kid: 0, key: key(4) });
    tx.push(audio(30, 960)); await settle();
    const oldFrame = tx.out[0];
    // rotate: new epoch at the next slot, activate, retire the old one
    await call("key", { pid, epoch: 101, kid: 1, key: key(5) });
    await call("key", { pid: "c4|screenaudio", epoch: 101, kid: 1, key: key(5) });
    await call("activate", { pid, epoch: 101 });
    await call("retire", { pid, through: 100 });
    tx.push(audio(30, 1920)); await settle();
    assert.equal(new Uint8Array(tx.out[1].data).at(-1), 1, "new frames use the new slot");
    await call("retire", { pid: "c4|screenaudio", through: 100 });
    rx.push(clone(oldFrame)); rx.push(clone(tx.out[1])); await settle();
    assert.equal(rx.out.length, 1, "the retired epoch's frame is dropped, the new one decrypts");
    await assert.rejects(call("key", { pid: "c4|screenaudio", epoch: 100, kid: 0, key: key(4) }), /retired/);
    await assert.rejects(call("key", { pid: "c4|screenaudio", epoch: 99, kid: 2, key: key(9) }), /retired/);
    await assert.rejects(call("key", { pid: "c4|screenaudio", epoch: 101, kid: 1, key: key(6) }), /conflicting/);
    // forget keeps the retirement watermark: an old control cannot restore it
    await call("forget", { pid: "c4|screenaudio" });
    await assert.rejects(call("key", { pid: "c4|screenaudio", epoch: 101, kid: 1, key: key(5) }), /retired/);
});

test("a rebound receiver (same MID, new publisher) never outputs the former publisher", async () => {
    const a = await sender("tx|a", 7, 0, key(7));
    const b = await sender("tx|b", 8, 0, key(8));
    a.push(audio(20, 960)); b.push(audio(20, 960)); await settle();
    const rx = endpoint("decode", "audio");
    await call("key", { pid: "pa|mic", epoch: 7, kid: 0, key: key(7) });
    await call("key", { pid: "pb|mic", epoch: 8, kid: 0, key: key(8) });
    await call("bind", { id: rx.id, pid: "pa|mic" });
    rx.push(clone(a.out[0])); await settle();
    assert.equal(rx.out.length, 1);
    await call("bind", { id: rx.id, pid: "pb|mic" });
    rx.push(clone(a.out[0])); rx.push(clone(b.out[0])); await settle();
    assert.equal(rx.out.length, 2, "only publisher B's frame passes after rebinding");
    assert.deepEqual([...new Uint8Array(rx.out[1].data)], [...new Uint8Array(audio(20, 960).data)]);
});

test("senders never emit plaintext: no key, unsupported codec, or a library that stops encrypting", async () => {
    const tx = endpoint("encode", "audio");
    await call("bind", { id: tx.id, pid: "tx|nokey" });
    tx.push(audio(10, 960)); await settle();
    assert.equal(tx.out.length, 0, "no active key: nothing leaves");
    const video = await sender("tx|h264", 5, 0, key(10), "video");
    video.push(new Frame([0, 0, 0, 1, 0x65, 1, 2, 3, 4, 5, 6, 7], { kind: "video", mime: "video/H264" })); await settle();
    assert.equal(video.out.length, 0, "H.264 is refused until proven through the SFU");
    const guarded = await sender("tx|guard", 6, 0, key(11));
    // Simulate the library switching this endpoint to passthrough (a state
    // the page cannot request): the wrapper still drops the output.
    library({ data: { kind: "enable", data: { participantIdentity: `ep:${guarded.id}`, enabled: false } } });
    await settle();
    guarded.push(audio(10, 960)); await settle();
    assert.equal(guarded.out.length, 0);
    assert.ok((await call("stats", { id: guarded.id })).endpoint.dropped.unencrypted >= 1);
    const unbound = endpoint("decode", "audio");
    unbound.push(audio(40, 960)); await settle();
    assert.equal(unbound.out.length, 0, "an unbound receiver outputs nothing");
});

test("invalid operations are refused and an endpoint attaches once", async () => {
    await assert.rejects(call("key", { pid: "bad pid", epoch: 1, kid: 0, key: key(1) }), /invalid/);
    await assert.rejects(call("key", { pid: "x|mic", epoch: 0, kid: 0, key: key(1) }), /invalid/);
    await assert.rejects(call("key", { pid: "x|mic", epoch: 1, kid: 16, key: key(1) }), /invalid/);
    await assert.rejects(call("nope"), /unknown/);
    const ep = endpoint("decode");
    const id = ++seq;
    globalThis.onmessage({ data: { op: "attach", seq: id, id: ep.id, operation: "decode", kind: "audio", readable: new ReadableStream(), writable: new WritableStream() } });
    assert.match(replies.find(r => r.seq === id).value, /already attached/);
});

// Review 2a-B P1-2: equal (kid, epoch) across publications must never keep
// the former publication's key after a rebind.
test("rebinding with a colliding (kid, epoch) outputs only the new publication", async () => {
    for (const [from, to] of [["pa2|mic", "pb2|mic"], ["px|screen", "px|cam"]]) {
        const kind = from.endsWith("mic") ? "audio" : "video";
        const a = await sender(`tx|${from}`, 7, 0, key(0x41), kind);
        const b = await sender(`tx|${to}`, 7, 0, key(0x42), kind);
        const frameA = kind === "audio" ? audio(20, 960) : new Frame(Array.from({ length: 200 }, (_, j) => j & 255), { kind, timestamp: 3000 });
        const frameB = kind === "audio" ? audio(21, 1000) : new Frame(Array.from({ length: 220 }, (_, j) => (j * 3) & 255), { kind, timestamp: 3000 });
        a.push(clone(frameA)); b.push(clone(frameB)); await settle();
        const rx = endpoint("decode", kind);
        await call("key", { pid: from, epoch: 7, kid: 0, key: key(0x41) });
        await call("key", { pid: to, epoch: 7, kid: 0, key: key(0x42) });
        await call("bind", { id: rx.id, pid: from });
        rx.push(clone(a.out[0])); await settle();
        assert.equal(rx.out.length, 1);
        await call("bind", { id: rx.id, pid: to });
        rx.push(clone(a.out[0])); rx.push(clone(b.out[0])); await settle();
        assert.equal(rx.out.length, 2, `${from} -> ${to}: exactly one frame after rebind`);
        assert.deepEqual([...new Uint8Array(rx.out[1].data)], [...new Uint8Array(frameB.data)], "and it is the new publication's");
    }
});

test("an older epoch can never evict a newer live key in the same slot", async () => {
    await call("key", { pid: "ev|mic", epoch: 50, kid: 4, key: key(50) });
    await assert.rejects(call("key", { pid: "ev|mic", epoch: 40, kid: 4, key: key(40) }), /newer media epoch/);
    const st = await call("stats");
    assert.ok(st.pids > 0);
});

test("a script-API bind that arrives before the endpoint attaches is applied on attach", async () => {
    const id = "rx:early1";
    const tx = await sender("tx|early", 9, 0, key(0x51));
    tx.push(audio(30, 960)); await settle();
    await call("key", { pid: "early|mic", epoch: 9, kid: 0, key: key(0x51) });
    assert.deepEqual(await call("bind", { id, pid: "early|mic" }), { pending: true });
    let push; const out = [];
    globalThis.onrtctransform({ transformer: { options: { id, operation: "decode", kind: "audio" },
        readable: new ReadableStream({ start(c) { push = c; } }), writable: new WritableStream({ write(f) { out.push(f); } }) } });
    await settle();
    push.enqueue(clone(tx.out[0])); await settle();
    assert.equal(out.length, 1);
});

test("a receive epoch older than the maximum age stops decrypting", async () => {
    const tx = await sender("tx|aged", 11, 0, key(0x61));
    tx.push(audio(30, 960)); tx.push(audio(31, 1920)); await settle();
    const rx = endpoint("decode", "audio");
    await call("key", { pid: "aged|mic", epoch: 11, kid: 0, key: key(0x61) });
    await call("bind", { id: rx.id, pid: "aged|mic" });
    rx.push(clone(tx.out[0])); await settle();
    assert.equal(rx.out.length, 1);
    const realNow = performance.now.bind(performance), realDate = Date.now;
    performance.now = () => realNow() + 13 * 60 * 1000;
    Date.now = () => realDate() - 60 * 60 * 1000; // a backwards wall-clock step changes nothing
    try { rx.push(clone(tx.out[1])); await settle(); } finally { performance.now = realNow; Date.now = realDate; }
    assert.equal(rx.out.length, 1);
    assert.ok((await call("stats", { id: rx.id })).endpoint.dropped.stale >= 1);
});

test("retireOne removes only that epoch; an older live epoch keeps decrypting", async () => {
    const tx = await sender("tx|one", 30, 0, key(0x71));
    tx.push(audio(30, 960)); await settle();
    const rx = endpoint("decode", "audio");
    await call("key", { pid: "one|mic", epoch: 30, kid: 0, key: key(0x71) });
    await call("key", { pid: "one|mic", epoch: 31, kid: 1, key: key(0x72) });
    await call("bind", { id: rx.id, pid: "one|mic" });
    await call("retireOne", { pid: "one|mic", epoch: 31 });
    rx.push(clone(tx.out[0])); await settle();
    assert.equal(rx.out.length, 1, "epoch 30 still decrypts");
    await assert.rejects(call("key", { pid: "one|mic", epoch: 31, kid: 1, key: key(0x72) }), /retired/);
});
