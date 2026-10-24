// frame.js — the noXa control framing: 4-byte big-endian length (type +
// payload), 2-byte big-endian type, JSON payload. WebSocket messages carry a
// byte stream, so frames may be split or merged across messages.

export const MAX_FRAME_SIZE = 1 << 20;

// Message types used by the mobile app (internal/netproto/codec.go).
export const Msg = Object.freeze({
    Authenticate: 1,
    AuthResponse: 2,
    ChatSend: 5,
    ChatBroadcast: 6,
    Error: 7,
    Ping: 8,
    Pong: 9,
    JoinChannel: 10,
    WebRTCOffer: 18,
    WebRTCAnswer: 19,
    ICECandidate: 20,
    Snapshot: 14,
    Event: 15,
    AuthChallenge: 16,
    AuthSignature: 17,
    KeyPublish: 42,
    KeyRequest: 43,
    KeyResponse: 44,
    ChannelKey: 45,
    ChatHistory: 46,
    ChatHistoryResponse: 47,
    Typing: 53,
    ChatKeyRequest: 99,
    ChatKeyBundle: 100,
    ChannelSubscribe: 123,
    SubscriptionState: 124,
    VideoStreamControl: 166,
    VideoStreamResult: 167,
    AudioStateSet: 186,
    DeviceRequest: 195,
    DeviceResult: 196,
    DeviceEvent: 197,
});

const encoder = new TextEncoder();
const decoder = new TextDecoder();

/** Encodes one frame. */
export function encodeFrame(type, message) {
    const payload = encoder.encode(JSON.stringify(message ?? {}));
    if (payload.length + 2 > MAX_FRAME_SIZE) throw new Error("frame too large");
    const out = new Uint8Array(6 + payload.length);
    const view = new DataView(out.buffer);
    view.setUint32(0, payload.length + 2);
    view.setUint16(4, type);
    out.set(payload, 6);
    return out;
}

/** Accumulates stream bytes and yields complete frames. */
export class FrameReader {
    #buffer = new Uint8Array(0);

    /** Appends bytes and returns every complete frame as {type, payload}. */
    push(bytes) {
        const chunk = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
        const merged = new Uint8Array(this.#buffer.length + chunk.length);
        merged.set(this.#buffer);
        merged.set(chunk, this.#buffer.length);
        const frames = [];
        let offset = 0;
        while (merged.length - offset >= 4) {
            const view = new DataView(merged.buffer, merged.byteOffset + offset);
            const length = view.getUint32(0);
            if (length < 2 || length > MAX_FRAME_SIZE) throw new Error(`invalid frame length ${length}`);
            if (merged.length - offset < 4 + length) break;
            const type = view.getUint16(4);
            const body = merged.subarray(offset + 6, offset + 4 + length);
            frames.push({ type, payload: body.length ? JSON.parse(decoder.decode(body)) : {} });
            offset += 4 + length;
        }
        this.#buffer = merged.slice(offset);
        return frames;
    }
}
