import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { readFileSync } from "node:fs";
import {
    HUD_HIDE_MS, PEEK_MAX, PEEK_MS, QUICK_IDLE_MS,
    createAutoHide, createChatPeek, createQuickReply, hudLayout, hudSpeakers, pingQuality, telemetryParts,
} from "../src/fullscreen-hud-state.js";
import { placementRect, presetZones, zonesHit } from "../src/overlay-zones.js";
import { fullscreenEnglish, fullscreenGerman } from "../src/fullscreen-messages.js";

// A manual clock: schedule() queues, advance() fires what is due in order.
function fakeClock() {
    let now = 0, seq = 0;
    const timers = new Map();
    return {
        now: () => now,
        schedule(callback, ms) { const id = ++seq; timers.set(id, { at: now + ms, callback }); return id; },
        cancel(id) { timers.delete(id); },
        advance(ms) {
            const end = now + ms;
            for (;;) {
                const next = [...timers].filter(([, timer]) => timer.at <= end).sort((a, b) => a[1].at - b[1].at || a[0] - b[0])[0];
                if (!next) break;
                timers.delete(next[0]);
                now = next[1].at;
                next[1].callback();
            }
            now = end;
        },
        get pending() { return timers.size; },
    };
}

describe("HUD auto-hide timer", () => {
    it("shows on activity and hides 2.5 s after the last movement", () => {
        const clock = fakeClock(), changes = [];
        const hide = createAutoHide({ clock, onChange: visible => changes.push(visible) });
        assert.equal(HUD_HIDE_MS, 2500);
        assert.equal(hide.visible, false);
        hide.activity();
        assert.equal(hide.visible, true);
        clock.advance(2000);
        hide.activity();                 // movement restarts the countdown
        clock.advance(2000);
        assert.equal(hide.visible, true);
        clock.advance(500);
        assert.equal(hide.visible, false);
        assert.deepEqual(changes, [true, false]);
    });

    it("stays visible while held (hover, focus) and counts down again on release", () => {
        const clock = fakeClock();
        const hide = createAutoHide({ clock });
        hide.activity();
        hide.hold("dock");
        hide.hold("focus");
        clock.advance(10_000);
        assert.equal(hide.visible, true);
        hide.release("dock");
        clock.advance(10_000);
        assert.equal(hide.visible, true, "focus still holds it");
        hide.release("focus");
        clock.advance(2499);
        assert.equal(hide.visible, true);
        clock.advance(1);
        assert.equal(hide.visible, false);
    });

    it("a hold shows a hidden HUD; releasing an unknown hold changes nothing", () => {
        const clock = fakeClock();
        const hide = createAutoHide({ clock });
        hide.hold("focus");
        assert.equal(hide.visible, true);
        assert.equal(hide.held, true);
        hide.release("other");
        clock.advance(5000);
        assert.equal(hide.visible, true);
        hide.hide();
        assert.equal(hide.visible, false);
        assert.equal(hide.held, false);
        hide.activity();
        hide.reset();
        assert.equal(hide.visible, false);
        assert.equal(clock.pending, 0, "reset drops the countdown");
    });
});

describe("chat peek queue", () => {
    it("shows each message for about six seconds", () => {
        const clock = fakeClock(), seen = [];
        const peek = createChatPeek({ clock, onChange: items => seen.push(items.map(item => item.text)) });
        assert.equal(PEEK_MS, 6000);
        peek.push({ name: "Oren", text: "nice pull" });
        clock.advance(3000);
        peek.push({ name: "Vex", text: "gg" });
        clock.advance(3000);
        assert.deepEqual(peek.items.map(item => item.text), ["gg"]);
        clock.advance(3000);
        assert.deepEqual(peek.items, []);
        assert.deepEqual(seen, [["nice pull"], ["nice pull", "gg"], ["gg"], []]);
    });

    it("keeps at most three, dropping the oldest, and ignores duplicate keys", () => {
        const clock = fakeClock();
        const peek = createChatPeek({ clock });
        assert.equal(PEEK_MAX, 3);
        for (const text of ["a", "b", "c", "d"]) peek.push({ key: `ch:${text}`, text });
        assert.deepEqual(peek.items.map(item => item.text), ["b", "c", "d"]);
        assert.equal(peek.push({ key: "ch:c", text: "c again" }), null);
        assert.equal(peek.items.length, 3);
        assert.equal(clock.pending, 3, "the dropped message's timer is cancelled");
        peek.dismiss(peek.items[0].id);
        assert.deepEqual(peek.items.map(item => item.text), ["c", "d"]);
        peek.clear();
        assert.deepEqual(peek.items, []);
        assert.equal(clock.pending, 0);
    });
});

