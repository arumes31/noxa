import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { createRatchetCore } from "../src/core/ratchet-core.js";

test("ratchet core refuses corrupt or unbounded replacements", async () => {
    const core = new Uint8Array(await readFile(new URL("../../internal/e2ee/ratchet/core.wasm", import.meta.url)));
    core[core.length - 1] ^= 1;
    await assert.rejects(createRatchetCore({ bytes: core }), /hash mismatch/);
    await assert.rejects(createRatchetCore({ bytes: new Uint8Array(2 ** 21 + 1) }), /size/);
});

test("ratchet wrapper rejects missing storage keys and closed instances", async () => {
    const bytes = await readFile(new URL("../../internal/e2ee/ratchet/core.wasm", import.meta.url));
    const core = await createRatchetCore({ bytes });
    await assert.rejects(core.apply({ op: "new_account" }), /storage key/);
    core.close();
    await assert.rejects(core.apply({ op: "new_account", key: new Uint8Array(32) }), /closed/);
});

test("ratchet initialization rejects supplied bytes when already cancelled", async t => {
    const bytes = await readFile(new URL("../../internal/e2ee/ratchet/core.wasm", import.meta.url));
    const controller = new AbortController();
    controller.abort();
    const digest = t.mock.method(crypto.subtle, "digest");
    await assert.rejects(createRatchetCore({ bytes, signal: controller.signal }), { name: "AbortError" });
    assert.equal(digest.mock.callCount(), 0, "cancelled initialization must not start hashing");
});

for (const stage of ["digest", "compile"]) {
    test(`ratchet initialization rejects cancellation during ${stage}`, async t => {
        const bytes = await readFile(new URL("../../internal/e2ee/ratchet/core.wasm", import.meta.url));
        const controller = new AbortController();
        const owner = stage === "digest" ? crypto.subtle : WebAssembly;
        const original = owner[stage].bind(owner);
        t.mock.method(owner, stage, async (...args) => {
            const result = await original(...args);
            controller.abort();
            return result;
        });
        await assert.rejects(createRatchetCore({ bytes, signal: controller.signal }), { name: "AbortError" });
    });
}

// PR #212 review: a runtime failure inside the worker (e.g. WebAssembly
// instantiation) is environmental and must stay retryable (core_unavailable);
// only a refusal by the core itself is deterministic.
for (const [name, reply, code] of [
    ["runtime failure", { error: "ratchet runtime failed" }, "core_unavailable"],
    ["core refusal", { error: "ratchet operation rejected", core: true }, undefined],
]) {
    test(`ratchet worker ${name} is classified correctly`, async t => {
        const bytes = await readFile(new URL("../../internal/e2ee/ratchet/core.wasm", import.meta.url));
        const core = await createRatchetCore({ bytes });
        const original = globalThis.Worker;
        globalThis.Worker = class {
            postMessage() { queueMicrotask(() => this.onmessage?.({ data: reply })); }
            terminate() {}
        };
        t.after(() => { globalThis.Worker = original; core.close(); });
        const error = await core.apply({ op: "new_account", key: new Uint8Array(32) }).catch(e => e);
        assert.ok(error instanceof Error);
        assert.equal(error.code, code);
    });
}
