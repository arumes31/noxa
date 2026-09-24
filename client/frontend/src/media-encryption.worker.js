// Keep encryption/codec handling in the maintained implementation. The wrapper
// adds authenticated replay rejection, absent from its frame protocol.
import "livekit-client/e2ee-worker";
import { MediaReplayWindow } from "./media-replay.js";

const delegate = self.onmessage;
const bindings = new Map();
const windows = new Map();

self.onmessage = event => {
    const { kind, data } = event.data;
    if (kind === "setSifTrailer" || (kind === "enable" && data.enabled !== true)) return;
    if (kind === "updateCodec") {
        const binding = bindings.get(data.previousTrackId || data.trackId);
        // Identity changes require a fresh endpoint. The maintained worker's
        // asynchronous queue cannot atomically rebind our replay transform.
        if (!binding || binding.identity !== data.participantIdentity || data.trackId !== data.previousTrackId) return;
    }
    if (kind === "encode") bindings.set(data.trackId, { identity: data.participantIdentity });
    if (kind !== "decode") { delegate(event); return; }
    const binding = { identity: data.participantIdentity }; bindings.set(data.trackId, binding);
    const inputs = new WeakMap();
    const readable = data.readableStream.pipeThrough(new TransformStream({
        transform(frame, controller) {
            const bytes = new Uint8Array(frame.data);
            // Empty DTX carries no content. No plaintext/SIF bypass is accepted.
            if (bytes.length < 31 || bytes[bytes.length - 2] !== 12) return;
            inputs.set(frame, { identity: binding.identity, index: bytes[bytes.length - 1], iv: bytes.slice(bytes.length - 14, bytes.length - 2) });
            controller.enqueue(frame);
        },
    }));
    const authenticated = new TransformStream({
        transform(frame, controller) {
            // The pinned cryptor mutates/enqueues the same frame object only
            // after GCM verifies. A dropped corrupt frame cannot shift a FIFO.
            const input = inputs.get(frame); inputs.delete(frame);
            if (!input || input.identity !== binding.identity) return;
            const key = `${input.identity}:${input.index}`;
            let window = windows.get(key);
            if (!window) { window = new MediaReplayWindow(); windows.set(key, window); }
            if (window.accept(input.iv)) controller.enqueue(frame);
        },
    });
    void authenticated.readable.pipeTo(data.writableStream).catch(() => {});
    delegate({ data: { kind, data: { ...data, readableStream: readable, writableStream: authenticated.writable } } });
};
