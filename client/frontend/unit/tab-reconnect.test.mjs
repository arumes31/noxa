import { test } from "node:test";
import assert from "node:assert/strict";
import { createTabReconnects, reconnectDelay } from "../src/tab-reconnect.js";

test("outages retry beyond five attempts and stop on successful recovery", async t => {
    t.mock.timers.enable({ apis: ["setTimeout", "setInterval"] });
    let attempts = 0;
    const reconnects = createTabReconnects({
        enabled: () => true, delay: () => 10,
        connect: async () => (++attempts < 9 ? { error: "offline" } : {}),
        complete: async () => true,
    });
    reconnects.start("a", { addr: "original" });
    for (let i = 0; i < 12; i++) {
        t.mock.timers.tick(10);
        for (let j = 0; j < 8; j++) await Promise.resolve();
    }
    assert.equal(attempts, 9);
    reconnects.cancelAll();
});

test("terminal authentication failure and explicit cancellation stop retries", async t => {
    t.mock.timers.enable({ apis: ["setTimeout", "setInterval"] });
    let attempts = 0;
    let terminal = "";
    const reconnects = createTabReconnects({
        enabled: () => true, delay: () => 10,
        connect: async () => { attempts++; return { error: "invalid credentials", terminal: true }; },
        stopped: (_tab, error) => { terminal = error; },
    });
    reconnects.start("a", {});
    t.mock.timers.tick(10);
    for (let j = 0; j < 8; j++) await Promise.resolve();
    t.mock.timers.tick(10000);
    assert.equal(attempts, 1);
    assert.equal(terminal, "invalid credentials");
    reconnects.start("b", {});
    reconnects.cancel("b");
    t.mock.timers.tick(10000);
    assert.equal(attempts, 1);
});

test("backoff is exponential, jittered and capped at thirty seconds", () => {
    assert.deepEqual([1, 2, 3, 4, 1000].map(attempt => reconnectDelay(attempt, () => 0)), [5000, 8000, 16000, 24000, 24000]);
    for (let attempt = 1; attempt < 100; attempt++) {
        assert.ok(reconnectDelay(attempt, () => 0.99999) <= 30000);
    }
});
