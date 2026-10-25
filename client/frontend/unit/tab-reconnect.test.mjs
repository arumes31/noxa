import { test } from "node:test";
import assert from "node:assert/strict";
import { createTabReconnects, reconnectDelay, reconnectDecision } from "../src/tab-reconnect.js";

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

test("retry now skips backoff, refuses parallel attempts and works with auto retry disabled", async t => {
    t.mock.timers.enable({apis:["setTimeout","setInterval"]});let calls=0,release;
    const reconnects=createTabReconnects({enabled:()=>false,connect:async()=>{calls++;return await new Promise(resolve=>release=resolve);},complete:async()=>true});
    assert.equal(reconnects.retryNow("a",{addr:"original"}),true);t.mock.timers.tick(0);await Promise.resolve();
    assert.equal(calls,1);assert.equal(reconnects.retryNow("a"),false);release({error:"offline"});for(let i=0;i<8;i++)await Promise.resolve();t.mock.timers.tick(60000);assert.equal(calls,1);reconnects.cancelAll();
});

test("(314) reconnectDecision stops on terminal flags and rejected logins, keeps capped backoff for network loss", () => {
    assert.deepEqual(reconnectDecision({ terminal: true, error: "offline" }), { retry: false, reason: "terminal" });
    for (const error of ["invalid credentials", "invalid server password", "banned", "authentication failed", "permission denied", "tls: failed to verify certificate: x509: certificate signed by unknown authority"]) {
        const decision = reconnectDecision({ error, attempts: 1 });
        assert.equal(decision.retry, false, error);
        assert.equal(decision.reason, "authentication", error);
    }
    for (const error of ["dial tcp 10.0.0.5:12333: connection refused", "dial tcp: lookup voice.example: no such host", "i/o timeout", "too many failed logins, try again later", "server tab is not available for automatic reconnect", ""]) {
        const decision = reconnectDecision({ error, attempts: 7 });
        assert.equal(decision.retry, true, error);
        assert.equal(decision.reason, "network", error);
        assert.ok(decision.delay >= 5000 && decision.delay <= 30000, `${error}: delay ${decision.delay}`);
    }
    assert.equal(reconnectDecision({ error: "offline", attempts: 3, maxAttempts: 3 }).reason, "exhausted");
    assert.equal(reconnectDelay(50) <= 30000, true, "delay cap holds for very long outages");
});

test("(314) the retry series stops after a rejected login even when the native result lacks the terminal flag", async t => {
    t.mock.timers.enable({ apis: ["setTimeout", "setInterval"] });
    let attempts = 0, stopped = null;
    const reconnects = createTabReconnects({
        enabled: () => true, delay: () => 10,
        connect: async () => { attempts++; return { error: "invalid credentials" }; },
        complete: async () => true,
        stopped: (_tab, error, cause) => { stopped = { error, cause }; },
    });
    reconnects.start("a", { addr: "original" });
    for (let i = 0; i < 6; i++) { t.mock.timers.tick(10); await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); }
    assert.equal(attempts, 1, "no second automatic attempt after a rejected password");
    assert.deepEqual(stopped, { error: "invalid credentials", cause: "authentication" });
    // A manual retry is still possible and runs exactly once more.
    assert.equal(reconnects.retryNow("a", { addr: "original" }), true);
    t.mock.timers.tick(0); await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    assert.equal(attempts, 2);
});
