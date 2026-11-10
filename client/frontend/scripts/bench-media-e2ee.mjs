// bench-media-e2ee.mjs — per-frame cost of the media E2EE path (plan item 5).
//
// Runs the ACTUAL pinned LiveKit e2ee worker with the noXa wrapper (the same
// code the app loads), encrypting and then decrypting realistic VP8 and Opus
// frames one at a time. Each frame is timed from enqueue to the transformed
// frame reaching the output stream, so stream and wrapper overhead count.
//
//   node scripts/bench-media-e2ee.mjs [frames-per-case]
//
// Node's WebCrypto uses OpenSSL with AES-NI, like Chromium's BoringSSL; the
// browser numbers in tests/media-e2ee-bench.spec.js cover the raw cipher there.
globalThis.self = globalThis;
globalThis.onmessage = null;
const replies = [];
globalThis.postMessage = () => {};
await import("livekit-client/e2ee-worker");
const { installMediaE2EEWorker } = await import("../src/media-e2ee-core.js");
installMediaE2EEWorker(globalThis, { reply: message => replies.push(message) });

const FRAMES = Number(process.argv[2]) || 300;
let seq = 0;
/** Sends one control op to the worker wrapper and resolves with its reply value. */
async function call(op, data = {}) {
    const id = ++seq;
    globalThis.onmessage({ data: { op, seq: id, ...data } });
    for (;;) {
        const found = replies.find(r => r.seq === id);
        if (found) {
            replies.splice(replies.indexOf(found), 1);
            if (!found.ok) throw new Error(found.value);
            return found.value;
        }
        await new Promise(r => setTimeout(r, 1));
    }
}

/** Minimal RTCEncodedVideoFrame / RTCEncodedAudioFrame stand-in. */
class Frame {
    constructor(bytes, { kind, type = "key", timestamp = 0 }) {
        this.data = bytes.buffer;
        this.timestamp = timestamp;
        if (kind === "video") this.type = type;
        this.meta = { synchronizationSource: 4242, mimeType: kind === "video" ? "video/VP8" : "audio/opus", payloadType: kind === "video" ? 96 : 111 };
    }
    getMetadata() { return this.meta; }
}

let n = 0;
/**
 * Attaches an encode or decode endpoint. run(frame) enqueues one frame and
 * resolves with the transformed frame and the elapsed milliseconds.
 */
function endpoint(operation, kind) {
    const id = `${operation === "encode" ? "tx" : "rx"}:bench${++n}`;
    let push;
    let waiter = null;
    const readable = new ReadableStream({ start(c) { push = c; } });
    const writable = new WritableStream({ write(frame) { const w = waiter; waiter = null; w?.(frame); } });
    globalThis.onmessage({ data: { op: "attach", id, operation, kind, readable, writable, hardwareRequired: false } });
    return {
        id,
        // Resolves with the transformed frame and the elapsed milliseconds.
        run(frame) {
            return new Promise(resolve => {
                const start = performance.now();
                waiter = out => resolve({ out, ms: performance.now() - start });
                push.enqueue(frame);
            });
        },
    };
}

/** A VP8 frame of the given size: a keyframe header, or an inter-frame tag. */
function vp8(bytes, key, fill) {
    const data = new Uint8Array(bytes);
    for (let i = 0; i < data.length; i++) data[i] = (i * 131 + fill) & 255;
    if (key) data.set([0x10, 0x02, 0x00, 0x9d, 0x01, 0x2a, 0x80, 0x07, 0x38, 0x04]);
    else data[0] = 0x31; // inter frame: show_frame set, key bit set to 1 (= not key)
    return data;
}
/** An Opus packet of the given size with a TOC byte. */
function opus(bytes, fill) {
    const data = new Uint8Array(bytes);
    for (let i = 0; i < data.length; i++) data[i] = (i * 7 + fill) & 255;
    data[0] = 0xf8;
    return data;
}

/** The p-quantile of an ascending array. */
const pct = (sorted, p) => sorted[Math.min(sorted.length - 1, Math.floor(sorted.length * p))];
/** Milliseconds per frame at fps. */
const budget = fps => 1000 / fps;

// Frame sizes from typical bitrates: delta = bitrate / 8 / fps; keyframes
// are taken as ~8x a delta frame (VP8 screen and camera content).
const cases = [
    { name: "Opus 20 ms @ 64 kbps", kind: "audio", bytes: 160, fps: 50 },
    { name: "VP8 720p30 delta (2.5 Mbps)", kind: "video", bytes: 10_400, fps: 30 },
    { name: "VP8 720p30 key", kind: "video", bytes: 83_000, key: true, fps: 30 },
    { name: "VP8 1080p60 delta (8 Mbps)", kind: "video", bytes: 16_700, fps: 60 },
    { name: "VP8 1080p60 key", kind: "video", bytes: 133_000, key: true, fps: 60 },
    { name: "VP8 1440p60 delta (16 Mbps)", kind: "video", bytes: 33_300, fps: 60 },
    { name: "VP8 1440p60 key", kind: "video", bytes: 266_000, key: true, fps: 60 },
];

await call("init");
const key = new Uint8Array(32).fill(7);
let epoch = 1000;
console.log(`node ${process.version}, ${FRAMES} frames per case (after 30 warm-up frames)`);
console.log("case | encrypt p50 / p95 / max ms | decrypt p50 / p95 / max ms | p95 enc+dec as % of frame budget");
for (const c of cases) {
    epoch++;
    const pid = `bench${epoch}|${c.kind === "audio" ? "mic" : "cam"}`;
    const tx = endpoint("encode", c.kind);
    await call("bind", { id: tx.id, pid });
    await call("key", { pid, epoch, kid: 0, key });
    await call("activate", { pid, epoch });
    const rx = endpoint("decode", c.kind);
    await call("bind", { id: rx.id, pid: `peer-${pid}` });
    await call("key", { pid: `peer-${pid}`, epoch, kid: 0, key });
    const enc = [];
    const dec = [];
    for (let i = 0; i < FRAMES + 30; i++) {
        // RTP clock: Opus advances 960 ticks per 20 ms; video runs at 90 kHz.
        const ts = (i + 1) * (c.kind === "audio" ? 960 : Math.round(90_000 / c.fps));
        const bytes = c.kind === "audio" ? opus(c.bytes, i) : vp8(c.bytes, c.key || i === 0, i);
        const type = c.kind === "video" && !(c.key || i === 0) ? "delta" : "key";
        const e = await tx.run(new Frame(bytes, { kind: c.kind, type, timestamp: ts }));
        const d = await rx.run(e.out);
        if (i === 0 && d.out.data.byteLength !== c.bytes) throw new Error(`${c.name}: round trip failed`);
        if (i >= 30) { enc.push(e.ms); dec.push(d.ms); }
    }
    enc.sort((a, b) => a - b);
    dec.sort((a, b) => a - b);
    const both = pct(enc, 0.95) + pct(dec, 0.95);
    console.log(`${c.name} | ${pct(enc, 0.5).toFixed(3)} / ${pct(enc, 0.95).toFixed(3)} / ${enc.at(-1).toFixed(3)} | ` +
        `${pct(dec, 0.5).toFixed(3)} / ${pct(dec, 0.95).toFixed(3)} / ${dec.at(-1).toFixed(3)} | ${(100 * both / budget(c.fps)).toFixed(1)}%`);
    await call("detach", { id: tx.id });
    await call("detach", { id: rx.id });
}
process.exit(0);
