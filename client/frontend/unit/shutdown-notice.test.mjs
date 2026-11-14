import test from "node:test";
import assert from "node:assert/strict";
import { createShutdownNotice } from "../src/shutdown-notice.js";

test("a disconnect right after an announced shutdown is suppressed once", () => {
    let t = 1000;
    const n = createShutdownNotice(() => t);
    n.announce("tabA", 30000);
    assert.equal(n.consume("tabA"), true, "the drop after the announcement is expected");
    assert.equal(n.consume("tabA"), false, "a later drop announces normally (entry consumed)");
});

test("an announcement for one tab does not suppress another tab's drop", () => {
    const n = createShutdownNotice(() => 0);
    n.announce("tabA", 30000);
    assert.equal(n.consume("tabB"), false);
});

test("a stale announcement no longer suppresses", () => {
    let t = 0;
    const n = createShutdownNotice(() => t);
    n.announce("tabA", 5000);
    t = 6000;
    assert.equal(n.consume("tabA"), false, "past the ttl, the drop announces normally");
});

test("no announcement means a normal drop announces", () => {
    const n = createShutdownNotice(() => 0);
    assert.equal(n.consume("tabA"), false);
});

test("forget clears a pending marker (tab reconnect / background drop)", () => {
    const n = createShutdownNotice(() => 0);
    n.announce("tabA", 30000);
    n.forget("tabA");
    assert.equal(n.consume("tabA"), false, "a reconnected tab has no stale marker");
});
