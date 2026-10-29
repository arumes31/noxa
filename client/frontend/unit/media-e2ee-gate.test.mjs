// Review 2a-fix2 P3-1 (repro adopted): no presentation-gate deadlock after a timed-out rebind.
// Real worker core + real pinned LiveKit worker in-process, real page MediaE2EE.
import assert from "node:assert/strict";
import { test } from "node:test";

globalThis.self = globalThis;
globalThis.onmessage = null;
globalThis.postMessage = () => {};
await import("livekit-client/e2ee-worker");
const { installMediaE2EEWorker } = await import("../src/media-e2ee-core.js");
const { MediaE2EE } = await import("../src/media-e2ee.js");

let page = null; const direct = [];
let holdPageReplies = false; const held = [];
installMediaE2EEWorker(globalThis, { reply: m => {
    if (m.seq !== undefined && m.seq >= 1e6) { direct.push(m); return; }
    if (holdPageReplies && m.seq !== undefined) { held.push(m); return; }
    queueMicrotask(() => page?.onmessage?.({ data: m }));
} });
const worker = globalThis.onmessage;
class BridgeWorker { constructor() { page = this; } postMessage(data) { const d = structuredClone({ ...data, readable: undefined, writable: undefined }); if (data.readable) { d.readable = data.readable; d.writable = data.writable; } setTimeout(() => worker({ data: d }), 0); } terminate() {} }
let dseq = 1e6;
function call(op, data = {}) { const id = ++dseq; worker({ data: { op, seq: id, ...data } }); return (async () => { for (;;) { const f = direct.find(r => r.seq === id); if (f) { direct.splice(direct.indexOf(f), 1); if (!f.ok) throw new Error(f.value); return f.value; } await new Promise(r => setTimeout(r, 5)); } })(); }
class Frame { constructor(bytes, ts) { this.data = new Uint8Array(bytes).buffer; this.timestamp = ts; this.meta = { synchronizationSource: 1, mimeType: "audio/opus", payloadType: 111 }; } getMetadata() { return this.meta; } }
const clone = f => Object.assign(Object.create(Frame.prototype), f, { data: f.data.slice(0), meta: { ...f.meta } });
function txEndpoint(id) { let push; const readable = new ReadableStream({ start(c) { push = c; } }); const out = []; const writable = new WritableStream({ write(f) { out.push(f); } }); worker({ data: { op: "attach", id, operation: "encode", kind: "audio", readable, writable } }); return { out, push: f => push.enqueue(f) }; }
const settle = (ms = 80) => new Promise(r => setTimeout(r, ms));
let ts = 0; const frame = () => new Frame([0xf8, ...Array.from({ length: 40 }, (_, i) => (i + ts) & 255)], ts += 960);

test("a rebind whose reply timed out still opens the gate on the retried bind", { timeout: 60000 }, async () => {
    await call("init");
    const scope = { Worker: BridgeWorker, crypto: globalThis.crypto, RTCRtpSender: { prototype: { createEncodedStreams() {} } }, RTCRtpReceiver: { prototype: { createEncodedStreams() {} } } };
    const media = new MediaE2EE({ api: "streams", scope, workerURL: "bridge" });
    await media.ready;
    // receiver endpoint (streams API)
    let push; const readable = new ReadableStream({ start(c) { push = c; } }); const delivered = [];
    const writable = new WritableStream({ write(f) { delivered.push(f); } });
    const receiver = { track: { enabled: true, kind: "audio" }, createEncodedStreams: () => ({ readable, writable }) };
    media.protectReceiver(receiver, "audio");
    assert.equal(receiver.track.enabled, false, "gated at attach");
    // two publishers A and B
    const ta = txEndpoint("tx:A"), tb = txEndpoint("tx:B");
    for (const [pid, k] of [["pa|mic", 1], ["pb|mic", 2]]) { await call("bind", { id: pid === "pa|mic" ? "tx:A" : "tx:B", pid }); await call("key", { pid, epoch: 5, kid: 0, key: new Uint8Array(32).fill(k) }); await call("activate", { pid, epoch: 5 }); }
    const sendVia = async (tx, n = 3) => { for (let i = 0; i < n; i++) tx.push(frame()); await settle(); const out = tx.out.splice(0); for (const f of out) push.enqueue(clone(f)); await settle(); };
    // receiver bound to A with A's key: opens
    await media.bind(receiver, "c1|mic");
    await media.setKey("c1|mic", 5, 0, new Uint8Array(32).fill(1));
    await sendVia(ta);
    assert.equal(media.authenticated(receiver), true, "authenticated after A's first authenticated frame");
    // MID reused for publisher B: the page's bind reply is lost/late (> 10 s request timeout)
    await media.setKey("c2|mic", 5, 0, new Uint8Array(32).fill(2));
    holdPageReplies = true;
    const first = media.bind(receiver, "c2|mic").catch(e => e);
    await settle(200); holdPageReplies = false;
    await sendVia(tb);                       // worker emits first-frame(gen 2); page still has gen 1 -> ignored
    const err = await first;                 // page request times out after 10 s
    console.log("first rebind:", String(err?.message || err));
    await media.bind(receiver, "c2|mic");    // next syncReceivers retries: worker early-returns {gen}
    await sendVia(tb, 10);
    const st = await call("stats", {});
    console.log("receiver track enabled:", receiver.track.enabled, "authenticated:", media.authenticated(receiver), "frames decrypted (total):", st.decrypted, "delivered to sink:", delivered.length);
    // Review 2a-fix2 P3-1 (repro adopted): the retried bind adopts the
    // worker's authenticated state; the gate never stays closed for good.
    assert.equal(media.authenticated(receiver), true, "authenticated B frames open the gate after a lost bind reply");
    media.close?.();
});
