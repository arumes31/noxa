// LiveKit's IV carries the original SSRC and RTP timestamp. Retain a bounded
// timestamp window, not an IV LRU: eviction must never admit old ciphertext.
// Call accept only AFTER authenticated decryption, with a separate instance
// per actual key generation. Sender restarts require a fresh random key.
export class MediaReplayWindow {
    constructor({ maxSources = 8, maxFrames = 8192, timestampWindow = 1800000 } = {}) {
        this.sources = new Map();
        this.maxSources = maxSources; this.maxFrames = maxFrames; this.timestampWindow = timestampWindow;
    }
    accept(iv) {
        if (!(iv instanceof Uint8Array) || iv.length !== 12) return false;
        const view = new DataView(iv.buffer, iv.byteOffset, iv.byteLength);
        const ssrc = view.getUint32(0), timestamp = view.getUint32(4);
        const token = `${timestamp}:${view.getUint32(8)}`;
        let source = this.sources.get(ssrc);
        if (!source) {
            if (this.sources.size >= this.maxSources) return false;
            source = { newest: timestamp, frames: new Map() }; this.sources.set(ssrc, source);
        }
        // Signed modular subtraction handles one uint32 rollover. Keys must
        // expire well before half the timestamp range (10-minute session TTL).
        const distance = (timestamp - source.newest) | 0;
        if (distance < -this.timestampWindow || source.frames.has(token)) return false;
        if (distance > 0) {
            source.newest = timestamp;
            for (const [key, seenAt] of source.frames) {
                if (((timestamp - seenAt) | 0) > this.timestampWindow) source.frames.delete(key);
            }
        }
        if (source.frames.size >= this.maxFrames) return false;
        source.frames.set(token, timestamp);
        return true;
    }
}
