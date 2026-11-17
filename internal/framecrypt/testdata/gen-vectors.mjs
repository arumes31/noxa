// Regenerates livekit-h264-vectors.json from the REAL pinned LiveKit e2ee
// worker (client/frontend/node_modules/livekit-client, run from that folder):
//   cd client/frontend && node ../../internal/framecrypt/testdata/gen-vectors.mjs
// It writes livekit-h264-vectors.json and livekit-opus-vectors.json.
// Math.random is stubbed per SSRC so the IV send-count seed is recorded.
import { createRequire } from "node:module";
import { writeFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

const require = createRequire(pathToFileURL(process.cwd() + "/package.json"));
globalThis.self = globalThis;
globalThis.onmessage = null;
globalThis.postMessage = () => {};
await import(pathToFileURL(require.resolve("livekit-client/e2ee-worker")).href);
const lk = (kind, data) => globalThis.onmessage({ data: { kind, data } });

let seed = 0;
Math.random = () => (seed + 0.5) / 0xffff;
lk("init", { keyProviderOptions: { sharedKey: false, ratchetSalt: "LKFrameEncryptionKey", ratchetWindowSize: 0, failureTolerance: -1, keyringSize: 16, keySize: 128 }, loglevel: "silent" });

const hex = b => Buffer.from(b).toString("hex");
const rnd = (n, s) => { let x = s >>> 0 || 1; return Array.from({ length: n }, () => { x ^= x << 13; x >>>= 0; x ^= x >>> 17; x ^= x << 5; x >>>= 0; return x & 255; }); };
let streamN = 0;
async function stream(material, kid, ssrc, streamSeed, frames, audio = false) {
    const id = `s${++streamN}`;
    lk("enable", { participantIdentity: id, enabled: true });
    const key = await crypto.subtle.importKey("raw", material, "HKDF", false, ["deriveBits", "deriveKey"]);
    lk("setKey", { participantIdentity: id, key, keyIndex: kid, updateCurrentKeyIndex: true });
    await new Promise(r => setTimeout(r, 50));
    let push; const out = [];
    const readable = new ReadableStream({ start(c) { push = c; } });
    const writable = new WritableStream({ write(f) { out.push(f); } });
    lk("encode", { participantIdentity: id, trackId: id, codec: "vp8", hasPacketTrailer: false, readableStream: readable, writableStream: writable });
    seed = streamSeed;
    const vectors = [];
    for (const [i, { bytes, ts }] of frames.entries()) {
        // Audio frames (RTCEncodedAudioFrame) have no "type": LiveKit keeps 1 clear byte (the Opus TOC).
        push.enqueue(audio
            ? { data: new Uint8Array(bytes).buffer, timestamp: ts, getMetadata: () => ({ synchronizationSource: ssrc, payloadType: 111, mimeType: "audio/opus" }) }
            : { data: new Uint8Array(bytes).buffer, timestamp: ts, type: "key", getMetadata: () => ({ synchronizationSource: ssrc, payloadType: 102, mimeType: "video/h264" }) });
        for (let t = 0; out.length <= i && t < 400; t++) await new Promise(r => setTimeout(r, 5));
        if (out.length <= i) throw new Error("no encrypted output");
        vectors.push({ key: hex(material), kid, ssrc, ts, seed: streamSeed, index: i, plaintext: hex(bytes), ciphertext: hex(new Uint8Array(out[i].data)) });
    }
    return vectors;
}

const SPS = [0x67, 0x42, 0xc0, 0x1f, 0x0f, 0xd9, 0x1f, 0x88, 0x88, 0x84, 0, 0, 3, 0, 4, 0, 0, 3, 0, 0xc8, 0x3c, 0x60, 0xc9, 0x20];
const PPS = [0x68, 0x87, 0xcb, 0x83, 0xcb, 0x20];
const sc4 = [0, 0, 0, 1], sc3 = [0, 0, 1];
const key1 = new Uint8Array(32).map((_, i) => i + 1), key2 = new Uint8Array(rnd(32, 77));
const all = [
    // AUD + SPS + PPS + temporal prefix (type 14) + IDR, 4-byte start codes.
    ...await stream(key1, 0, 0x11223344, 1234, [
        { ts: 90000, bytes: [...sc4, 0x09, 0xf0, ...sc4, ...SPS, ...sc4, ...PPS, ...sc4, 0x6e, 0xc0, 0x80, 0x0f, 0x07, ...sc4, 0x65, 0x88, 0x84, ...rnd(200, 5)] },
        { ts: 93000, bytes: [...sc4, 0x41, 0x9a, ...rnd(120, 6)] },
        // Zero runs in the plaintext tail: ciphertext is random, keep length large for escapes.
        { ts: 96000, bytes: [...sc4, 0x41, 0x9a, ...rnd(4000, 7)] },
        { ts: 99000, bytes: [...sc4, 0x41, 0x9b, ...new Array(64).fill(0)] },
    ]),
    // 3-byte start codes (header normalized to 4), kid 7.
    ...await stream(key2, 7, 0xdeadbeef, 0, [
        { ts: 3000, bytes: [...sc3, ...SPS, ...sc3, ...PPS, ...sc3, 0x65, 0xb8, ...rnd(300, 8)] },
        { ts: 6000, bytes: [...sc3, 0x01, 0x9a, ...rnd(50, 9)] },
    ]),
    // RTP timestamp near 2^32 with a large seed: ts - sendCount wraps.
    ...await stream(key2, 15, 1, 65534, [
        { ts: 100, bytes: [...sc4, ...SPS, ...sc4, ...PPS, ...sc4, 0x65, 0x88, ...rnd(80, 10)] },
        { ts: 4294967200, bytes: [...sc4, 0x41, 0x9a, ...rnd(80, 11)] },
        { ts: 2900, bytes: [...sc4, 0x41, 0x9a, ...rnd(80, 12)] },
    ]),
];
// Encrypted output must include at least one inserted emulation-prevention byte.
// (4-byte start codes only: growth beyond the 30-byte trailer is escaping.)
if (!all.some(v => !v.plaintext.includes("000001") === false && v.ciphertext.length / 2 - v.plaintext.length / 2 > 30 && !/(^|[^0])000001/.test(v.plaintext.replace(/00000001/g, "")))) throw new Error("no escaped vector");
writeFileSync(new URL("./livekit-h264-vectors.json", import.meta.url), JSON.stringify({ source: "livekit-client 2.23.0 e2ee worker", vectors: all }, null, 1) + "\n");
console.log(`wrote ${all.length} vectors`);

// Opus packets (CELT fullband stereo 20 ms TOC 0xfc, a 1-byte TOC-only
// packet, zero runs: no emulation prevention on audio) at 48 kHz timestamps.
const opus = [
    ...await stream(key1, 3, 0x0a0b0c0d, 4321, [
        { ts: 960, bytes: [0xfc, ...rnd(320, 21)] },
        { ts: 1920, bytes: [0xfc, ...new Array(40).fill(0), 0, 0, 1, 0, 0, 3] },
        { ts: 2880, bytes: [0xfc] },
    ], true),
    ...await stream(key2, 9, 0xfffffffe, 65534, [
        { ts: 4294966336, bytes: [0x78, ...rnd(90, 22)] },
        { ts: 0, bytes: [0x78, ...rnd(90, 23)] },
    ], true),
];
writeFileSync(new URL("./livekit-opus-vectors.json", import.meta.url), JSON.stringify({ source: "livekit-client 2.23.0 e2ee worker (audio)", vectors: opus }, null, 1) + "\n");
console.log(`wrote ${opus.length} opus vectors`);
process.exit(0);
