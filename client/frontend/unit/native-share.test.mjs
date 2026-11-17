import test from "node:test";
import assert from "node:assert/strict";

const store = new Map();
globalThis.localStorage = { getItem: k => store.get(k) ?? null, setItem: (k, v) => store.set(k, String(v)) };
globalThis.document = { title: "noXa" };
const calls = [];
globalThis.window = { __noxa: { state: { activeTabID: "t1" } }, go: { main: { App: {
    NativeScreenKeyForTab: async (tab, body) => { calls.push([tab, body]); await new Promise(r => setTimeout(r, body.op === "key" ? 5 : 0)); },
} } } };
const { screenCaptureBackend, setScreenCaptureBackend, nativeSource, nativeSourceOptions, mirrorNativeKey, nativeEventIsCurrent } = await import("../src/native-share.js");

test("capture backend defaults to Browser and remembers Native", () => {
    assert.equal(screenCaptureBackend(), "browser");
    setScreenCaptureBackend("native");
    assert.equal(screenCaptureBackend(), "native");
    setScreenCaptureBackend("bogus");
    assert.equal(screenCaptureBackend(), "browser");
});

test("native sources validate the picker value against the surface", () => {
    assert.deepEqual(nativeSource("monitor", "m:1"), { monitor: 1, window: "0" });
    assert.deepEqual(nativeSource("window", "w:4242"), { monitor: 0, window: "4242" });
    assert.equal(nativeSource("window", "m:1"), null);
    assert.equal(nativeSource("region", "m:0"), null);
    assert.equal(nativeSource("window", "w:0"), null);
    const status = { monitors: [{ index: 0, width: 1920, height: 1080, primary: false }, { index: 1, width: 2560, height: 1440, primary: true }],
        windows: [{ handle: "7", title: "Game" }, { handle: "8", title: "noXa" }, { handle: "9", title: "" }] };
    assert.deepEqual(nativeSourceOptions(status, "monitor").map(o => [o.value, o.selected]), [["m:0", false], ["m:1", true]]);
    assert.deepEqual(nativeSourceOptions(status, "window").map(o => o.value), ["w:7"]);
});

test("only the own screen slot keys are mirrored, in order", async () => {
    mirrorNativeKey("self|cam", { op: "activate", epoch: 1 }, "t1");
    mirrorNativeKey("self|screen", { op: "key", epoch: 2, kid: 3, key: new Uint8Array(32).fill(1) }, "t1");
    mirrorNativeKey("self|screen", { op: "activate", epoch: 2 }, "t1");
    await new Promise(r => setTimeout(r, 30));
    assert.deepEqual(calls.map(([tab, body]) => [tab, body.op, body.epoch]), [["t1", "key", 2], ["t1", "activate", 2]]);
    assert.equal(calls[0][1].key, Buffer.from(new Uint8Array(32).fill(1)).toString("base64"));
    assert.equal(calls[0][1].kid, 3);
});

test("key operations go to the owning session's tab, never the active one", async () => {
    calls.length = 0;
    window.__noxa.state.activeTabID = "t2";
    mirrorNativeKey("self|screen", { op: "forget" }, "t1");
    mirrorNativeKey("self|screen", { op: "activate", epoch: 4 }); // no owner (a call session): not mirrored
    await new Promise(r => setTimeout(r, 10));
    assert.deepEqual(calls.map(([tab, body]) => [tab, body.op]), [["t1", "forget"]]);
});

test("native share events match their share and tab", () => {
    const native = { id: "s2", tabID: "t1" };
    assert.equal(nativeEventIsCurrent({ tab: "t1", share: "s2", state: "ended" }, native), true);
    assert.equal(nativeEventIsCurrent({ tab: "t1", share: "s1", state: "ended" }, native), false);
    assert.equal(nativeEventIsCurrent({ tab: "t2", share: "s2", state: "ended" }, native), false);
    assert.equal(nativeEventIsCurrent({ tab: "t1", share: "s2", state: "ended" }, null), false);
});
