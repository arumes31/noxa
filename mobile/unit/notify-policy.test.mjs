import assert from "node:assert/strict";
import { test } from "node:test";

import { DEFAULTS } from "../src/core/settings.js";
import { EVENTS, channelEvent, decide, matchesKeyword, matrixRow, pushFilters, silencedAt, snoozeEnd } from "../src/core/notify-policy.js";

const noon = new Date(2026, 4, 12, 12, 0);
const settings = (patch = {}) => ({ ...DEFAULTS, ...patch });
const pick = d => [d.sound, d.toast, d.push, d.count];

test("defaults: direct events alert and push, channel chatter only counts", () => {
    const s = settings();
    assert.deepEqual(pick(decide({ event: "dm", tag: "dm:ivy", from: "ivy", settings: s, now: noon })), [true, true, true, true]);
    assert.deepEqual(pick(decide({ event: "mention", tag: "channel:4", channelID: 4, settings: s, now: noon })), [true, true, true, true]);
    assert.deepEqual(pick(decide({ event: "channel", tag: "channel:4", channelID: 4, settings: s, now: noon })), [false, false, false, true]);
    // Events without a conversation (pokes, watches) never count as unread.
    assert.deepEqual(pick(decide({ event: "buddy_online", settings: s, now: noon })), [true, true, false, false]);
    for (const event of EVENTS) assert.equal(typeof matrixRow(s, event).sound, "boolean");
});

test("the matrix switches single outputs; push stays off where the server cannot push", () => {
    const s = settings({ matrix: { dm: { sound: false }, whisper: { push: true }, channel: { sound: true, toast: true } } });
    assert.deepEqual(pick(decide({ event: "dm", tag: "dm:ivy", settings: s, now: noon })), [false, true, true, true]);
    assert.equal(matrixRow(s, "whisper").push, false);
    assert.deepEqual(pick(decide({ event: "channel", tag: "channel:4", channelID: 4, settings: s, now: noon })), [true, true, false, true]);
});

test("channel levels: mentions only, none counts silently, mute ignores", () => {
    const levels = { 4: "mentions", 5: "none", 6: "mute" };
    const s = settings({ channelNotify: levels, matrix: { channel: { sound: true, toast: true } } });
    assert.deepEqual(pick(decide({ event: "channel", tag: "channel:4", channelID: 4, settings: s, now: noon })), [false, false, false, true]);
    assert.deepEqual(pick(decide({ event: "keyword", tag: "channel:4", channelID: 4, settings: s, now: noon })), [true, true, false, true]);
    assert.deepEqual(pick(decide({ event: "mention", tag: "channel:5", channelID: 5, settings: s, now: noon })), [false, false, false, true]);
    assert.deepEqual(pick(decide({ event: "mention", tag: "channel:6", channelID: 6, settings: s, now: noon })), [false, false, false, false]);
});

test("chat level limits which conversations alert", () => {
    const mentions = settings({ chatLevel: "mentions" });
    assert.equal(decide({ event: "group", tag: "conversation:g", settings: mentions, now: noon }).toast, false);
    assert.equal(decide({ event: "mention", tag: "channel:1", channelID: 1, settings: mentions, now: noon }).toast, true);
    const dms = settings({ chatLevel: "dm" });
    assert.deepEqual(pick(decide({ event: "mention", tag: "channel:1", channelID: 1, settings: dms, now: noon })), [false, false, false, true]);
    assert.equal(decide({ event: "dm", tag: "dm:ivy", settings: dms, now: noon }).sound, true);
    assert.equal(decide({ event: "poke", settings: dms, now: noon }).toast, true);
});

test("DND, snooze and quiet hours silence sound and banner but keep counting", () => {
    for (const patch of [{ dnd: true }, { snoozeUntil: noon.getTime() + 60000 }, { quietFrom: "11:00", quietTo: "13:00" }, { quietFrom: "22:00", quietTo: "12:30" }]) {
        const d = decide({ event: "dm", tag: "dm:ivy", settings: settings(patch), now: noon });
        assert.deepEqual([d.sound, d.toast, d.count], [false, false, true], JSON.stringify(patch));
    }
    assert.equal(silencedAt(settings({ snoozeUntil: noon.getTime() - 1 }), noon), false);
    assert.equal(silencedAt(settings({ quietFrom: "13:00", quietTo: "07:00" }), noon), false);
    assert.equal(silencedAt(settings({ quietFrom: "12:00", quietTo: "12:00" }), noon), false);
});

test("muted conversations count without alerts; blocked senders vanish", () => {
    const s = settings({ mutedConversations: ["dm:ivy"], blockedUsers: ["troll"] });
    assert.deepEqual(pick(decide({ event: "dm", tag: "dm:ivy", from: "ivy", settings: s, now: noon })), [false, false, false, true]);
    assert.deepEqual(pick(decide({ event: "dm", tag: "dm:troll", from: "troll", settings: s, now: noon })), [false, false, false, false]);
    assert.deepEqual(pick(decide({ event: "mention", tag: "channel:1", channelID: 1, from: "troll", settings: s, now: noon })), [false, false, false, false]);
});

test("an open conversation neither alerts nor counts", () => {
    assert.deepEqual(pick(decide({ event: "dm", tag: "dm:ivy", viewing: true, settings: settings(), now: noon })), [false, false, true, false]);
});

test("keywords and mentions pick the channel event", () => {
    assert.equal(matchesKeyword("Deploy is DONE", ["done"]), true);
    assert.equal(matchesKeyword("nothing", ["done", " "]), false);
    assert.equal(matchesKeyword(null, ["x"]), false);
    assert.equal(channelEvent({ mentioned: true, text: "done" , keywords: ["done"] }), "mention");
    assert.equal(channelEvent({ text: "we are done", keywords: ["done"] }), "keyword");
    assert.equal(channelEvent({ text: "hello", keywords: ["done"] }), "channel");
});

test("push filters mirror the settings for the server", () => {
    const s = settings({
        channelNotify: { 4: "mentions", 5: "none", 6: "mute" }, mutedConversations: ["conversation:g"], blockedUsers: ["troll"],
        matrix: { poke: { push: false }, channel: { push: true } }, chatLevel: "dm", quietFrom: "22:00", quietTo: "07:00",
        snoozeUntil: noon.getTime() + 1800000,
    });
    const f = pushFilters(s, noon);
    assert.deepEqual(f.muted_channels.sort(), [5, 6]);
    assert.deepEqual(f.muted_tags.sort(), ["conversation:g", "dm:troll", "poke:troll"]);
    assert.deepEqual(f.muted_kinds, ["poke"]);
    assert.equal(f.chat_level, "dm");
    assert.equal(f.dnd_until, Math.ceil((noon.getTime() + 1800000) / 1000));
    assert.equal(f.quiet_from, "22:00");
    assert.equal(f.utc_offset_minutes, -noon.getTimezoneOffset());
    assert.ok(pushFilters(settings({ dnd: true }), noon).dnd_until > noon.getTime() / 1000 + 365 * 86400);
    const plain = pushFilters(settings(), noon);
    assert.deepEqual([plain.dnd_until, plain.quiet_from, plain.muted_kinds.length], [0, undefined, 0]);
});

test("snooze ends after the chosen minutes or tomorrow morning", () => {
    assert.equal(snoozeEnd(30, noon), noon.getTime() + 30 * 60000);
    const tomorrow = new Date(snoozeEnd("tomorrow", noon));
    assert.deepEqual([tomorrow.getDate(), tomorrow.getHours(), tomorrow.getMinutes()], [13, 8, 0]);
});
