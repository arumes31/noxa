// Runs one operation in a disposable worker. No network or durable writes.
const MAX_INPUT = 1 << 20;
const MAX_OUTPUT = 2 << 20;
self.onmessage = async ({ data: { module, request } }) => {
    let instance;
    let input;
    try {
        if (!(request.key instanceof Uint8Array) || request.key.length !== 32) throw new Error("invalid storage key");
        let key = "";
        for (const byte of request.key) key += String.fromCharCode(byte);
        input = new TextEncoder().encode(JSON.stringify({ ...request, key: btoa(key) }));
        request.key.fill(0);
        if (input.length > MAX_INPUT) throw new Error("ratchet request too large");
        instance = await WebAssembly.instantiate(module, { noxa: { random_fill(ptr, length) {
            if (length > 65536) return 1;
            try { crypto.getRandomValues(new Uint8Array(instance.exports.memory.buffer, ptr, length)); return 0; } catch { return 1; }
        } } });
        const core = instance.exports;
        const ptr = core.alloc(input.length);
        if (!ptr) throw new Error("ratchet allocation failed");
        new Uint8Array(core.memory.buffer, ptr, input.length).set(input);
        const result = core.run(ptr, input.length);
        const outputPtr = Number(result & 0xffffffffn), length = Number(result >> 32n);
        if (!result || length > MAX_OUTPUT) throw new Error("invalid ratchet response");
        const response = JSON.parse(new TextDecoder().decode(new Uint8Array(core.memory.buffer, outputPtr, length)));
        if (response.error) throw new Error(response.error);
        self.postMessage({ response });
    } catch (error) {
        // Core errors are fixed strings. Runtime errors may contain paths or
        // implementation data, so only expose a bounded generic failure here.
        self.postMessage({ error: "ratchet operation rejected" });
    } finally {
        input?.fill(0);
        if (instance) new Uint8Array(instance.exports.memory.buffer).fill(0);
        self.close();
    }
};