describe("whisper / quick-reply bubble", () => {
    it("opens for an incoming private message with the sender as reply target", () => {
        const clock = fakeClock();
        const quick = createQuickReply({ clock });
        assert.equal(quick.receive({ uid: "u-mira", name: "Mira", text: "swap at 3 stacks" }), "open");
        assert.deepEqual(quick.state.target, { kind: "dm", uid: "u-mira", name: "Mira" });
        quick.receive({ uid: "u-mira", name: "Mira", text: "don't stand in the fire" });
        assert.deepEqual(quick.state.messages.map(m => m.text), ["swap at 3 stacks", "don't stand in the fire"]);
        quick.sent("on it");
        assert.deepEqual(quick.state.messages.map(m => [m.text, m.self]), [["swap at 3 stacks", false], ["don't stand in the fire", false], ["on it", true]]);
        assert.equal(quick.receive({ uid: "" , text: "x" }), "ignored");
    });

    it("queues other senders as unread while you are replying, Alt+R opens the newest", () => {
        const clock = fakeClock();
        const quick = createQuickReply({ clock });
        quick.receive({ uid: "u-mira", name: "Mira", text: "hi" });
        quick.engage(true);
        clock.advance(10);
        assert.equal(quick.receive({ uid: "u-jinx", name: "Jinx", text: "yo" }), "queued");
        clock.advance(10);
        assert.equal(quick.receive({ uid: "u-oren", name: "Oren", text: "ready?" }), "queued");
        assert.equal(quick.state.target.uid, "u-mira", "the reply in progress keeps its target");
        assert.deepEqual(quick.state.unread.map(u => [u.name, u.count]), [["Jinx", 1], ["Oren", 1]]);
        assert.deepEqual(quick.openLatest(), { kind: "dm", uid: "u-oren", name: "Oren" });
        assert.deepEqual(quick.state.unread.map(u => u.name), ["Jinx"]);
        assert.equal(quick.engaged, false);
        quick.openLatest();
        assert.equal(quick.state.target.uid, "u-jinx");
        assert.deepEqual(quick.state.unread, []);
        // Nothing waiting: Alt+R keeps the open conversation.
        assert.equal(quick.openLatest().uid, "u-jinx");
    });

    it("a newer message replaces an untouched bubble; closed bubbles reopen the latest thread", () => {
        const clock = fakeClock();
        const quick = createQuickReply({ clock });
        quick.receive({ uid: "u-mira", name: "Mira", text: "one" });
        clock.advance(10);
        quick.receive({ uid: "u-jinx", name: "Jinx", text: "two" });
        assert.equal(quick.state.target.uid, "u-jinx");
        assert.deepEqual(quick.state.unread.map(u => [u.name, u.count]), [["Mira", 1]], "the replaced message is not lost");
        quick.close();
        assert.equal(quick.state.open, false);
        assert.equal(quick.openLatest().uid, "u-mira");
        assert.deepEqual(quick.state.messages.map(m => m.text), ["one"]);
        quick.close();
        assert.equal(quick.openLatest().uid, "u-jinx", "nothing unread: the newest thread");
    });

    it("closes after a quiet period unless the reply field is engaged", () => {
        const clock = fakeClock();
        const quick = createQuickReply({ clock });
        quick.receive({ uid: "u-mira", name: "Mira", text: "hi" });
        clock.advance(QUICK_IDLE_MS - 1);
        assert.equal(quick.state.open, true);
        clock.advance(1);
        assert.equal(quick.state.open, false);
        quick.receive({ uid: "u-mira", name: "Mira", text: "still there?" });
        quick.engage(true);
        clock.advance(QUICK_IDLE_MS * 3);
        assert.equal(quick.state.open, true);
        quick.engage(false);
        clock.advance(QUICK_IDLE_MS);
        assert.equal(quick.state.open, false);
    });

    it("doubles as the channel chat drawer", () => {
        const clock = fakeClock(), changes = [];
        const quick = createQuickReply({ clock, onChange: state => changes.push(state.target?.kind || "") });
        quick.openChannel({ id: 2, name: "Raid Night", recent: [{ name: "Vex", text: "a" }, { name: "Vex", text: "b" }, { name: "Oren", text: "c" }, { name: "Oren", text: "d" }, { name: "Jinx", text: "e" }] });
        assert.deepEqual(quick.state.target, { kind: "channel", id: 2, name: "Raid Night" });
        assert.deepEqual(quick.state.messages.map(m => m.text), ["b", "c", "d", "e"]);
        quick.channelMessage({ name: "Mira", text: "f" });
        assert.deepEqual(quick.state.messages.map(m => m.text), ["c", "d", "e", "f"]);
        quick.sent("g");
        assert.equal(quick.state.messages.at(-1).self, true);
        quick.reset();
        assert.equal(quick.state.open, false);
        assert.deepEqual(changes.slice(0, 2), ["channel", "channel"]);
    });
});

