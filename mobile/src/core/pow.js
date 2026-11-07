// pow.js — anti-raid proof of work (#7, internal/netproto/pow.go). While a
// server's ingress shield is raised it answers a login with a challenge:
// find a nonce so SHA-256("noxa-pow-v1" || prefix || nonce_be64) starts with
// `difficulty` zero bits. The message is 35 bytes, a single SHA-256 block,
// and difficulty is at most 24 bits, so only the first output word matters.

export const POW_ALGORITHM = "sha256-noxa-pow-v1";
export const MAX_POW_DIFFICULTY = 24;
const LABEL = "noxa-pow-v1";
const PREFIX_BYTES = 16;

const K = new Uint32Array([
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);
const H = [0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19];
const rotr = (x, n) => (x >>> n) | (x << (32 - n));

/** The 64-byte padded block for prefix with an all-zero nonce. */
function baseBlock(prefix) {
    const block = new Uint8Array(64);
    for (let i = 0; i < LABEL.length; i++) block[i] = LABEL.charCodeAt(i);
    block.set(prefix, LABEL.length);
    const length = LABEL.length + PREFIX_BYTES + 8; // 35 bytes
    block[length] = 0x80;
    new DataView(block.buffer).setUint32(60, length * 8);
    return block;
}

/** First 32 bits of the digest for nonce (high and low 32-bit halves). */
function firstWord(words, w, high, low) {
    // The nonce occupies bytes 27..34 of the block: words 6, 7 and 8.
    w.set(words);
    w[6] = (words[6] & 0xffffff00) | (high >>> 24);
    w[7] = ((high << 8) | (low >>> 24)) >>> 0;
    w[8] = (((low << 8) >>> 0) | (words[8] & 0xff)) >>> 0;
    for (let i = 16; i < 64; i++) {
        const s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >>> 3);
        const s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >>> 10);
        w[i] = (w[i - 16] + s0 + w[i - 7] + s1) | 0;
    }
    let [a, b, c, d, e, f, g, h] = H;
    for (let i = 0; i < 64; i++) {
        const t1 = (h + (rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)) + ((e & f) ^ (~e & g)) + K[i] + w[i]) | 0;
        const t2 = ((rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)) + ((a & b) ^ (a & c) ^ (b & c))) | 0;
        h = g; g = f; f = e; e = (d + t1) | 0; d = c; c = b; b = a; a = (t1 + t2) | 0;
    }
    return (H[0] + a) >>> 0;
}

/** First 32 bits of the digest for one nonce (tests and diagnostics). */
export function powFirstWord(prefix, nonce) {
    const view = new DataView(baseBlock(prefix).buffer);
    const words = new Uint32Array(16);
    for (let i = 0; i < 16; i++) words[i] = view.getUint32(i * 4);
    return firstWord(words, new Uint32Array(64), Math.floor(nonce / 0x100000000), nonce >>> 0);
}

/** Decodes a challenge prefix (base64 from JSON). Returns null if invalid. */
export function challengePrefix(challenge) {
    if (challenge?.algorithm !== POW_ALGORITHM) return null;
    const difficulty = challenge.difficulty;
    if (!Number.isInteger(difficulty) || difficulty < 1 || difficulty > MAX_POW_DIFFICULTY) return null;
    let bytes;
    try {
        bytes = Uint8Array.from(atob(String(challenge.prefix || "")), c => c.charCodeAt(0));
    } catch {
        return null;
    }
    return bytes.length === PREFIX_BYTES ? bytes : null;
}

/**
 * Searches nonces [start, start + count) and returns the first solution or
 * -1. Nonces stay below 2^53 so they remain exact JavaScript numbers.
 */
export function searchNonces(prefix, difficulty, start, count) {
    const block = baseBlock(prefix);
    const view = new DataView(block.buffer);
    const words = new Uint32Array(16);
    for (let i = 0; i < 16; i++) words[i] = view.getUint32(i * 4);
    const w = new Uint32Array(64);
    for (let nonce = start; nonce < start + count; nonce++) {
        const high = Math.floor(nonce / 0x100000000), low = nonce >>> 0;
        if (Math.clz32(firstWord(words, w, high, low)) >= difficulty) return nonce;
    }
    return -1;
}

/** Leading zero bits of SHA-256 for nonce, computed with WebCrypto (tests). */
export async function digestZeroBits(prefix, nonce) {
    const message = new Uint8Array(LABEL.length + PREFIX_BYTES + 8);
    for (let i = 0; i < LABEL.length; i++) message[i] = LABEL.charCodeAt(i);
    message.set(prefix, LABEL.length);
    new DataView(message.buffer).setBigUint64(LABEL.length + PREFIX_BYTES, BigInt(nonce));
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", message));
    let bits = 0;
    for (const byte of digest) {
        if (byte) return bits + Math.clz32(byte) - 24;
        bits += 8;
    }
    return bits;
}

const CHUNK = 1 << 15;

/** Solves on this thread in chunks, yielding so the UI stays responsive. */
async function solveInline(prefix, difficulty, signal) {
    for (let start = 0; start < Number.MAX_SAFE_INTEGER; start += CHUNK) {
        if (signal?.aborted) throw new Error("proof of work cancelled");
        const nonce = searchNonces(prefix, difficulty, start, CHUNK);
        if (nonce >= 0) return nonce;
        await new Promise(resolve => setTimeout(resolve, 0));
    }
    throw new Error("proof of work not found");
}

/**
 * Solves a PowChallenge and returns the nonce. Uses a module worker when the
 * browser has one, so a phone's UI never freezes; otherwise a chunked loop.
 */
export async function solvePow(challenge, { signal, worker = globalThis.Worker } = {}) {
    const prefix = challengePrefix(challenge);
    if (!prefix) throw new Error("the server asked for an unsupported proof of work");
    if (typeof worker !== "function") return solveInline(prefix, challenge.difficulty, signal);
    let instance;
    try {
        instance = new worker(new URL("./pow-worker.js", import.meta.url), { type: "module" });
    } catch {
        return solveInline(prefix, challenge.difficulty, signal);
    }
    try {
        return await new Promise((resolve, reject) => {
            instance.onmessage = event => resolve(event.data.nonce);
            instance.onerror = () => reject(new Error("proof of work failed"));
            signal?.addEventListener("abort", () => reject(new Error("proof of work cancelled")), { once: true });
            instance.postMessage({ prefix, difficulty: challenge.difficulty });
        });
    } finally {
        instance.terminate();
    }
}
