import assert from "node:assert/strict";
import { test } from "node:test";

import { _clear, entries, formatArg, formatLine, installLogger, LOG_LIMIT, logText, record, RingBuffer } from "../src/core/log.js";

test("the ring buffer keeps the newest items in order", () => {
    const ring = new RingBuffer(3);
    for (let i = 1; i <= 5; i++) ring.push(i);
    assert.deepEqual(ring.toArray(), [3, 4, 5]);
    assert.equal(ring.length, 3);
    ring.clear();
    assert.deepEqual(ring.toArray(), []);
    ring.push("a");
    assert.deepEqual(ring.toArray(), ["a"]);
});

test("the log keeps the last 500 lines", () => {
    _clear();
    for (let i = 0; i < LOG_LIMIT + 20; i++) record("log", [`line ${i}`], i);
    const all = entries();
    assert.equal(all.length, 500);
    assert.equal(all[0].text, "line 20");
    assert.equal(all.at(-1).text, `line ${LOG_LIMIT + 19}`);
});

test("arguments format as text", () => {
    assert.equal(formatArg("x"), "x");
    assert.equal(formatArg({ a: 1 }), '{"a":1}');
    const circular = {};
    circular.self = circular;
    assert.equal(formatArg(circular), "[object Object]");
    assert.match(formatArg(new Error("boom")), /boom/);
    assert.equal(formatLine({ at: 0, level: "warn", text: "hi" }), "1970-01-01T00:00:00.000Z warn  hi");
    _clear();
    record("info", ["a".repeat(5000)]);
    assert.ok(entries()[0].text.length <= 4001);
});

test("installLogger wraps console and still calls through", () => {
    _clear();
    const seen = [];
    const listeners = {};
    const fake = { console: { log: (...a) => seen.push(a), warn: (...a) => seen.push(a) }, addEventListener: (type, fn) => { listeners[type] = fn; } };
    installLogger(fake);
    fake.console.log("hello", 1);
    fake.console.warn("careful");
    listeners.error({ error: new Error("uncaught"), filename: "app.js", lineno: 3 });
    listeners.unhandledrejection({ reason: "nope" });
    assert.equal(seen.length, 2);
    const text = logText();
    assert.match(text, /log {3}hello 1/);
    assert.match(text, /warn {2}careful/);
    assert.match(text, /error Error: uncaught/);
    assert.match(text, /unhandled rejection: nope/);
});
