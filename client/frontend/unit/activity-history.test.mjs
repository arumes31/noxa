import test from "node:test";
import assert from "node:assert/strict";
import { createActivityHistory } from "../src/activity-history.js";
const entry = (id, text = `message ${id}`) => ({ key: `channel:1:${id}`, cursor: id, message: { text } });
const source = { key: "channel:1", kind: "channel", id: 1 };
const setup = overrides => createActivityHistory({ current: () => true, listSources: async () => [source], pause: async () => {}, ...overrides });

test("pages retain all entries and do not duplicate overlapping live history", async () => {
    const requested = [];
    const feed = setup({ loadPage: async (_source, before) => {
        requested.push(before);
        return before ? { entries: [entry(2), entry(1)], cursor: 1, more: false } : { entries: [entry(3), entry(2)], cursor: 2, more: true };
    } });
    await feed.refresh(); await feed.older();
    assert.deepEqual(requested, [0, 2]);
    assert.equal(feed.entries().length, 3);
    assert.equal(feed.status().more, false);
});

test("refresh fills a gap from newer activity before continuing older history", async () => {
    let latest = 3;
    const requested = [];
    const feed = setup({ loadPage: async (_source, before) => {
        requested.push(before);
        const top = before ? before - 1 : latest;
        return { entries: [entry(top), entry(top - 1)], cursor: top - 1, more: top > 2 };
    } });
    await feed.refresh(); latest = 8; await feed.refresh(); await feed.older();
    assert.deepEqual(requested, [0, 0, 7]);
    assert.equal(feed.entries().length, 6);
});

test("revoked sources and rejected pages drop retained private bodies", async () => {
    let allowed = true, reject = false;
    const feed = setup({ listSources: async () => allowed ? [source] : [], loadPage: async () => {
        if (reject) return { denied: true };
        return { entries: [entry(1, "private")], cursor: 1, more: false };
    } });
    await feed.refresh(); reject = true; await feed.refresh(); assert.deepEqual(feed.entries(), []);
    reject = false; await feed.refresh(); allowed = false; await feed.refresh(); assert.deepEqual(feed.entries(), []);
});

test("a stale response cannot repopulate after invalidation or disposal", async () => {
    let finish;
    const feed = setup({ loadPage: () => new Promise(resolve => { finish = resolve; }) });
    const pending = feed.refresh();
    while (!finish) await new Promise(resolve => setImmediate(resolve));
    feed.invalidate(); finish({ entries: [entry(1)], cursor: 1, more: false }); await pending;
    assert.deepEqual(feed.entries(), []);
    feed.dispose(); await feed.refresh(); assert.deepEqual(feed.entries(), []);
});

test("only one paging batch runs at once and late errors cannot erase its replacement", async () => {
    let finish, first = true;
    const feed = setup({ loadPage: async () => {
        if (first) { first = false; return new Promise((_resolve, reject) => { finish = reject; }); }
        return { entries: [entry(2)], cursor: 2, more: false };
    } });
    const pending = feed.refresh();
    while (!finish) await new Promise(resolve => setImmediate(resolve));
    feed.invalidate(); await feed.refresh(); finish(new Error("old error")); await pending;
    assert.deepEqual(feed.entries().map(e => e.cursor), [2]);
    assert.equal(feed.status().failed, false);
});

test("errors are visible and retry can recover without marking anything read", async () => {
    let fail = true;
    const feed = setup({ loadPage: async () => {
        if (fail) throw new Error("offline");
        return { entries: [entry(1)], cursor: 1, more: false };
    } });
    await feed.refresh(); assert.equal(feed.status().failed, true);
    fail = false; await feed.refresh(); assert.equal(feed.status().failed, false); assert.equal(feed.entries().length, 1);
});

test("an authoritative complete page removes deleted or expired cached rows", async () => {
    let rows = [entry(2), entry(1)];
    const feed = setup({ loadPage: async () => ({ entries: rows, cursor: rows.at(-1)?.cursor || 0, more: false }) });
    await feed.refresh(); rows = [entry(2)]; await feed.refresh();
    assert.deepEqual(feed.entries().map(e => e.cursor), [2]);
    rows = []; await feed.refresh(); assert.deepEqual(feed.entries(), []);
});