describe("speaker list", () => {
    const members = [
        { id: "self", name: "Daniel", speaking: true },
        { id: "torvald", name: "Torvald" },
        { id: "mira", name: "Mira", speaking: true },
        { id: "sable", name: "Sable", muted: true },
        { id: "jinx", name: "Jinx", whispering: true },
    ];

    it("active HUD lists everyone but you, talkers first", () => {
        const { shown, more } = hudSpeakers(members, { self: "self", active: true });
        assert.deepEqual(shown.map(m => m.name), ["Jinx", "Mira", "Sable", "Torvald"]);
        assert.equal(more, 0);
    });

    it("idle HUD shows only who is talking or whispering, capped with a remainder", () => {
        assert.deepEqual(hudSpeakers(members, { self: "self" }).shown.map(m => m.name), ["Jinx", "Mira"]);
        const { shown, more } = hudSpeakers(members, { self: "self", active: true, limit: 2 });
        assert.equal(shown.length, 2);
        assert.equal(more, 2);
    });
});

describe("zone-aware placement", () => {
    const box = { w: 20, h: 40 };

    it("keeps the overlay's own anchor when no zone is hit", () => {
        const layout = hudLayout({ settings: { gaming_overlay_game_ui: "mmo", gaming_overlay_position: "center-left" }, speakers: box });
        assert.equal(layout.anchor, "center-left");
        assert.equal(layout.moved, false);
        assert.equal(layout.side, "left");
        assert.equal(zonesHit(layout.speakers, presetZones("mmo")).length, 0);
        assert.equal(layout.bubble.anchor, "center-right", "the whisper bubble goes to the opposite side");
        assert.equal(layout.peekAbove, false);
        assert.equal(layout.topInset, 0);
    });

    it("moves speakers off a no-go zone to the nearest free anchor", () => {
        const settings = { gaming_overlay_game_ui: "shooter", gaming_overlay_position: "top-left" };
        const layout = hudLayout({ settings, speakers: box });
        assert.equal(layout.moved, true);
        assert.notEqual(layout.anchor, "top-left");
        assert.equal(zonesHit(layout.speakers, presetZones("shooter")).length, 0);
    });

    it("honours a free custom placement and the 'avoid zones' switch", () => {
        const custom = hudLayout({ settings: { gaming_overlay_position: "custom", gaming_overlay_x: 100, gaming_overlay_y: 50 }, speakers: box });
        assert.equal(custom.anchor, "center-right");
        assert.equal(custom.side, "right");
        assert.equal(custom.bubble.anchor, "center-left");
        const off = hudLayout({ settings: { gaming_overlay_game_ui: "shooter", gaming_overlay_position: "top-left", gaming_overlay_avoid_zones: false }, speakers: box });
        assert.equal(off.anchor, "top-left");
        assert.equal(off.moved, false);
    });

    it("bottom anchors stack the chat peek above the speakers", () => {
        const layout = hudLayout({ settings: { gaming_overlay_position: "bottom-right" }, speakers: box });
        assert.equal(layout.vertical, "bottom");
        assert.equal(layout.peekAbove, true);
        assert.equal(layout.side, "right");
    });

    it("keeps the bubble off zones and off the speakers", () => {
        const bubble = { w: 26, h: 30 };
        for (const preset of ["none", "mmo", "shooter", "moba"]) {
            for (const position of ["center-left", "top-right", "bottom-left", "bottom-right"]) {
                const layout = hudLayout({ settings: { gaming_overlay_game_ui: preset, gaming_overlay_position: position }, speakers: box, bubble });
                const rect = layout.bubble;
                assert.equal(zonesHit(rect, presetZones(preset)).length, 0, `${preset}/${position}: ${layout.bubble.anchor}`);
                const s = layout.speakers;
                const overlap = rect.x < s.x + s.w && s.x < rect.x + rect.w && rect.y < s.y + s.h && s.y < rect.y + rect.h;
                assert.equal(overlap, false, `${preset}/${position}`);
            }
        }
    });

    it("drops the top pill below a top-centre zone (MOBA scoreboard)", () => {
        assert.equal(hudLayout({ settings: { gaming_overlay_game_ui: "moba" } }).topInset, 8);
        // MMO unit frames end at 32 %: a narrow pill fits beside them, a wide one drops below.
        assert.equal(hudLayout({ settings: { gaming_overlay_game_ui: "mmo" }, pill: { w: 34, h: 6 } }).topInset, 0);
        assert.equal(hudLayout({ settings: { gaming_overlay_game_ui: "mmo" }, pill: { w: 50, h: 6 } }).topInset, 12);
        assert.equal(hudLayout({ settings: { gaming_overlay_game_ui: "moba", gaming_overlay_avoid_zones: false } }).topInset, 0);
    });

    it("uses the same rectangles as the overlay settings preview", () => {
        const layout = hudLayout({ settings: { gaming_overlay_position: "top-right" }, speakers: box });
        assert.deepEqual(layout.speakers, placementRect({ position: "top-right", x: 0, y: 0 }, box));
    });
});

