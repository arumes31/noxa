import assert from "node:assert/strict";
import { test } from "node:test";

import {
    applyPreset, effectOf, groupCapabilities, memberCounts, nextEffect, overrideChanges, presetOf, reorderedRoles, roleAllowed,
    roleDirty, roleDraft, roleTemplate, setOverride, setSynced, setWithDependencies, withRole,
} from "../src/core/access-matrix.js";
import {
    Admin, auditDetail, auditMatches, BAN_DURATIONS, banExpiresAt, banStatus, durationParts, emojiName, inviteLink, joinList, parseInvite, splitList,
} from "../src/core/admin.js";

const EVERYONE = 1;
const capabilities = [
    { key: "view_channel", group: "access", en: "View channel", de: "Kanal sehen", channel: true },
    { key: "send_messages", group: "text", en: "Send messages", de: "Nachrichten senden", channel: true, requires: ["view_channel"] },
    { key: "connect", group: "voice", en: "Connect to voice", de: "Sprachkanal betreten", channel: true, requires: ["view_channel"] },
    { key: "speak", group: "voice", en: "Speak", de: "Sprechen", channel: true, requires: ["connect"] },
    { key: "manage_roles", group: "server", en: "Manage roles", de: "Rollen verwalten", channel: false },
];

test("an override moves between inherit, allow and deny", () => {
    const subject = { role_id: 5 };
    let overrides = [];
    assert.equal(effectOf(overrides, subject, "speak"), "inherit");
    overrides = setOverride(overrides, subject, "speak", nextEffect("inherit"));
    assert.deepEqual(overrides, [{ role_id: 5, capability: "speak", effect: "allow" }]);
    overrides = setOverride(overrides, subject, "speak", nextEffect("allow"));
    assert.deepEqual(overrides, [{ role_id: 5, capability: "speak", effect: "deny" }]);
    overrides = setOverride(overrides, subject, "speak", nextEffect("deny"));
    assert.deepEqual(overrides, []);
    assert.throws(() => setOverride([], subject, "speak", "maybe"));
});

test("role and member subjects never overwrite each other", () => {
    let overrides = setOverride([], { role_id: 5 }, "connect", "allow");
    overrides = setOverride(overrides, { user_id: 9 }, "connect", "deny");
    assert.equal(effectOf(overrides, { role_id: 5 }, "connect"), "allow");
    assert.equal(effectOf(overrides, { user_id: 9 }, "connect"), "deny");
    assert.deepEqual(overrides[1], { user_id: 9, capability: "connect", effect: "deny" });
    // The original list is untouched (drafts are copied, never mutated).
    const before = [...overrides];
    setOverride(overrides, { role_id: 5 }, "connect", "inherit");
    assert.deepEqual(overrides, before);
});

test("presets rewrite only @everyone and are recognised again", () => {
    const member = { role_id: 7, capability: "speak", effect: "allow" };
    const pub = applyPreset([member], EVERYONE, "public");
    assert.equal(presetOf(pub, EVERYONE), "public");
    assert.ok(pub.includes(member));
    const priv = applyPreset(pub, EVERYONE, "private");
    // Private denies view but keeps the other @everyone rules the preset does not name.
    assert.equal(effectOf(priv, { role_id: EVERYONE }, "view_channel"), "deny");
    assert.equal(effectOf(priv, { role_id: EVERYONE }, "speak"), "allow");
    assert.equal(presetOf(priv, EVERYONE), "");
    assert.equal(presetOf(applyPreset([], EVERYONE, "private"), EVERYONE), "private");
    const read = applyPreset([], EVERYONE, "readOnly");
    assert.equal(effectOf(read, { role_id: EVERYONE }, "send_messages"), "deny");
    assert.equal(presetOf(read, EVERYONE), "readOnly");
    const listen = applyPreset([], EVERYONE, "listenOnly");
    assert.equal(effectOf(listen, { role_id: EVERYONE }, "speak"), "deny");
    assert.equal(effectOf(listen, { role_id: EVERYONE }, "connect"), "allow");
    assert.throws(() => applyPreset([], EVERYONE, "nonsense"));
});

