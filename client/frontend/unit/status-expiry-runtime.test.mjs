import assert from "node:assert/strict";
import { test } from "node:test";
import { setImmediate } from "node:timers/promises";

test("runtime keeps offline expiry and retries on its original server", async t => {
    t.mock.timers.enable({ apis: ["setTimeout", "setInterval", "Date"], now: 100_000 });
    const original = new Map(["window", "document", "localStorage"].map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
    t.after(() => { for (const [key, descriptor] of original) { if (descriptor) Object.defineProperty(globalThis, key, descriptor); else delete globalThis[key]; } });
    const storage = new Map([["noxa.status.expiry", JSON.stringify({ uniqueID: "alice", tabID: "a", server: "alpha", status: "away", expiresAt: 90_000 })]]);
    globalThis.localStorage = { getItem: key => storage.get(key), setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key) };
    let tabs = [], calls = [];
    const state = { activeTabID: "b", myUniqueID: "bob", myClientID: "bob-client", myStatus: "busy", clients: [] };
    globalThis.document = new EventTarget();
    globalThis.window = Object.assign(new EventTarget(), {
        __noxa: { state, sysMsg() {}, toast() {} },
        go: { main: { App: {
            ListTabs: async () => tabs,
            SetStatusForTab: async (id, status) => { calls.push([id, status]); return id === state.activeTabID ? "" : "server tab changed; refresh the editor"; },
        } } },
    });
    const runtime = await import("../src/status-expiry-runtime.js");
    const scheduler = runtime.initStatusExpiry();
    scheduler.check();
    await setImmediate();
    assert.ok(scheduler.current(), "offline expiry is not consumed by the runtime wrapper");
    assert.ok(storage.has("noxa.status.expiry"));
    // A reused tab ID must not expire a different server's status.
    tabs = [{ id: "a", addr: "beta", connected: true }];
    scheduler.check(); await setImmediate();
    assert.deepEqual(calls, []);
    tabs = [{ id: "a", addr: "alpha", connected: true }];
    scheduler.check(); await setImmediate();
    assert.ok(scheduler.current(), "an unrelated active identity does not discard the record");
    assert.deepEqual(calls, [], "background tabs are left untouched until their account can be checked");
    state.activeTabID = "a"; state.myUniqueID = "alice"; state.myClientID = "alice-client"; state.myStatus = "away";
    state.clients = [{ client_id: "alice-client", status: "away" }];
    scheduler.check(); await setImmediate();
    assert.equal(scheduler.current(), null);
    assert.equal(storage.has("noxa.status.expiry"), false);
    assert.equal(state.myStatus, "");
    scheduler.dispose();
});
