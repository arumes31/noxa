import assert from "node:assert/strict";
import { test } from "node:test";

import { ActivityLog, CAP } from "../src/core/activity.js";
import { cropSquare, dataURLBase64, panBy, zoomTo } from "../src/core/avatar-crop.js";
import { AutoAway, presenceOf, statusOf } from "../src/core/presence.js";
import * as settings from "../src/core/settings.js";
import { PresenceWatch, addContact, contacts, isBlocked, removeContact, rolesOf, setBlocked, setChannelWatch } from "../src/core/social.js";
import { SoundEngine } from "../src/core/sounds.js";

/** Timers driven by a fake clock. */
function fakeClock() {
    let now = 0, next = 1;
    const timers = new Map();
    return {
        now: () => now,
        setTimer: (fn, ms) => { timers.set(next, { fn, at: now + ms }); return next++; },
        clearTimer: id => timers.delete(id),
        advance(ms) {
            const end = now + ms;
            for (;;) {
                const due = [...timers.entries()].filter(([, t]) => t.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
                if (!due) break;
                timers.delete(due[0]);
                now = due[1].at;
                due[1].fn();
            }
            now = end;
        },
        get pending() { return timers.size; },
    };
}

test("auto-away goes away after the idle limit and restores the chosen status", () => {
    const clock = fakeClock();
    const applied = [];
    let chosen = { status: "online", message: "" };
    let minutes = 5;
    const away = new AutoAway({ minutes: () => minutes, message: () => "brb", current: () => chosen, apply: (s, m) => applied.push([s, m]), ...clock });
    clock.advance(4 * 60000);
    away.activity();
    clock.advance(4 * 60000);
    assert.deepEqual(applied, []);
    clock.advance(60000);
    assert.deepEqual(applied, [["away", "brb"]]);
    clock.advance(60 * 60000);
    assert.equal(applied.length, 1, "away once, not every check");
    away.activity();
    assert.deepEqual(applied.at(-1), ["online", ""]);

    // A deliberately chosen status is never replaced by away.
    chosen = { status: "busy", message: "meeting" };
    clock.advance(10 * 60000);
    assert.equal(applied.length, 2);

    // Zero minutes turns it off.
    chosen = { status: "online", message: "" };
    minutes = 0;
    away.activity();
    assert.equal(clock.pending, 0);
    clock.advance(120 * 60000);
    assert.equal(applied.length, 2);
    away.stop();
});

test("presence picks the most available session of an account", () => {
    const session = { clients: () => [
        { client_id: "a", unique_id: "ivy", status: "away", on_mobile: true },
        { client_id: "b", unique_id: "ivy", status: "busy", status_message: "focus" },
        { client_id: "c", unique_id: "kai" },
    ] };
    const ivy = presenceOf(session, "ivy");
    assert.deepEqual([ivy.online, ivy.status, ivy.message, ivy.onMobile, ivy.client.client_id], [true, "busy", "focus", false, "b"]);
    assert.equal(presenceOf(session, "kai").status, "online");
    assert.deepEqual(presenceOf(session, "nobody"), { online: false, status: "offline", message: "", onMobile: false, clients: [] });
    assert.equal(statusOf({ status: "weird" }), "online");
});

test("avatar crop stays inside the image at any zoom and pan", () => {
    assert.deepEqual(cropSquare(400, 200), { sx: 100, sy: 0, size: 200, x: 0, y: 0, zoom: 1 });
    const zoomed = cropSquare(400, 200, { zoom: 2, x: 1000, y: -1000 });
    assert.deepEqual([zoomed.sx, zoomed.sy, zoomed.size], [300, 0, 100]);
    assert.equal(cropSquare(100, 100, { zoom: 99 }).zoom, 4);
    assert.equal(cropSquare(100, 100, { zoom: 0 }).size, 100);
    // Dragging right by half the view shows the left half of the square more.
    const moved = panBy(400, 200, { zoom: 1, x: 0, y: 0 }, 100, 0, 200);
    assert.deepEqual(moved, { zoom: 1, x: -100, y: 0 });
    // Zooming out pulls an offset back inside.
    assert.deepEqual(zoomTo(400, 400, { zoom: 4, x: 150, y: 0 }, 1), { zoom: 1, x: 0, y: 0 });
    assert.equal(dataURLBase64("data:image/png;base64,QUJD"), "QUJD");
});

test("contacts, blocks and channel watches live in settings", () => {
    settings._resetCache();
    addContact({ uid: "ivy", name: "Ivy" });
    addContact({ uid: "ivy", note: "climbing" });
    assert.deepEqual(contacts(), [{ uid: "ivy", name: "Ivy", note: "climbing" }]);
    removeContact("ivy");
    assert.deepEqual(contacts(), []);
    setBlocked("troll", true);
    assert.equal(isBlocked("troll"), true);
    setBlocked("troll", false);
    assert.equal(isBlocked("troll"), false);
    setChannelWatch(7, 3);
    assert.deepEqual(settings.get("channelWatch"), { 7: 3 });
    setChannelWatch(7, 0);
    assert.deepEqual(settings.get("channelWatch"), {});
    assert.deepEqual(rolesOf({ roles: [{ name: "Admin", color: "#f00" }, { name: "Odd", color: "url(x)" }, { color: "#fff" }] }), [{ name: "Admin", color: "#f00" }, { name: "Odd", color: "" }]);
});

test("presence watch reports buddies coming online and channels reaching a count", () => {
    let clients = [{ client_id: "1", unique_id: "kai", nickname: "Kai" }];
    let lobby = [];
    const session = { uniqueID: "me", clients: () => [...clients, ...lobby], channels: () => [{ id: 9, name: "Raid", clients: lobby }] };
    const watch = new PresenceWatch();
    const options = { buddies: ["ivy", "kai"], channelWatch: { 9: 2 } };
    assert.deepEqual(watch.update(session, options), [], "the first tree is the baseline");
    clients = [...clients, { client_id: "2", unique_id: "ivy", nickname: "Ivy" }];
    assert.deepEqual(watch.update(session, options), [{ type: "buddy_online", uid: "ivy", name: "Ivy" }]);
    lobby = [{ client_id: "3", unique_id: "a" }];
    assert.deepEqual(watch.update(session, options), []);
    lobby = [...lobby, { client_id: "4", unique_id: "b" }];
    assert.deepEqual(watch.update(session, options), [{ type: "channel_watch", channelID: 9, name: "Raid", count: 2 }]);
    lobby = [...lobby, { client_id: "5", unique_id: "c" }];
    assert.deepEqual(watch.update(session, options), [], "only when the count is crossed");
});

function memoryStorage(initial) {
    const storage = { value: initial, saves: 0, load: async () => storage.value, save: async entries => { storage.value = entries; storage.saves++; } };
    return storage;
}

test("the activity log keeps the newest entries, merges a busy conversation and marks reads", async () => {
    const storage = memoryStorage([{ id: "old", kind: "poke", title: "Old", at: 1, read: true }]);
    const log = new ActivityLog(storage);
    await log.ready;
    log.add({ kind: "dm", title: "Ivy", body: "one", tag: "dm:ivy", at: 10 });
    log.add({ kind: "dm", title: "Ivy", body: "two", tag: "dm:ivy", at: 11 });
    log.add({ kind: "mention", title: "Kai", tag: "channel:1", at: 12 });
    assert.equal(log.add({ kind: "bogus", title: "x" }), null);
    assert.deepEqual(log.list().map(e => e.body || e.title), ["Kai", "two", "Old"]);
    assert.deepEqual(log.list("dm").map(e => e.body), ["two"]);
    assert.equal(log.unread, 2);
    log.markTagRead("dm:ivy");
    assert.equal(log.unread, 1);
    log.markAllRead();
    assert.equal(log.unread, 0);
    for (let i = 0; i < CAP + 20; i++) log.add({ kind: "poke", title: `p${i}`, at: 100 + i });
    assert.equal(log.entries.length, CAP);
    assert.equal(log.entries[0].title, `p${CAP + 19}`);
    await log.flush();
    assert.equal(storage.value.length, CAP);
});

test("the sound engine decodes once, respects mute and silence, and speaks the phone's language", async () => {
    const started = [], decoded = [], loaded = [];
    const context = {
        state: "suspended", destination: {},
        resume: async () => { context.state = "running"; },
        decodeAudioData: (bytes, ok) => { decoded.push(bytes); ok({ duration: 1 }); },
        createBufferSource: () => ({ connect: node => node, start: () => started.push("start"), stop() {} }),
        createGain: () => ({ gain: { value: 0 }, connect: node => node }),
    };
    let contexts = 0;
    const state = { enabled: true, volume: 0.5, speech: false, silenced: false, language: "de" };
    const engine = new SoundEngine({ createContext: () => { contexts++; return context; }, load: async url => { loaded.push(url); return url; }, state: () => state });
    assert.ok(await engine.play("dm"));
    assert.ok(await engine.play("dm"));
    assert.deepEqual([contexts, started.length, decoded.length], [1, 2, 1]);
    assert.equal(context.state, "running");
    state.silenced = true;
    assert.equal(await engine.play("mention"), null);
    assert.ok(await engine.play("mention", { force: true }), "previews ignore silence");
    state.silenced = false;
    assert.equal(await engine.speak("poke"), null, "speech is off");
    state.speech = true;
    assert.ok(await engine.speak("poke"));
    assert.equal(loaded.at(-1), "/app/speech/de/poke.wav");
    state.enabled = false;
    assert.equal(await engine.play("poke"), null);
    assert.equal(await engine.play("nonexistent"), null);
});
