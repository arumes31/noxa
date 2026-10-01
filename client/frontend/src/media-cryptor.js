// Codec-aware frame encryption is supplied by the pinned LiveKit worker.
// This adapter never enables plaintext passthrough. Membership/key distribution
// belongs to the channel session, not to the SFU or this transform boundary.

export class MediaCryptor {
    static supported() {
        return typeof Worker === "function" && typeof globalThis.crypto?.subtle === "object"
            && typeof globalThis.RTCRtpSender?.prototype.createEncodedStreams === "function"
            && typeof globalThis.RTCRtpReceiver?.prototype.createEncodedStreams === "function";
    }

    constructor(onError = () => {}) {
        if (!MediaCryptor.supported()) throw new Error("Encoded media encryption is unavailable");
        this.worker = new Worker(new URL("./media-encryption.worker.js", import.meta.url), { type: "module" });
        this.pending = new Map();
        this.transforms = new WeakMap();
        this.keyIdentities = new Map();
        this.identities = new Set();
        this.transformCount = 0;
        this.closed = false;
        this.onError = onError;
        // A session cannot span timestamp half-wrap or retain unbounded old
        // key handlers. The channel owner must replace it with fresh keys.
        this.expiry = setTimeout(() => { this.close(); this.onError(new Error("Media encryption session expired")); }, 10 * 60 * 1000);
        this.ready = this.request("init", {
            keyProviderOptions: { sharedKey: false, ratchetSalt: "LKFrameEncryptionKey", ratchetWindowSize: 0, failureTolerance: -1, keyringSize: 16, keySize: 128 },
            loglevel: "error",
        }, "initAck");
        this.worker.onmessage = ({ data: message }) => {
            if (message.kind === "log") return;
            const key = message.kind === "initAck" ? "initAck" : message.data?.uuid;
            const pending = this.pending.get(key);
            if (pending) {
                clearTimeout(pending.timer); this.pending.delete(key);
                if (message.kind === "error") pending.reject(message.data.error || new Error("Media encryption failed"));
                else pending.resolve(message.data);
            } else if (message.kind === "error") this.onError(message.data.error || new Error("Media encryption failed"));
        };
        this.worker.onerror = () => { const error = new Error("Media encryption worker stopped"); this.close(); this.onError(error); };
    }

    request(kind, data, key = crypto.randomUUID(), precedingMessage) {
        if (this.closed) return Promise.reject(new Error("Media encryption session ended"));
        if (this.pending.size >= 64) return Promise.reject(new Error("Media encryption request capacity reached"));
        return new Promise((resolve, reject) => {
            const timer = setTimeout(() => { this.pending.delete(key); reject(new Error("Media encryption worker timed out")); }, 5000);
            this.pending.set(key, { resolve, reject, timer });
            try {
                if (precedingMessage) this.worker.postMessage(precedingMessage);
                this.worker.postMessage({ kind, data: { ...data, uuid: key } });
            } catch (error) { clearTimeout(timer); this.pending.delete(key); reject(error); }
        });
    }

    registerIdentity(identity) {
        if (typeof identity !== "string" || !/^[\x21-\x7e]{1,256}$/.test(identity)) throw new Error("Invalid media identity");
        if (this.closed) throw new Error("Media encryption session ended");
        if (!this.identities.has(identity) && this.identities.size >= 512) throw new Error("Media encryption identity capacity reached");
        this.identities.add(identity);
    }

