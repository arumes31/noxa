import test from "node:test";
import assert from "node:assert/strict";
import { captureNotificationDestination, createNotificationNavigator } from "../src/notification-navigation.js";

const state = () => ({ activeTabID: "a", serverGeneration: 1, sessionGeneration: 0, myUniqueID: "identity-a", lastConnect: { addr: "a.test" } });
const destination = () => captureNotificationDestination("mention", { channelID: 7, reference: { kind: "channel", channel_id: 7, message_id: 42 } }, state());
function fixture({ confirmSwitch } = {}) {
    const current = state(), listeners = new Map(), opened = [], failures = [], switches = [];
    const emit = (name, value) => { for (const callback of listeners.get(name) || []) callback(value); };
    const reset = tab => { current.activeTabID = tab; current.serverGeneration++; current.myUniqueID = `identity-${tab}`; emit("tab_reset", tab); };
    const app = {
        ListTabs: async () => [{ id: "a", addr: "a.test", connected: true }, { id: "b", addr: "b.test", connected: true }],
        SetActiveTab: async tab => { switches.push(tab); reset(tab); emit("tab_replay_done", tab); },
        SessionInfoForTab: async () => ({ connected: true }),
        DMHistoryContextForTab: async tab => ({ tab_id: tab, identity_uid: current.myUniqueID }),
    };
    const navigate = createNotificationNavigator({ state: () => current, app: () => app,
        on: (name, callback) => { const set = listeners.get(name) || new Set(); set.add(callback); listeners.set(name, set); return () => set.delete(callback); },
        open: async ref => opened.push([current.activeTabID, ref]), unavailable: () => failures.push(true), confirmSwitch,
    });
    return { current, app, navigate, opened, failures, switches, reset, emit, listeners };
}

test("destinations preserve explicit source tab and message without copying plaintext", () => {
    const result = destination();
    assert.deepEqual(result.reference, { kind: "channel", channel_id: 7, message_id: 42 });
    assert.equal(result.identityUID, "identity-a");
    assert.equal(captureNotificationDestination("mention", { channelID: 0 }, state()).reference.channel_id, 0);
    assert.equal(captureNotificationDestination("dm", { tabID: "other", uid: "peer" }, state()), null);
    const thread = captureNotificationDestination("channel_message", { uid: "author", reference: { kind: "thread", channel_id: 7, thread_id: 8, message_id: 9, body: "secret" } }, state());
    assert.deepEqual(thread.reference, { kind: "thread", channel_id: 7, thread_id: 8, message_id: 9 });
});

test("cross-server destination waits for replay and verifies its original identity", async () => {
    const f = fixture(); f.reset("b");
    assert.equal(await f.navigate(destination()), true);
    assert.deepEqual(f.switches, ["a"]);
    assert.deepEqual(f.opened, [["a", destination().reference]]);
    assert.equal([...f.listeners.values()].reduce((sum, set) => sum + set.size, 0), 0);
});

for (const failure of ["missing", "disconnected", "address", "identity", "transport"]) test(`unavailable ${failure} never opens a source`, async () => {
    const f = fixture();
    if (failure === "missing") f.app.ListTabs = async () => [];
    if (failure === "disconnected") f.app.ListTabs = async () => [{ id: "a", connected: false }];
    if (failure === "address") f.app.ListTabs = async () => [{ id: "a", connected: true, addr: "replacement.test" }];
    if (failure === "identity") f.app.DMHistoryContextForTab = async () => ({ tab_id: "a", identity_uid: "replacement" });
    if (failure === "transport") f.app.SessionInfoForTab = async () => ({ connected: false });
    assert.equal(await f.navigate(destination()), false);
    assert.deepEqual(f.opened, []);
    assert.equal(f.failures.length, 1);
});

test("late tab lookup cannot override a newer server selection", async () => {
    const f = fixture(); let release;
    f.app.ListTabs = () => new Promise(resolve => { release = resolve; });
    const pending = f.navigate(destination()); f.reset("b");
    release([{ id: "a", addr: "a.test", connected: true }]);
    assert.equal(await pending, false); assert.deepEqual(f.switches, []); assert.deepEqual(f.opened, []);
});

test("a competing activation cancels the pending replay and removes its listeners", async () => {
    const f = fixture(); f.reset("b");
    f.app.SetActiveTab = async tab => { f.reset(tab); f.reset("b"); f.emit("tab_replay_done", tab); };
    assert.equal(await f.navigate(destination()), false);
    assert.deepEqual(f.opened, []); assert.deepEqual(f.failures, []);
    assert.equal([...f.listeners.values()].reduce((sum, set) => sum + set.size, 0), 0);
});

test("late authorization cannot open a message after another tab or identity change", async () => {
    const f = fixture(); let release;
    f.app.DMHistoryContextForTab = () => new Promise(resolve => { release = resolve; });
    const pending = f.navigate(destination());
    while (!release) await Promise.resolve();
    f.current.myUniqueID = "replacement";
    release({ tab_id: "a", identity_uid: "identity-a" });
    assert.equal(await pending, false); assert.deepEqual(f.opened, []);
});

test("failed activation reports unavailable and cleans its replay listeners", async () => {
    const f = fixture(); f.reset("b"); f.app.SetActiveTab = async () => "tab unavailable";
    assert.equal(await f.navigate(destination()), false); assert.deepEqual(f.opened, []);
    assert.equal(f.failures.length, 1);
    assert.equal([...f.listeners.values()].reduce((sum, set) => sum + set.size, 0), 0);
});

test("cancelling a media-preserving switch never activates the source tab", async () => {
    const f = fixture({ confirmSwitch: async () => false }); f.reset("b");
    assert.equal(await f.navigate(destination()), false);
    assert.deepEqual(f.switches, []); assert.deepEqual(f.opened, []);
});

test("confirmation cannot activate a source after the current server changes", async () => {
    let release;
    const f = fixture({ confirmSwitch: () => new Promise(resolve => { release = resolve; }) }); f.reset("b");
    const pending = f.navigate(destination());
    while (!release) await Promise.resolve();
    f.reset("c"); release(true);
    assert.equal(await pending, false); assert.deepEqual(f.switches, []);
});

test("confirmation rechecks that the source remains connected", async () => {
    const f = fixture({ confirmSwitch: async () => { f.app.ListTabs = async () => []; return true; } }); f.reset("b");
    assert.equal(await f.navigate(destination()), false); assert.deepEqual(f.switches, []);
    assert.equal(f.failures.length, 1);
});

test("verified session hydrates account membership before opening the destination", async () => {
    const f = fixture(); f.current.myClientID = "";
    f.app.SessionInfoForTab = async () => ({ connected: true, client_id: "account-client" });
    assert.equal(await f.navigate(destination()), true);
    assert.equal(f.current.myClientID, "account-client");
});