test("changes list each subject's before and after effect", () => {
    const before = [{ role_id: 1, capability: "speak", effect: "allow" }, { user_id: 4, capability: "connect", effect: "deny" }];
    const after = [{ role_id: 1, capability: "speak", effect: "deny" }, { role_id: 2, capability: "connect", effect: "allow" }];
    assert.deepEqual(overrideChanges(before, after), [
        { role_id: 1, capability: "speak", effect: "deny", before: "allow", after: "deny" },
        { user_id: 4, capability: "connect", effect: "deny", before: "deny", after: "inherit" },
        { role_id: 2, capability: "connect", effect: "allow", before: "inherit", after: "allow" },
    ].sort((a, b) => `${a.role_id || 0}:${a.user_id || 0}:${a.capability}`.localeCompare(`${b.role_id || 0}:${b.user_id || 0}:${b.capability}`)));
    assert.deepEqual(overrideChanges(before, before), []);
});

test("syncing drops own overrides and customising starts from the inherited ones", () => {
    const inherited = [{ role_id: 1, capability: "view_channel", effect: "deny" }];
    const draft = { channel_id: 3, parent_id: 2, synced: true, overrides: [] };
    const custom = setSynced(draft, false, inherited);
    assert.deepEqual(custom.overrides, inherited);
    assert.notEqual(custom.overrides, inherited);
    assert.deepEqual(setSynced(custom, true).overrides, []);
});

test("prerequisites follow allow and deny", () => {
    const subject = { role_id: 2 };
    let overrides = setOverride([], subject, "connect", "deny");
    overrides = setWithDependencies(overrides, capabilities, subject, "speak", "allow");
    assert.equal(effectOf(overrides, subject, "connect"), "allow");
    overrides = setWithDependencies(overrides, capabilities, subject, "view_channel", "deny");
    assert.equal(effectOf(overrides, subject, "speak"), "deny");
    assert.equal(effectOf(overrides, subject, "connect"), "deny");
});

test("capabilities group by category and filter in the UI language", () => {
    const groups = groupCapabilities(capabilities, "", "de", { channelOnly: true });
    assert.deepEqual(groups.map(g => g.group), ["access", "text", "voice"]);
    assert.deepEqual(groupCapabilities(capabilities, "sprech", "de").flatMap(g => g.items.map(i => i.key)), ["speak"]);
    assert.deepEqual(groupCapabilities(capabilities, "roles", "de").flatMap(g => g.items.map(i => i.key)), ["manage_roles"]);
});

test("effective role access follows overrides and requirements", () => {
    const state = { capabilities, policy: { everyone_id: EVERYONE, roles: [{ id: EVERYONE, permissions: ["view_channel", "connect"] }, { id: 2, permissions: ["speak"] }] } };
    const role = state.policy.roles[1];
    assert.equal(roleAllowed(state, [], role, "speak"), true);
    assert.equal(roleAllowed(state, applyPreset([], EVERYONE, "private"), role, "speak"), false);
    assert.equal(roleAllowed(state, [{ role_id: 2, capability: "view_channel", effect: "allow" }, ...applyPreset([], EVERYONE, "private")], role, "speak"), true);
});

test("role drafts, templates and reordering", () => {
    const role = { id: 4, name: "Mods", position: 2, color: "#112233", icon_image: "data:...", permissions: ["speak", "connect"] };
    const draft = roleDraft(role);
    assert.equal("icon_image" in draft, false);
    assert.deepEqual(draft.permissions, ["connect", "speak"]);
    assert.equal(roleDirty(role, draft), false);
    assert.equal(roleDirty(role, { ...draft, hoist: true }), true);
    assert.deepEqual(roleTemplate("moderator", ["speak", "kick_members"]), ["speak", "kick_members"]);
    assert.deepEqual(roleTemplate("administrator", ["speak"]), []);
    const roles = [{ id: 1, position: 0 }, { id: 4, position: 1 }, { id: 6, position: 2 }];
    assert.deepEqual(reorderedRoles(roles, 4, 1), [1, 6, 4]);
    assert.equal(reorderedRoles(roles, 4, -1), null); // @everyone stays at the bottom
    assert.equal(reorderedRoles(roles, 1, 1), null);
    assert.deepEqual([...memberCounts({ members: [{ role_ids: [4, 6] }, { role_ids: [4] }] })], [[4, 2], [6, 1]]);
    assert.deepEqual(withRole([6, 4], 5, true), [4, 5, 6]);
    assert.deepEqual(withRole([6, 4], 4, false), [6]);
});

