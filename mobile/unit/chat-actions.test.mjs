import assert from "node:assert/strict";
import { test } from "node:test";

import { insertMention, isMention, keywordHit, mentionAt, mentionChoices, parseKeywords, tabComplete, visibleRoles } from "../src/core/mentions.js";
import { DelayedSends } from "../src/core/undo-send.js";
import { mergeSystemLines, presenceLines } from "../src/core/join-leave.js";

const clients = [
    { client_id: "c1", unique_id: "u-self", nickname: "me", status: "online" },
    { client_id: "c2", unique_id: "u-lou", nickname: "Lou", status: "away", roles: [{ id: 7, name: "Mods", position: 1 }], channel_id: 4 },
    { client_id: "c3", unique_id: "u-lu", nickname: "lucy", roles: [{ id: 7, name: "Mods" }, { id: 9, name: "Lurkers" }], channel_id: 0 },
    { client_id: "c4", unique_id: "u-ab", nickname: "Ablou" },
];

test("mentionAt finds the @word before the caret only at a word start", () => {
    assert.deepEqual(mentionAt("hi @lo"), { start: 3, query: "lo" });
    assert.deepEqual(mentionAt("@"), { start: 0, query: "" });
    assert.deepEqual(mentionAt("(@a-b.c"), { start: 1, query: "a-b.c" });
    assert.equal(mentionAt("mail me@host"), null);
    assert.equal(mentionAt("@lou done"), null);
    assert.deepEqual(mentionAt("@lou and @x", 4), { start: 0, query: "lou" });
});

test("mentionChoices ranks prefix matches, skips self and adds roles and mass forms", () => {
    const choices = mentionChoices("lu", { clients, selfUID: "u-self" });
    assert.deepEqual(choices.map(c => c.label), ["lucy", "@Lurkers"]);
    const lo = mentionChoices("lo", { clients, selfUID: "u-self" });
    assert.deepEqual(lo.map(c => c.insert), ["@Lou", "@Ablou"]);
    assert.equal(lo[0].status, "away");
    assert.deepEqual(mentionChoices("mo", { clients }).map(c => c.insert), ["<@&7>"]);
    assert.deepEqual(mentionChoices("e", { clients, selfUID: "u-self" }).filter(c => c.kind === "mass").map(c => c.insert), ["@everyone"]);
    assert.equal(mentionChoices("e", { clients, mass: false }).some(c => c.kind === "mass"), false);
    assert.equal(mentionChoices("me", { clients, selfUID: "u-self" }).some(c => c.label === "me"), false);
    assert.equal(mentionChoices("", { clients, limit: 3 }).length, 3);
    assert.deepEqual(visibleRoles(clients).map(r => r.id), [7, 9]);
});

test("insertMention replaces the query and adds one space", () => {
    assert.deepEqual(insertMention("hi @lo", 3, 6, "@Lou"), { value: "hi @Lou ", caret: 8 });
    assert.deepEqual(insertMention("hi @lo there", 3, 6, "@Lou"), { value: "hi @Lou there", caret: 8 });
    assert.deepEqual(insertMention("@m", 0, 2, "<@&7>"), { value: "<@&7> ", caret: 6 });
});

test("Tab completes the nearest name and cycles on repeat", () => {
    const choose = query => mentionChoices(query, { clients, selfUID: "u-self", roles: false, mass: false });
    const first = tabComplete("ping @l", 7, null, choose);
    assert.equal(first.value, "ping @Lou ");
    assert.equal(first.caret, 10);
    const second = tabComplete(first.value, first.caret, first.cycle, choose);
    assert.equal(second.value, "ping @lucy ");
    const third = tabComplete(second.value, second.caret, second.cycle, choose);
    assert.equal(third.value, "ping @Ablou ");
    assert.equal(tabComplete(third.value, third.caret, third.cycle, choose).value, "ping @Lou ");
    assert.equal(tabComplete("no mention", 10, null, choose), null);
    assert.equal(tabComplete("@zz", 3, null, choose), null);
});

