// Same pinned vodozemac WASM as internal/e2ee/ratchet. This module does not
// authorize devices or persist state. See the core README before integrating.
import { CORE_SHA256 } from "./ratchet-integrity.js";

export const RATCHET_VERSION = "noxa-olm-v1";
const CORE_URL = new URL("../../../internal/e2ee/ratchet/core.wasm", import.meta.url);
const MAX_WASM = 2 << 20;
const MAX_INPUT = 1 << 20;
const TIMEOUT = 5000;
// Local environment failures (timeout, worker, busy, closed, cancelled) carry
// code core_unavailable: retryable, never evidence about a ciphertext. Errors
// reported by the core itself (via the worker) stay uncoded.
const unavailable = message => Object.assign(new Error(message), { code: "core_unavailable" });

async function loadBytes(signal) {
    const response = await fetch(CORE_URL, { signal, credentials: "same-origin" });
    if (!response.ok || !response.body || Number(response.headers.get("content-length")) > MAX_WASM) throw new Error("ratchet core unavailable");
    const reader = response.body.getReader();
    const chunks = [];
    let length = 0;
    try {
        for (;;) {
            const { value, done } = await reader.read();
            if (done) break;
            length += value.length;
            if (length > MAX_WASM) throw new Error("ratchet core too large");
            chunks.push(value);
        }
    } finally { await reader.cancel(); }
    const bytes = new Uint8Array(length);
    let offset = 0;
    for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length; }
    return bytes;
}

/**
 * Load and authenticate the bundled core. bytes is useful to offline packagers
 * and tests; it must match the same embedded hash. No alternate core is accepted.
 * apply expects key: Uint8Array(32); all other fields match the Go Request JSON.
 * Returned account/session strings are protected snapshots, NOT committed state.
 */
export async function createRatchetCore({ bytes, signal } = {}) {
    const timeout = AbortSignal.timeout(TIMEOUT);
    const loadingSignal = signal ? AbortSignal.any([signal, timeout]) : timeout;
    loadingSignal.throwIfAborted();
    const binary = bytes ? new Uint8Array(bytes) : await loadBytes(loadingSignal);
    loadingSignal.throwIfAborted();
    if (!binary.length || binary.length > MAX_WASM) throw new Error("invalid ratchet core size");
    const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", binary));
    loadingSignal.throwIfAborted();
    if ([...hash].map(b => b.toString(16).padStart(2, "0")).join("") !== CORE_SHA256) throw new Error("ratchet core hash mismatch");
    const module = await WebAssembly.compile(binary);
    loadingSignal.throwIfAborted();
    const active = new Set();
    let closed = false;
    return {
        async apply(request, { signal } = {}) {
            if (closed) throw unavailable("ratchet core closed");
            if (signal?.aborted) throw unavailable("ratchet operation cancelled");
            if (active.size >= 2) throw unavailable("ratchet core busy");
            if (!(request?.key instanceof Uint8Array) || request.key.length !== 32) throw new Error("invalid storage key");
            // Avoid posting an unbounded object to a worker. The worker and core
            // independently check encoded input and plaintext bounds as well.
            const size = new TextEncoder().encode(JSON.stringify({ ...request, key: "" })).length;
            if (size > MAX_INPUT - 128) throw new Error("ratchet request too large");
            return new Promise((resolve, reject) => {
                let worker;
                try { worker = new Worker(new URL("./ratchet.worker.js", import.meta.url), { type: "module" }); }
                catch { reject(unavailable("ratchet workers unavailable")); return; }
                let settled = false;
                const finish = (error, response, environment = true) => {
                    if (settled) return;
                    settled = true;
                    clearTimeout(timer);
                    signal?.removeEventListener("abort", aborted);
                    active.delete(cancel);
                    worker.terminate();
                    if (error) reject(environment ? unavailable(error) : new Error(error)); else resolve(response);
                };
                const aborted = () => finish("ratchet operation cancelled");
                const cancel = () => finish("ratchet core closed");
                const timer = setTimeout(() => finish("ratchet operation timed out"), TIMEOUT);
                active.add(cancel);
                signal?.addEventListener("abort", aborted, { once: true });
                // Only a refusal by the core is deterministic; a runtime failure
                // inside the worker is reported as unavailable (retryable).
                worker.onmessage = event => finish(event.data.error, event.data.response, !event.data.core);
                worker.onerror = () => finish("ratchet worker failed");
                try { worker.postMessage({ module, request }); }
                catch { finish("invalid ratchet request"); }
            });
        },
        close() { closed = true; for (const cancel of [...active]) cancel(); },
    };
}
