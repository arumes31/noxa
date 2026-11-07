import assert from "node:assert/strict";
import test from "node:test";
import { lazyModule, preloadWhenIdle } from "../src/lazy-module.js";
import { hotkeyErrors, recordHotkeyStatus } from "../src/hotkey-status.js";

test("a lazy module imports once and is current afterwards", async () => {
    let imports = 0;
    const lazy = lazyModule(async () => { imports++; return { value: 42 }; });
    assert.equal(lazy.current(), null);
    const [a, b] = await Promise.all([lazy.load(), lazy.load()]);
    assert.equal(imports, 1, "concurrent callers share one import");
    assert.equal(a, b);
    assert.equal(lazy.current().value, 42);
    await lazy.load();
    assert.equal(imports, 1);
});

test("a failed import is forgotten so the next call retries", async () => {
    let attempt = 0;
    const lazy = lazyModule(async () => {
        attempt++;
        if (attempt === 1) throw new Error("chunk missing");
        return { ok: true };
    });
    await assert.rejects(lazy.load(), /chunk missing/);
    assert.equal(lazy.current(), null);
    assert.deepEqual(await lazy.load(), { ok: true });
    assert.equal(attempt, 2);
});

test("preloading waits for idle time and swallows a failure", async () => {
    const calls = [];
    let idle;
    const scheduler = { requestIdleCallback: (fn, opts) => { calls.push(opts); idle = fn; } };
    const lazy = lazyModule(async () => { throw new Error("offline"); });
    preloadWhenIdle(lazy, { scheduler, timeoutMs: 1234 });
    assert.deepEqual(calls, [{ timeout: 1234 }]);
    idle();
    await new Promise(resolve => setTimeout(resolve, 0)); // no unhandled rejection
    const timers = [];
    preloadWhenIdle(lazyModule(async () => ({})), { scheduler: { setTimeout: (fn, ms) => timers.push(ms) }, timeoutMs: 4000 });
    assert.deepEqual(timers, [1500], "without requestIdleCallback a short timer is used");
});

test("hotkey errors are recorded before Settings opens and update visible rows", () => {
    hotkeyErrors.clear();
    const err = { textContent: "" };
    const row = { dataset: { hotkeyAction: "ptt" }, querySelector: () => err };
    const other = { dataset: { hotkeyAction: "mute" }, querySelector: () => { throw new Error("not this row"); } };
    const doc = { querySelectorAll: () => [row, other] };
    recordHotkeyStatus({ action: "ptt", error: "already registered" }, doc);
    assert.equal(hotkeyErrors.get("ptt"), "already registered");
    assert.equal(err.textContent, "already registered");
    recordHotkeyStatus({ action: "ptt", error: "" }, doc);
    assert.equal(hotkeyErrors.has("ptt"), false);
    assert.equal(err.textContent, "");
    recordHotkeyStatus(null, doc);
    recordHotkeyStatus({ action: "x", error: "e" }, undefined);
    assert.equal(hotkeyErrors.get("x"), "e");
});
