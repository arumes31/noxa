import test from "node:test";
import assert from "node:assert/strict";

const store = new Map();
globalThis.localStorage = { getItem: k => store.get(k) ?? null, setItem: (k, v) => store.set(k, String(v)) };
globalThis.document = { title: "noXa" };
const calls = [];
globalThis.window = { __noxa: { state: { activeTabID: "t1" } }, go: { main: { App: {
    NativeScreenKeyForTab: async (tab, body) => { calls.push([tab, body]); await new Promise(r => setTimeout(r, body.op === "key" ? 5 : 0)); },
    NativeScreenThumbnails: async values => Object.fromEntries(values.filter(v => v !== "w:9").map(v => [v, `data:image/jpeg;base64,${v}`])),
} } } };
const { screenCaptureBackend, setScreenCaptureBackend, nativeSource, nativeSourceOptions, mirrorNativeKey, nativeEventIsCurrent,
    rememberNativeSource, rememberedNativeSource, nativeThumbnails } = await import("../src/native-share.js");

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
        windows: [{ handle: "7", title: "Game" }, { handle: "9", title: "" }] };
    assert.deepEqual(nativeSourceOptions(status, "monitor", null).map(o => [o.value, o.selected]), [["m:0", false], ["m:1", true]]);
    assert.deepEqual(nativeSourceOptions(status, "window", null).map(o => o.value), ["w:7"]);
    assert.deepEqual(nativeSourceOptions(status, "region", null), []);
});

const desktop = {
    foreground: "30",
    monitors: [{ index: 0, width: 1920, height: 1080, primary: true }, { index: 1, width: 2560, height: 1440, primary: false }],
    windows: [ // z-order
        { handle: "10", title: "Inbox - Mail", exe: "mail.exe", fullscreen: false },
        { handle: "20", title: "Video", exe: "player.exe", fullscreen: true },
        { handle: "30", title: "Space Game", exe: "Game.exe", fullscreen: true },
        { handle: "40", title: "Docs - Editor", exe: "editor.exe", fullscreen: false },
        { handle: "50", title: "Notes - Editor", exe: "editor.exe", fullscreen: false },
    ],
};
const picked = options => options.filter(o => o.selected).map(o => o.value);

test("the running game comes first and is preselected", () => {
    const options = nativeSourceOptions(desktop, "window", null);
    assert.deepEqual(options.map(o => o.value), ["w:30", "w:20", "w:10", "w:40", "w:50"]); // focused fullscreen, other fullscreen, z-order
    assert.deepEqual(picked(options), ["w:30"]);
    assert.equal(options[0].game, true);
    assert.equal(options[0].exe, "Game.exe");
    // Without any fullscreen window the first (topmost) window is selected.
    const plain = { windows: desktop.windows.map(w => ({ ...w, fullscreen: false })) };
    assert.deepEqual(picked(nativeSourceOptions(plain, "window", null)), ["w:10"]);
});

test("the remembered source wins: executable and title, then executable", () => {
    assert.deepEqual(picked(nativeSourceOptions(desktop, "window", { surface: "window", exe: "editor.exe", title: "Notes - Editor" })), ["w:50"]);
    // A new window handle and a changed title still find the same application (case-insensitive).
    assert.deepEqual(picked(nativeSourceOptions(desktop, "window", { surface: "window", exe: "EDITOR.EXE", title: "Old title" })), ["w:40"]);
    // The application is gone: the game again.
    assert.deepEqual(picked(nativeSourceOptions(desktop, "window", { surface: "window", exe: "gone.exe", title: "x" })), ["w:30"]);
    // A remembered window never selects on the monitor surface.
    assert.deepEqual(picked(nativeSourceOptions(desktop, "monitor", { surface: "window", exe: "editor.exe", title: "Notes - Editor" })), ["m:0"]);
});

test("a remembered monitor is reselected only while its size matches", () => {
    assert.deepEqual(picked(nativeSourceOptions(desktop, "monitor", { surface: "monitor", value: "m:1", size: "2560x1440" })), ["m:1"]);
    assert.deepEqual(picked(nativeSourceOptions(desktop, "monitor", { surface: "monitor", value: "m:1", size: "1920x1080" })), ["m:0"]);
});

test("the started source is remembered per device", () => {
    const option = nativeSourceOptions(desktop, "window", null).find(o => o.value === "w:40");
    rememberNativeSource("window", option);
    assert.deepEqual(rememberedNativeSource(), { surface: "window", value: "w:40", exe: "editor.exe", title: "Docs - Editor", size: "" });
    assert.deepEqual(picked(nativeSourceOptions(desktop, "window")), ["w:40"]);
    store.set("noxa.nativeShareSource", "{broken");
    assert.equal(rememberedNativeSource(), null);
    assert.deepEqual(picked(nativeSourceOptions(desktop, "window")), ["w:30"]);
});

test("thumbnails come back as data URLs; missing sources have none", async () => {
    assert.deepEqual(await nativeThumbnails(["m:0", "w:9"]), { "m:0": "data:image/jpeg;base64,m:0" });
    assert.deepEqual(await nativeThumbnails([]), {});
});

test("only the own screen and screen audio slot keys are mirrored, in order", async () => {
    mirrorNativeKey("self|cam", { op: "activate", epoch: 1 }, "t1");
    mirrorNativeKey("self|mic", { op: "activate", epoch: 1 }, "t1");
    mirrorNativeKey("other|screenaudio", { op: "activate", epoch: 1 }, "t1");
    mirrorNativeKey("self|screen", { op: "key", epoch: 2, kid: 3, key: new Uint8Array(32).fill(1) }, "t1");
    mirrorNativeKey("self|screen", { op: "activate", epoch: 2 }, "t1");
    mirrorNativeKey("self|screenaudio", { op: "activate", epoch: 2 }, "t1");
    await new Promise(r => setTimeout(r, 30));
    assert.deepEqual(calls.map(([tab, body]) => [tab, body.slot, body.op, body.epoch]), [["t1", "screen", "key", 2], ["t1", "screen", "activate", 2], ["t1", "screenaudio", "activate", 2]]);
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
