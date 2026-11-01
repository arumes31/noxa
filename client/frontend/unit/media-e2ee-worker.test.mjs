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
const libraryTrackIDs = new Set();
globalThis.onmessage = event => {
    if (["encode", "decode"].includes(event.data?.kind)) libraryTrackIDs.add(event.data.data.trackId);
    return library(event);
};
const { installMediaE2EEWorker } = await import("../src/media-e2ee-core.js");
const installed = installMediaE2EEWorker(globalThis, { reply: message => replies.push(message) });

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
function endpoint(operation, kind = "audio", via = "post", hardwareRequired = false) {
    const id = `${operation === "encode" ? "tx" : "rx"}:t${++endpointN}`;
    let push, cancelled = false;
    const readable = new ReadableStream({ start(c) { push = c; }, cancel() {cancelled = true;} });
    const out = [];
    const writable = new WritableStream({ write(frame) { out.push(frame); } });
    if (via === "script") globalThis.onrtctransform({ transformer: { options: { id, operation, kind, hardwareRequired }, readable, writable } });
    else globalThis.onmessage({ data: { op: "attach", id, operation, kind, readable, writable, hardwareRequired } });
    return { id, out, push: frame => push.enqueue(frame), cancelled: () => cancelled };
}
const settle = () => new Promise(r => setTimeout(r, 60));
async function waitFrames(endpoint, count) {
    const deadline = performance.now() + 3000;
    while (endpoint.out.length < count && performance.now() < deadline) await new Promise(r => setTimeout(r, 5));
    // Preserve the observation window for assertions that no extra output
    // appeared (tamper/replay/hardware-proof failures reuse the prior count).
    await settle();
}
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

test("idle library slots safely change between Opus/H264 and encode/decode", async () => {
    const audioTX=await sender("types|mic",100001,0,key(62));
    const audioRX=endpoint("decode");
    await call("bind",{id:audioRX.id,pid:"types-peer|mic"});await call("key",{pid:"types-peer|mic",epoch:100001,kid:0,key:key(62)});
    audioTX.push(audio(40,960));await waitFrames(audioTX,1);audioRX.push(clone(audioTX.out[0]));await waitFrames(audioRX,1);
    assert.equal(audioRX.out.length,1);
    const txSlot=installed.endpoints.get(audioTX.id).libraryID, rxSlot=installed.endpoints.get(audioRX.id).libraryID;
    await call("detach",{id:audioTX.id});await call("detach",{id:audioRX.id});
    const videoRX=endpoint("decode","video","script");
    const videoTX=await sender("types|cam",100002,1,key(63),"video","script");
    assert.equal(installed.endpoints.get(videoRX.id).libraryID,txSlot,"encode/audio becomes decode/video");
    assert.equal(installed.endpoints.get(videoTX.id).libraryID,rxSlot,"decode/audio becomes encode/video");
    await call("bind",{id:videoRX.id,pid:"types-peer|cam"});await call("key",{pid:"types-peer|cam",epoch:100002,kid:1,key:key(63)});
    const frame=new Frame([0,0,0,1,0x67,0x42,0xe0,0x1f,0xab,0,0,0,1,0x68,0xce,0x3c,0x80,0,0,0,1,0x65,0x88,...new Array(100).fill(0x55)],{kind:"video",mime:"video/H264",timestamp:18000});
    videoTX.push(clone(frame));await waitFrames(videoTX,1);videoRX.push(clone(videoTX.out[0]));await waitFrames(videoRX,1);
    assert.deepEqual([...new Uint8Array(videoRX.out[0].data)],[...new Uint8Array(frame.data)]);
    await call("detach",{id:videoRX.id});await call("detach",{id:videoTX.id});
    const resumedTX=await sender("types2|mic",100003,2,key(64));const resumedRX=endpoint("decode");
    await call("bind",{id:resumedRX.id,pid:"types2-peer|mic"});await call("key",{pid:"types2-peer|mic",epoch:100003,kid:2,key:key(64)});
    resumedTX.push(audio(40,1920));await waitFrames(resumedTX,1);resumedRX.push(clone(resumedTX.out[0]));await waitFrames(resumedRX,1);
    assert.deepEqual([...new Uint8Array(resumedRX.out[0].data)],[...new Uint8Array(audio(40,1920).data)]);
    assert.equal((await call("stats")).librarySlots,2);assert.equal(libraryTrackIDs.size,2);
    await call("detach",{id:resumedTX.id});await call("detach",{id:resumedRX.id});
});

