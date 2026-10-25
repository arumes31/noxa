import assert from "node:assert/strict";
import { test } from "node:test";
import { primarySessionLabel, resolveAccountTarget, resolveAccountTargetByUID } from "../src/account-identity.js";
import { setLanguage, t } from "../src/i18n.js";

const desktop = { client_id: "d", unique_id: "u1", nickname: "Ann", channel_id: 3 };
const phone = { client_id: "p", unique_id: "u1", nickname: "Ann", on_mobile: true, channel_id: 0 };
const guest = { client_id: "g", nickname: "Guest" };
const other = { client_id: "o", unique_id: "u2", nickname: "Bob" };
const clients = [phone, other, desktop, guest];

test("a clicked phone session resolves to its account and the desktop primary session", () => {
    const target = resolveAccountTarget(clients, "p", "me");
    assert.equal(target.accountID, "u1");
    assert.equal(target.primary.client_id, "d");
    assert.deepEqual(target.sessions.map(s => s.client_id), ["p", "d"]);
    assert.deepEqual(target.others.map(s => s.client_id), ["p"]);
    assert.equal(target.clicked, phone);
    assert.equal(target.multiple, true);
    assert.equal(target.primaryDevice, "desktop");
    assert.equal(resolveAccountTarget(clients, "d", "me").key, target.key, "both sessions share the account key");
});

test("the viewer's own session always leads its account", () => {
    assert.equal(resolveAccountTarget(clients, "d", "p").primary.client_id, "p");
});

test("single sessions and anonymous guests resolve to themselves; unknown ids to null", () => {
    const single = resolveAccountTarget(clients, "o", "me");
    assert.equal(single.multiple, false); assert.equal(single.primary, other); assert.equal(single.accountID, "u2");
    const anonymous = resolveAccountTarget(clients, "g", "me");
    assert.equal(anonymous.accountID, ""); assert.equal(anonymous.sessions.length, 1);
    assert.equal(resolveAccountTarget(clients, "missing", "me"), null);
    assert.equal(resolveAccountTargetByUID(clients, "", "me"), null);
    assert.equal(resolveAccountTargetByUID(clients, "nobody", "me"), null);
});

test("resolving by unique_id picks the same primary session as resolving by client id", () => {
    const byUID = resolveAccountTargetByUID(clients, "u1", "me");
    assert.equal(byUID.primary.client_id, "d");
    assert.equal(byUID.clicked, byUID.primary);
    assert.equal(byUID.sessions.length, 2);
});

test("the primary session label names the device and session count, and is empty for single sessions", () => {
    setLanguage("de");
    assert.equal(primarySessionLabel(resolveAccountTarget(clients, "p", "me"), t), "Primäre Sitzung: Desktop (2 Geräte verbunden)");
    setLanguage("en");
    assert.equal(primarySessionLabel(resolveAccountTarget([phone, { ...phone, client_id: "p2" }], "p", "me"), t), "Primary session: Mobile (2 devices connected)");
    assert.equal(primarySessionLabel(resolveAccountTarget(clients, "o", "me"), t), "");
    assert.equal(primarySessionLabel(null, t), "");
});
