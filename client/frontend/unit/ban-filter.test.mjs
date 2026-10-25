import { test } from "node:test";
import assert from "node:assert/strict";
import { banSearchText, filterBans, normalizeBanQuery } from "../src/ban-filter.js";

const bans = [
    { id: 1, type: 1, value: "AbCdEf0123456789XYZ", reason: "Spam links", banned_by: "MODERATOR-7781", created_at: 1760000000, expires_at: 1767225600 },
    { id: 2, type: 2, value: "troll_42", reason: "harassment", banned_by: "OWNER-1", created_at: 1760000000, expires_at: 0 },
    { id: 3, type: 0, value: "10.0.0.5", reason: "", banned_by: "", created_at: 1760000000, expires_at: 1762000000 },
];
const fmt = unix => `D${unix}`;

test("(350) an empty filter returns the full list and terms are normalised", () => {
    assert.equal(filterBans(bans, ""), bans);
    assert.equal(filterBans(bans, "   "), bans);
    assert.deepEqual(normalizeBanQuery("  Spam   Links "), ["spam", "links"]);
    assert.deepEqual(filterBans(null, "x"), []);
});

test("(350) the filter matches account name or id, moderator, reason and expiry date", () => {
    assert.deepEqual(filterBans(bans, "troll").map(b => b.id), [2], "account name");
    assert.deepEqual(filterBans(bans, "abcdef01").map(b => b.id), [1], "unique id, case-insensitive");
    assert.deepEqual(filterBans(bans, "moderator-7781").map(b => b.id), [1], "moderator");
    assert.deepEqual(filterBans(bans, "harass").map(b => b.id), [2], "reason");
    assert.deepEqual(filterBans(bans, "D1767225600", { formatTime: fmt }).map(b => b.id), [1], "formatted expiry date");
    assert.deepEqual(filterBans(bans, "permanent", { formatTime: fmt }).map(b => b.id), [2], "permanent label");
    assert.deepEqual(filterBans(bans, "dauerhaft", { formatTime: fmt, permanentLabel: "Dauerhaft" }).map(b => b.id), [2], "localised permanent label");
    assert.deepEqual(filterBans(bans, "10.0.0").map(b => b.id), [3], "ip value");
    assert.deepEqual(filterBans(bans, "#3").map(b => b.id), [3], "ban id");
});

test("(350) several terms narrow the result (AND) and unmatched terms yield nothing", () => {
    assert.deepEqual(filterBans(bans, "spam moderator").map(b => b.id), [1]);
    assert.deepEqual(filterBans(bans, "spam owner").map(b => b.id), []);
    assert.deepEqual(filterBans(bans, "nope"), []);
});

test("(350) search text never throws on sparse or malformed entries", () => {
    assert.equal(banSearchText(null), "");
    assert.equal(banSearchText({}), "permanent");
    assert.match(banSearchText({ expires_at: 1762000000 }, { formatTime: fmt }), /d1762000000/);
    assert.deepEqual(filterBans([{ id: 9 }, null, "x"], "#9").map(b => b?.id), [9]);
});