test("both entry points encrypt with the library and decrypt only authenticated frames", async () => {
    for (const via of ["post", "script"]) {
        const tx = await sender(`tx|mic-${via}`, 10, 0, key(1), "audio", via);
        const rx = endpoint("decode", "audio", via);
        await call("bind", { id: rx.id, pid: `c1|mic-${via}` });
        await call("key", { pid: `c1|mic-${via}`, epoch: 10, kid: 0, key: key(1) });
        const plain = [audio(40, 960), audio(41, 1920), audio(42, 2880)];
        for (const f of plain) tx.push(clone(f));
        await waitFrames(tx, 3);
        assert.equal(tx.out.length, 3, via);
        for (const [i, f] of tx.out.entries()) {
            const bytes = new Uint8Array(f.data);
            assert.equal(bytes.length, plain[i].data.byteLength + 30);
            assert.equal(bytes[0], 0xf8, "Opus TOC stays clear");
            assert.notDeepEqual(bytes.slice(1, 41), new Uint8Array(plain[i].data).slice(1, 41), "payload is encrypted");
        }
        for (const f of tx.out) rx.push(clone(f));
        await waitFrames(rx, 3);
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
    await waitFrames(tx, 3);
    assert.equal(tx.out.length, 3);
    const keyframe = new Uint8Array(tx.out[0].data);
    assert.deepEqual(keyframe.slice(0, 10), new Uint8Array(frames[0].data).slice(0, 10), "VP8 keyframe header stays clear for SFU bounds");
    const before = (await call("stats", { id: rx.id })).endpoint.dropped;
    const tampered = clone(tx.out[1]); new Uint8Array(tampered.data)[20] ^= 1;
    rx.push(tampered);
    rx.push(clone(tx.out[0])); rx.push(clone(tx.out[0]));
    rx.push(new Frame(Array.from({ length: 300 }, (_, j) => j & 255), { kind: "video", timestamp: 9000 }));
    await waitFrames(rx, 1);
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
    rx.push(clone(oldFrame)); rx.push(clone(tx.out[1])); await waitFrames(rx, 1);
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
    rx.push(clone(a.out[0])); await waitFrames(rx, 1);
    assert.equal(rx.out.length, 1);
    await call("bind", { id: rx.id, pid: "pb|mic" });
    rx.push(clone(a.out[0])); rx.push(clone(b.out[0])); await waitFrames(rx, 2);
    assert.equal(rx.out.length, 2, "only publisher B's frame passes after rebinding");
    assert.deepEqual([...new Uint8Array(rx.out[1].data)], [...new Uint8Array(audio(20, 960).data)]);
});

test("senders never emit plaintext: no key, unsupported codec, or a library that stops encrypting", async () => {
    const tx = endpoint("encode", "audio");
    await call("bind", { id: tx.id, pid: "tx|nokey" });
    tx.push(audio(10, 960)); await settle();
    assert.equal(tx.out.length, 0, "no active key: nothing leaves");
    const video = await sender("tx|h264", 5, 0, key(10), "video");
    video.push(new Frame([0, 0, 0, 1, 0x65, 1, 2, 3, 4, 5, 6, 7], { kind: "video", mime: "video/H265" })); await settle();
    assert.equal(video.out.length, 0, "unsupported codecs never leave the worker");
    const guarded = await sender("tx|guard", 6, 0, key(11));
    // Simulate the library switching this endpoint to passthrough (a state
    // the page cannot request): the wrapper still drops the output.
    library({ data: { kind: "enable", data: { participantIdentity: installed.endpoints.get(guarded.id).lk, enabled: false } } });
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
        rx.push(clone(a.out[0])); await waitFrames(rx, 1);
        assert.equal(rx.out.length, 1);
        await call("bind", { id: rx.id, pid: to });
        rx.push(clone(a.out[0])); rx.push(clone(b.out[0])); await waitFrames(rx, 2);
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
    const deadline = Date.now() + 2000;
    while (!(await call("stats", { id })).endpoint?.ready && Date.now() < deadline) await settle();
    assert.equal((await call("stats", { id })).endpoint.pid, "early|mic", "the pending bind is applied without a second bind");
    push.enqueue(clone(tx.out[0]));
    while (!out.length && Date.now() < deadline) await settle();
    assert.equal(out.length, 1);
});

test("a receive epoch older than the maximum age stops decrypting", async () => {
    const tx = await sender("tx|aged", 11, 0, key(0x61));
    tx.push(audio(30, 960)); tx.push(audio(31, 1920)); await settle();
    const rx = endpoint("decode", "audio");
    await call("key", { pid: "aged|mic", epoch: 11, kid: 0, key: key(0x61) });
    await call("bind", { id: rx.id, pid: "aged|mic" });
    rx.push(clone(tx.out[0])); await waitFrames(rx, 1);
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
    rx.push(clone(tx.out[0])); await waitFrames(rx, 1);
    assert.equal(rx.out.length, 1, "epoch 30 still decrypts");
    await assert.rejects(call("key", { pid: "one|mic", epoch: 31, kid: 1, key: key(0x72) }), /retired/);
});


test("H264 encrypts and authenticates normalized headers with both transform APIs", async () => {
    for (const via of ["post", "script"]) {
        const pid = `tx|h264-${via}`;
        const tx = await sender(pid, 81, 4, key(24), "video", via);
        const rx = endpoint("decode", "video", via);
        await call("bind", { id: rx.id, pid: `rx|h264-${via}` });
        await call("key", { pid: `rx|h264-${via}`, epoch: 81, kid: 4, key: key(24) });
        const head = [0,0,1,0x67,0x42,0xe0,0x1f,0xab,0,0,1,0x68,0xce,0x3c,0x80,0,0,1,0x65,0x88];
        const normalized = [0,0,0,1,0x67,0x42,0xe0,0x1f,0xab,0,0,0,1,0x68,0xce,0x3c,0x80,0,0,0,1,0x65,0x88];
        const payload = Array.from({length:400},(_,i)=>i%256);
        tx.push(new Frame([...head,...payload],{kind:"video",mime:"video/H264",timestamp:18000}));
        await waitFrames(tx, 1);
        assert.equal(tx.out.length,1,via);
        assert.notDeepEqual([...new Uint8Array(tx.out[0].data)].slice(-400),payload);
        rx.push(clone(tx.out[0]));
        await waitFrames(rx, 1);
        assert.equal(rx.out.length,1,via);
        assert.deepEqual([...new Uint8Array(rx.out[0].data)],[...normalized,...payload]);
        const tampered=clone(tx.out[0]);new Uint8Array(tampered.data)[5]^=1;rx.push(tampered);
        rx.push(clone(tx.out[0]));
        rx.push(new Frame([...normalized,...payload],{kind:"video",mime:"video/H264",timestamp:21000}));
        await waitFrames(rx, 1);
        assert.equal(rx.out.length,1,"tampering, replay and plaintext cannot be presented");
    }
});


test("screen content stays closed without fresh, source-bound hardware evidence", async () => {
    for (const [index,via] of ["post","script"].entries()) {
        const tx=endpoint("encode","video",via,true);
        const pid="self|screen",epoch=120+index;
        await call("bind",{id:tx.id,pid,sourceTag:"capture-a"});
        await call("key",{pid,epoch,kid:8+index,key:key(31+index)});
        await call("activate",{pid,epoch});
        const push=ts=>tx.push(new Frame(Array.from({length:300},(_,i)=>i%256),{kind:"video",timestamp:ts}));
        push(3000);await waitFrames(tx, 1);assert.equal(tx.out.length,1);
        assert.equal(tx.out[0].data.byteLength,61,"only an encrypted neutral picture is sent");
        await call("hardwareProof",{id:tx.id,pid,sourceTag:"old-capture",enabled:true});
        push(6000);await waitFrames(tx, 1);assert.equal(tx.out.length,1,"old-source evidence cannot open upload");
        await call("hardwareProof",{id:tx.id,pid,sourceTag:"capture-a",enabled:true});
        push(9000);await waitFrames(tx, 2);assert.equal(tx.out.length,2);
        assert.equal(tx.out[1].data.byteLength,330,"real source frames require hardware proof");
        await new Promise(r=>setTimeout(r,1050));
        push(12000);await waitFrames(tx, 2);assert.equal(tx.out.length,2,"expired evidence closes upload");
        await call("hardwareProof",{id:tx.id,pid,sourceTag:"capture-a",enabled:true});
        await call("bind",{id:tx.id,pid,sourceTag:"capture-b"});
        push(15000);await waitFrames(tx, 3);assert.equal(tx.out.length,3,"new capture starts with its own neutral probe");
        assert.equal(tx.out.at(-1).data.byteLength,61);
    }
});

test("Intel H264 temporal prefixes survive real AEAD encryption and decryption", async () => {
 for (const via of ["post", "script"]) {
  const pid=`intel-${via}|screen`, epoch=900;
  const tx=await sender(`tx-${pid}`,epoch,4,key(42),"video",via);
  const rx=endpoint("decode","video",via);
  await call("bind",{id:rx.id,pid});await call("key",{pid,epoch,kid:4,key:key(42)});
  const plain=new Frame([0,0,0,1,0x67,0x42,0x40,0x2a,0,0,0,1,0x68,0xc8,0,0,1,0x6e,0xc0,0x80,0x07,0xc0,0,0,1,0x65,0x88,...new Array(100).fill(0x55)],{kind:"video",mime:"video/H264",timestamp:123456});
  tx.push(clone(plain));await waitFrames(tx, 1);assert.equal(tx.out.length,1);
  rx.push(clone(tx.out[0]));await waitFrames(rx, 1);assert.equal(rx.out.length,1);
  // The maintained cipher normalizes Annex-B start codes to four bytes.
  assert.ok(new Uint8Array(rx.out[0].data).includes(0x6e));
  assert.equal(new Uint8Array(rx.out[0].data).at(-1),0x55);
  const tampered=clone(tx.out[0]);new Uint8Array(tampered.data)[21]^=1;
  rx.push(tampered);await waitFrames(rx, 1);assert.equal(rx.out.length,1,"clear prefix is authenticated");
 }
});


test("transport replacement retains installed keys and replay protection without old endpoint authority", async () => {
    const tx = await sender("resume|mic", 90001, 0, key(61));
    const oldRX = endpoint("decode");
    await call("bind", {id:oldRX.id,pid:"resumed-peer|mic"});
    await call("key", {pid:"resumed-peer|mic",epoch:90001,kid:0,key:key(61)});
    tx.push(audio(40, 960));tx.push(audio(40,1920));
    await waitFrames(tx,2);assert.equal(tx.out.length,2);
    oldRX.push(clone(tx.out[0]));await waitFrames(oldRX,1);assert.equal(oldRX.out.length,1);
    const before=await call("stats");
    await call("detach",{id:oldRX.id});
    assert.equal((await call("stats")).endpoints,before.endpoints-1);
    const resumedRX=endpoint("decode");
    await call("bind",{id:resumedRX.id,pid:"resumed-peer|mic"}); // no new key control
    const replay=(await call("stats")).dropped.replay;
    resumedRX.push(clone(tx.out[0]));resumedRX.push(clone(tx.out[1]));
    assert.equal(oldRX.cancelled(),true);await waitFrames(resumedRX,1);
    assert.equal(resumedRX.out.length,1);
    assert.deepEqual([...new Uint8Array(resumedRX.out[0].data)],[...new Uint8Array(audio(40,1920).data)]);
    assert.equal(oldRX.out.length,1,"a closed peer has no presentation authority");
    assert.equal((await call("stats")).dropped.replay,replay+1,"replay window survives the transport replacement");
    await call("detach",{id:tx.id});
    const resumedTX=endpoint("encode");await call("bind",{id:resumedTX.id,pid:"resume|mic"});
    resumedTX.push(audio(40,2880));await waitFrames(resumedTX,1);
    assert.equal(resumedTX.out.length,1,"retained active sender key needs no remote handshake");
});


test("repeated replacements reuse library cryptors and cancel obsolete streams", async () => {
    let tx=endpoint("encode");await call("bind",{id:tx.id,pid:"bounded|mic"});
    const allocated=(await call("stats")).librarySlots, libraryIDs=libraryTrackIDs.size;
    for(let i=0;i<12;i++) {
        await call("detach",{id:tx.id});assert.equal(tx.cancelled(),true);
        tx=endpoint("encode","audio",i%2?"script":"post");await call("bind",{id:tx.id,pid:"bounded|mic"});
        assert.equal((await call("stats")).librarySlots,allocated);
        assert.equal(libraryTrackIDs.size,libraryIDs,"the actual pinned library must see recycled identities");
    }
    await call("detach",{id:tx.id});
});
