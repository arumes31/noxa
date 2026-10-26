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