test("keywords match whole words, case-insensitively", () => {
    assert.deepEqual(parseKeywords(" deploy, Release\nrelease, ,x"), ["deploy", "Release", "x"]);
    assert.equal(keywordHit("Time to DEPLOY now", ["deploy"]), "deploy");
    assert.equal(keywordHit("redeployment", ["deploy"]), "");
    assert.equal(keywordHit("deploy_bot", ["deploy"]), "");
    assert.equal(keywordHit("ship it!", ["it"]), "it");
    assert.equal(keywordHit("C++ rocks", ["c++"]), "c++");
    assert.equal(keywordHit("Ärger im Büro", ["büro"]), "büro");
    assert.equal(keywordHit(null, ["x"]), "");
    assert.equal(isMention({ text: "deploy?", own: false }, ["deploy"]), true);
    assert.equal(isMention({ text: "deploy?", own: true, mentioned: false }, ["deploy"]), false);
    assert.equal(isMention({ text: "hi", mentioned: true }, []), true);
    assert.equal(isMention({ text: "hi" }, ["deploy"]), false);
});

function fakeClock() {
    let now = 1000;
    let seq = 0;
    const timers = new Map();
    return {
        now: () => now,
        setTimer: (fn, ms) => { timers.set(++seq, { fn, at: now + ms }); return seq; },
        clearTimer: id => timers.delete(id),
        advance(ms) {
            now += ms;
            for (const [id, timer] of [...timers]) if (timer.at <= now) { timers.delete(id); timer.fn(); }
        },
        get size() { return timers.size; },
    };
}

test("undo send holds, counts down, fires once and can be cancelled", () => {
    const clock = fakeClock();
    const sends = new DelayedSends(clock);
    const fired = [];
    let changes = 0;
    sends.addEventListener("change", () => changes++);
    const entry = sends.hold("a", 5, () => fired.push("a"));
    assert.equal(entry.dueAt, 6000);
    assert.equal(sends.remaining("a"), 5);
    clock.advance(1200);
    assert.equal(sends.remaining("a"), 4);
    clock.advance(3700);
    assert.equal(sends.remaining("a"), 1);
    assert.deepEqual(fired, []);
    clock.advance(100);
    assert.deepEqual(fired, ["a"]);
    assert.equal(sends.has("a"), false);
    assert.equal(sends.remaining("a"), 0);
    assert.equal(sends.cancel("a"), false, "too late to undo");

    sends.hold("b", 3, () => fired.push("b"));
    clock.advance(2000);
    assert.equal(sends.cancel("b"), true);
    clock.advance(5000);
    assert.deepEqual(fired, ["a"]);
    assert.equal(clock.size, 0);
    assert.deepEqual(sends.pending(), []);
    assert.ok(changes >= 4);

    // Holding the same id again restarts its countdown.
    sends.hold("c", 3, () => fired.push("c1"));
    clock.advance(2000);
    sends.hold("c", 3, () => fired.push("c2"));
    clock.advance(2000);
    assert.deepEqual(fired, ["a"]);
    clock.advance(1000);
    assert.deepEqual(fired, ["a", "c2"]);
});

test("join and leave lines follow presence events", () => {
    const list = [{ client_id: "c2", unique_id: "u-lou", nickname: "Lou", channel_id: 4 }];
    assert.deepEqual(presenceLines(list, "user_joined", { client_id: "c9", unique_id: "u9", nickname: "Neu" }, 5).map(l => [l.scope, l.kind, l.name]), [[0, "joined", "Neu"]]);
    assert.deepEqual(presenceLines(list, "user_left", { client_id: "c2" }, 5).map(l => [l.scope, l.kind]), [[0, "left"], [4, "left"]]);
    assert.deepEqual(presenceLines(list, "user_moved", { client_id: "c2", channel_id: 6 }, 5).map(l => [l.scope, l.kind]), [[4, "left"], [6, "joined"]]);
    assert.deepEqual(presenceLines(list, "status_changed", { client_id: "c2" }), []);
    assert.deepEqual(presenceLines([], "user_left", { client_id: "zz" }), []);
});

test("system lines merge by time and long runs fold", () => {
    const messages = [{ id: 1, sentAt: 10 }, { id: 2, sentAt: 20 }];
    const sys = (at, n) => ({ system: true, at, key: `k${n}`, kind: "joined", name: `n${n}` });
    const merged = mergeSystemLines(messages, [sys(15, 1), sys(25, 2)]);
    assert.deepEqual(merged.map(m => m.id ?? m.key), [1, "k1", 2, "k2"]);
    const run = mergeSystemLines(messages, [sys(11, 1), sys(12, 2), sys(13, 3), sys(14, 4), sys(30, 5)]);
    assert.equal(run.length, 4);
    assert.equal(run[1].group.length, 4);
    assert.equal(run[3].key, "k5");
    assert.equal(mergeSystemLines(messages, [sys(11, 1), sys(12, 2), sys(13, 3)]).length, 5, "three in a row stay separate");
});