describe("telemetry pill", () => {
    const format = {
        video: height => `${height}p`, videoFps: (height, fps) => `${height}p · ${fps} FPS`,
        rate: bits => `${(bits / 1e6).toFixed(1)} Mbps`, ping: ms => `${ms} ms`,
    };

    it("formats resolution, FPS, bitrate and ping from real samples", () => {
        assert.deepEqual(telemetryParts({ height: 1080, fps: 59.7, bitrate: 6_200_000 }, 24, format),
            { video: "1080p · 60 FPS", bitrate: "6.2 Mbps", ping: "24 ms", quality: "good" });
    });

    it("omits what was not measured", () => {
        assert.deepEqual(telemetryParts(null, NaN, format), {});
        assert.deepEqual(telemetryParts({ height: 720, fps: 0, bitrate: null }, -1, format), { video: "720p" });
    });

    it("grades ping like the connection pill", () => {
        assert.equal(pingQuality(99), "good");
        assert.equal(pingQuality(100), "fair");
        assert.equal(pingQuality(249), "fair");
        assert.equal(pingQuality(250), "poor");
        assert.equal(pingQuality(NaN), "");
    });
});

describe("fullscreen strings and stylesheet", () => {
    it("has every string in English and German", () => {
        assert.deepEqual(Object.keys(fullscreenGerman).sort(), Object.keys(fullscreenEnglish).sort());
        for (const [key, value] of Object.entries(fullscreenGerman)) assert.ok(value.trim(), key);
    });

    it("keeps backgrounds static and honours both reduced-motion switches", () => {
        const css = readFileSync(new URL("../src/arctic-fullscreen.css", import.meta.url), "utf8");
        assert.doesNotMatch(css, /infinite/, "no continuous animation");
        assert.match(css, /@media \(prefers-reduced-motion: reduce\)/);
        assert.match(css, /:root\[data-reduce-motion="1"\]/);
        assert.doesNotMatch(css, /\.speaking-avatar/, "the speaking avatar is reused, not restyled");
    });
});