test("ban durations and expiry", () => {
    const now = Date.UTC(2026, 0, 1, 12, 0, 0);
    assert.deepEqual(BAN_DURATIONS.map(d => d.seconds), [300, 3600, 86400, 0]);
    assert.equal(banExpiresAt(300, now), now + 300000);
    assert.equal(banExpiresAt(86400, now) - now, 24 * 3600 * 1000);
    assert.equal(banExpiresAt(0, now), 0);
    assert.deepEqual(banStatus({ expires_at: 0 }, now), { permanent: true, expired: false, remaining: Infinity });
    assert.deepEqual(banStatus({ expires_at: now / 1000 + 3600 }, now), { permanent: false, expired: false, remaining: 3600 });
    assert.deepEqual(banStatus({ expires_at: now / 1000 - 1 }, now), { permanent: false, expired: true, remaining: 0 });
    assert.equal(banStatus({ expires_at: now / 1000 + 0.2 }, now).remaining, 1);
    assert.deepEqual(durationParts(300), { value: 5, unit: "minute" });
    assert.deepEqual(durationParts(3600), { value: 1, unit: "hour" });
    assert.deepEqual(durationParts(86400 * 3), { value: 3, unit: "day" });
    assert.deepEqual(durationParts(10), { value: 1, unit: "minute" });
});

test("invite links round-trip and reject malformed tokens", () => {
    const token = "A".repeat(42) + "_";
    const link = inviteLink("https://chat.example/", 12, token);
    assert.equal(link, `https://chat.example/app/#/invite/12/${token}`);
    assert.deepEqual(parseInvite(link.split("#/invite/")[1]), { channelID: 12, token });
    assert.equal(parseInvite(`12/${token}x`), null);
    assert.equal(parseInvite(`0/${token}`), null);
    assert.equal(parseInvite("12"), null);
});

test("filter lists, emoji names and audit details", () => {
    assert.deepEqual(splitList("spam, scam\n\nbad word"), ["spam", "scam", "bad word"]);
    assert.equal(joinList(["a", " b ", "a", ""]), "a,b");
    assert.equal(emojiName("Party Parrot.GIF"), "party_parrot");
    assert.equal(emojiName("..."), "");
    const structured = auditDetail({ detail: JSON.stringify({ version: 1, channel_ids: [3], text: "x", before: { a: 1 }, after: { a: 2 } }) });
    assert.deepEqual([structured.text, structured.before, structured.after, structured.channels], ["x", { a: 1 }, { a: 2 }, [3]]);
    assert.deepEqual(auditDetail({ detail: "{not json" }).text, "{not json");
    const entry = { actor: "uid-1", action: "kick", target: "uid-2", created_at: 1000 };
    const names = new Map([["uid-1", "Rowan"]]);
    assert.equal(auditMatches(entry, { actor: "rowan", action: "KICK" }, names), true);
    assert.equal(auditMatches(entry, { target: "rowan" }, names), false);
    assert.equal(auditMatches(entry, { from: 1000 * 1000, to: 1001 * 1000 }), true);
    assert.equal(auditMatches(entry, { from: 1001 * 1000 }), false);
});

test("requests carry the documented frames and acknowledgements", async () => {
    const sent = [];
    const conn = {
        request: (type, payload, reply, options) => { sent.push({ type, payload, reply, accept: options?.accept }); return Promise.resolve({}); },
        send: (type, payload) => sent.push({ type, payload }),
    };
    const session = { conn, clientID: "me", clients: () => [{ client_id: "a", unique_id: "u" }, { client_id: "b", unique_id: "u" }, { client_id: "me", unique_id: "u" }] };
    const admin = new Admin(session);
    await admin.ban("a", "spam", 3600);
    assert.deepEqual(sent[0].payload, { ack_requested: true, client_id: "a", from_server: true, ban: true, reason: "spam", duration_seconds: 3600 });
    assert.equal(sent[0].type, 13);
    assert.equal(sent[0].reply, 157);
    assert.equal(sent[0].accept({ client_id: "a", ban: true }), true);
    assert.equal(sent[0].accept({ client_id: "a", ban: false }), false);
    await admin.disconnect({ client_id: "a", channel_id: 4 }, "afk");
    assert.deepEqual(sent[1].payload, { ack_requested: true, client_id: "a", expected_channel_id: 4, reason: "afk" });
    sent.length = 0;
    assert.equal(await admin.kickSessions("u"), 2);
    assert.deepEqual(sent.map(s => s.payload.client_id), ["a", "b"]);
    sent.length = 0;
    await admin.complain("u", "rude");
    assert.deepEqual(sent.map(s => s.type), [34, 8]); // the complaint, then a Ping whose Pong confirms it
    sent.length = 0;
    await admin.voice({ client_id: "a", channel_id: 4 }, { muted: true });
    assert.deepEqual(sent[0].payload, { client_id: "a", channel_id: 4, muted: true, deafened: undefined });
});