    async setKey(identity, material, index = 0) {
        if (!(material instanceof Uint8Array) || material.length !== 32 || !identity || !Number.isInteger(index) || index < 0 || index > 15) throw new Error("Invalid media key");
        const bytes = new Uint8Array(material);
        this.registerIdentity(identity);
        await this.ready;
        const keyID = `${identity}:${index}`;
        const fingerprint = [...new Uint8Array(await crypto.subtle.digest("SHA-256", bytes))].join(",");
        const previous = this.keyIdentities.get(keyID);
        if (previous && previous !== fingerprint) throw new Error("A new media key requires a new epoch identity");
        if (!previous && this.keyIdentities.size >= 1024) throw new Error("Media encryption session capacity reached");
        this.keyIdentities.set(keyID, fingerprint);
        const key = await crypto.subtle.importKey("raw", bytes, "HKDF", false, ["deriveBits", "deriveKey"]);
        if (this.closed) throw new Error("Media encryption session ended");
        // The worker serializes messages. This acknowledgement occurs after
        // key derivation, so callers can wait before publishing or rotating.
        await this.request("enable", { participantIdentity: identity, enabled: true }, undefined,
            { kind: "setKey", data: { participantIdentity: identity, key, keyIndex: index, updateCurrentKeyIndex: true } });
    }

    attachSender(sender, identity, trackID, codec) { this.attach(sender, "encode", identity, trackID, codec); }
    attachReceiver(receiver, identity, trackID, codec) { this.attach(receiver, "decode", identity, trackID, codec); }
    attach(endpoint, operation, identity, trackID, codec) {
        if (this.closed || !identity || !trackID || (codec && codec !== "vp8" && codec !== "opus")) throw new Error("Invalid encrypted media transform");
        const previous = this.transforms.get(endpoint);
        if (previous && (previous.identity !== identity || previous.operation !== operation)) throw new Error("A new media identity requires a new transform endpoint");
        if (!previous && this.transformCount >= 512) throw new Error("Media encryption transform capacity reached");
        this.registerIdentity(identity);
        // Always enable before installing a transform, even without a key.
        // Missing-key frames must be dropped by the worker, never forwarded.
        this.worker.postMessage({ kind: "enable", data: { participantIdentity: identity, enabled: true } });
        const wireID = previous?.wireID || `${operation}:${crypto.randomUUID()}`;
        if (previous) {
            this.worker.postMessage({ kind: "updateCodec", data: { participantIdentity: identity, trackId: wireID, previousTrackId: wireID, codec, hasPacketTrailer: false } });
        } else {
            const streams = endpoint.createEncodedStreams();
            this.worker.postMessage({ kind: operation, data: { participantIdentity: identity, trackId: wireID, codec, hasPacketTrailer: false, readableStream: streams.readable, writableStream: streams.writable } }, [streams.readable, streams.writable]);
            this.transformCount++;
        }
        this.transforms.set(endpoint, { wireID, identity, operation });
    }

    async encryptData(identity, bytes) {
        if (!(bytes instanceof Uint8Array) || bytes.length > 64 * 1024) throw new Error("Invalid encrypted preview");
        this.registerIdentity(identity);
        await this.ready;
        const result = await this.request("encryptDataRequest", { participantIdentity: identity, payload: bytes });
        return { payload: result.payload, iv: result.iv, keyIndex: result.keyIndex };
    }
    async decryptData(identity, sealed) {
        if (!(sealed.payload instanceof Uint8Array) || sealed.payload.length > 64 * 1024 + 16 || !(sealed.iv instanceof Uint8Array) || sealed.iv.length !== 12 || !Number.isInteger(sealed.keyIndex) || sealed.keyIndex < 0 || sealed.keyIndex > 15) throw new Error("Invalid encrypted preview");
        this.registerIdentity(identity);
        await this.ready;
        const result = await this.request("decryptDataRequest", { participantIdentity: identity, payload: sealed.payload, iv: sealed.iv, keyIndex: sealed.keyIndex });
        return result.payload;
    }
    close() {
        if (this.closed) return;
        this.closed = true; clearTimeout(this.expiry); this.worker.terminate();
        for (const pending of this.pending.values()) { clearTimeout(pending.timer); pending.reject(new Error("Media encryption session ended")); }
        this.pending.clear();
        this.keyIdentities.clear();
        this.identities.clear();
    }
}
