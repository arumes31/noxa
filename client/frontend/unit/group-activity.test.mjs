import { describe, it, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { setLanguage } from "../src/i18n.js";
import { applySeen, createTypingStore, seenLabel, seenMarkers, typingLabel } from "../src/group-activity.js";

beforeEach(() => setLanguage("en"));

describe("group typing", () => {
    it("expires members per group and reports when nothing is pending", () => {
        let now = 1000;
        const store = createTypingStore({ ttl: 4500, now: () => now });
        store.note("g1", "mira", "Mira");
        store.note("g1", "oren", "Oren");
        store.note("g2", "mira", "Mira");
        store.note("", "x", "X");
        assert.deepEqual(store.names("g1"), ["Mira", "Oren"]);
        assert.deepEqual(store.typers("g2"), [{ uid: "mira", nick: "Mira" }]);
        assert.equal(store.clear("g1", "oren"), true);
        assert.equal(store.clear("g1", "oren"), false);
        now += 3000;
        store.note("g1", "mira", "Mira"); // a fresh ping extends the entry
        now += 2000;
        assert.deepEqual(store.names("g1"), ["Mira"]);
        assert.deepEqual(store.names("g2"), []);
        now += 5000;
        assert.equal(store.live(), false);
        store.note("g3", "a", "");
        assert.deepEqual(store.names("g3"), ["a"]);
        store.drop("g3");
        assert.equal(store.live(), false);
        store.note("g4", "a", "A");
        store.reset();
        assert.equal(store.live(), false);
    });

    it("words the indicator like direct messages", () => {
        assert.equal(typingLabel([]), "");
        assert.equal(typingLabel(["Mira"]), "Mira is typing…");
        assert.equal(typingLabel(["Mira", "Oren"]), "Mira and Oren are typing…");
        assert.equal(typingLabel(["a", "b", "c"]), "3 people are typing…");
        setLanguage("de");
        assert.equal(typingLabel(["Mira"]), "Mira schreibt…");
    });
});

describe("group read receipts", () => {
    const messages = [{ id: 10, from: "me" }, { id: 12, from: "mira" }, { id: 15, from: "me" }];
    it("places each member on the newest rendered message they have read", () => {
        const members = [
            { unique_id: "me", seen_message_id: 15 },
            { unique_id: "mira", seen_message_id: 15 },
            { unique_id: "oren", seen_message_id: 13 },
            { unique_id: "ivy", seen_message_id: 99 },
            { unique_id: "old", seen_message_id: 0 },
            { unique_id: "new" },
            { unique_id: "invited", pending: true, seen_message_id: 15 },
            { unique_id: "early", seen_message_id: 5 },
        ];
        const markers = seenMarkers([...messages].reverse(), members, "me");
        assert.deepEqual([...markers.entries()], [[15, ["mira", "ivy"]], [12, ["oren"]]]);
    });

    it("never marks a member under their own message", () => {
        const markers = seenMarkers(messages, [{ unique_id: "mira", seen_message_id: 12 }], "me");
        assert.equal(markers.size, 0);
        assert.equal(seenMarkers([], [{ unique_id: "mira", seen_message_id: 12 }], "me").size, 0);
        assert.equal(seenMarkers(messages, null, "me").size, 0);
    });

    it("labels markers and moves positions only forward", () => {
        assert.equal(seenLabel([]), "");
        assert.equal(seenLabel(["Mira", "Oren"]), "Seen by Mira, Oren");
        assert.equal(seenLabel(["a", "b", "c", "d", "e"]), "Seen by a, b and 3 more");
        setLanguage("de");
        assert.equal(seenLabel(["Mira"]), "Gesehen von Mira");
        const members = [{ unique_id: "mira", seen_message_id: 12 }, { unique_id: "inv", pending: true }];
        assert.equal(applySeen(members, "mira", 10), false);
        assert.equal(applySeen(members, "mira", 15), true);
        assert.equal(members[0].seen_message_id, 15);
        assert.equal(applySeen(members, "inv", 20), false);
        assert.equal(applySeen(members, "nobody", 20), false);
        assert.equal(applySeen(null, "mira", 20), false);
    });
});
