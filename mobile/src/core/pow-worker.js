// pow-worker.js — runs the anti-raid proof-of-work search (core/pow.js) off
// the UI thread.
import { searchNonces } from "./pow.js";

const CHUNK = 1 << 20;

self.onmessage = ({ data }) => {
    const prefix = new Uint8Array(data.prefix);
    for (let start = 0; start < Number.MAX_SAFE_INTEGER; start += CHUNK) {
        const nonce = searchNonces(prefix, data.difficulty, start, CHUNK);
        if (nonce >= 0) {
            self.postMessage({ nonce });
            return;
        }
    }
};
