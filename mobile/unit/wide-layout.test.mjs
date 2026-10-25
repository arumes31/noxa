import test from "node:test";
import assert from "node:assert/strict";
import * as tabs from "../src/core/chat-tabs.js";
import { fold, rank, score } from "../src/core/switcher.js";

test("tab keys and hrefs round-trip conversation routes", () => {
    assert.equal(tabs.tabKey("chat", "4"), "chat/4");
    assert.equal(tabs.tabKey("dm", "uid/with slash"), "dm/uid/with slash");
    assert.equal(tabs.tabKey("home", ""), null);
    assert.equal(tabs.tabKey("dm", ""), null);
    assert.equal(tabs.tabHref("dm/uid/with slash"), "#/dm/uid%2Fwith%20slash");
    assert.deepEqual(tabs.parseKey("group/g-1"), { screen: "group", arg: "g-1" });
});

test("stored tab state is repaired", () => {
    assert.deepEqual(tabs.normalize(null), { order: [], pinned: [] });
    assert.deepEqual(tabs.normalize({ order: ["chat/1", "chat/1", "voice/2", 7, "dm/", "dm/a"], pinned: ["dm/a", "chat/9"] }),
        { order: ["chat/1", "dm/a"], pinned: ["dm/a"] });
});

test("opening appends once and the limit drops the oldest unpinned tab", () => {
    let state = { order: [], pinned: [] };
    state = tabs.open(state, "chat/1");
    state = tabs.open(state, "dm/a");
    assert.equal(tabs.open(state, "chat/1"), state);
    assert.deepEqual(state.order, ["chat/1", "dm/a"]);
    state = tabs.togglePin(state, "chat/1");
    for (let i = 0; i < tabs.MAX_TABS; i++) state = tabs.open(state, `chat/${100 + i}`);
    assert.equal(state.order.length, tabs.MAX_TABS);
    assert.ok(state.order.includes("chat/1"), "pinned tabs survive the limit");
    assert.ok(!state.order.includes("dm/a"));
    assert.equal(state.order.at(-1), `chat/${100 + tabs.MAX_TABS - 1}`);
});

test("pinned tabs come first; closing the active tab picks a neighbour", () => {
    let state = { order: ["chat/1", "dm/a", "group/g", "chat/2"], pinned: [] };
    state = tabs.togglePin(state, "group/g");
    assert.deepEqual(tabs.ordered(state), ["group/g", "chat/1", "dm/a", "chat/2"]);
    let result = tabs.close(state, "dm/a", "dm/a");
    assert.equal(result.next, "chat/2");
    assert.deepEqual(result.state.order, ["chat/1", "group/g", "chat/2"]);
    result = tabs.close(result.state, "chat/2", "chat/2");
    assert.equal(result.next, "chat/1", "the last tab falls back to its left neighbour");
    result = tabs.close(result.state, "group/g", "chat/1");
    assert.equal(result.next, "chat/1", "closing another tab keeps the active one");
    assert.deepEqual(result.state.pinned, []);
    result = tabs.close(result.state, "chat/1", "chat/1");
    assert.equal(result.next, null);
    assert.equal(tabs.close(result.state, "chat/404", "home").next, "home");
});

test("stepping wraps and moving stays inside the pinned group", () => {
    let state = { order: ["chat/1", "dm/a", "chat/2"], pinned: ["chat/2"] };
    assert.equal(tabs.step(state, "chat/2", 1), "chat/1");
    assert.equal(tabs.step(state, "dm/a", 1), "chat/2", "wraps from the end");
    assert.equal(tabs.step(state, "chat/2", -1), "dm/a");
    assert.equal(tabs.step(state, null, 1), "chat/2");
    assert.equal(tabs.step(state, null, -1), "dm/a");
    assert.equal(tabs.step({ order: [], pinned: [] }, null, 1), null);
    state = tabs.move(state, "dm/a", -1);
    assert.deepEqual(tabs.ordered(state), ["chat/2", "dm/a", "chat/1"]);
    assert.equal(tabs.move(state, "dm/a", -1), state, "an unpinned tab cannot jump over pinned ones");
    state = tabs.togglePin(state, "chat/2");
    assert.deepEqual(state.pinned, []);
});

test("switcher scores exact, prefix, word start, substring, subsequence", () => {
    assert.equal(fold("Über Kaï"), "uber kai");
    const exact = score("public1", "Public1");
    const prefix = score("pub", "Public1");
    const word = score("lob", "The Lobby");
    const inner = score("obb", "The Lobby");
    const fuzzy = score("pb1", "Public1");
    assert.ok(exact > prefix && prefix > word && word > inner && inner > fuzzy && fuzzy >= 0);
    assert.equal(score("xyz", "Public1"), -1);
    assert.equal(score("", "anything"), 0);
});

test("switcher ranking: text channels before voice, typed pages only on demand", () => {
    const items = [
        { kind: "page", typed: true, label: "Settings", keywords: ["preferences"] },
        { kind: "voice", label: "Public1" },
        { kind: "person", label: "Publius" },
        { kind: "text", label: "Public1" },
        { kind: "text", label: "Public10" },
        { kind: "dm", label: "Ivy", boost: 5 },
    ];
    assert.deepEqual(rank("", items).map(i => `${i.kind}:${i.label}`), ["dm:Ivy", "text:Public1", "text:Public10", "person:Publius", "voice:Public1"]);
    assert.deepEqual(rank("public1", items).map(i => `${i.kind}:${i.label}`), ["text:Public1", "voice:Public1", "text:Public10"]);
    assert.deepEqual(rank("pub", items, 2).map(i => `${i.kind}:${i.label}`), ["text:Public1", "person:Publius"], "equal prefixes order by kind");
    assert.deepEqual(rank("sett", items).map(i => i.label), ["Settings"]);
    assert.deepEqual(rank("prefer", items).map(i => i.label), ["Settings"], "keywords match too");
    assert.deepEqual(rank("zzz", items), []);
});
