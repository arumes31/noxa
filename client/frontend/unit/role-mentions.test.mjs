import assert from "node:assert/strict";
import { test } from "node:test";
import { roleMentionChoices, roleMentionLabel, mentionFlags } from "../src/role-mentions.js";
import { roleDraft, roleIsDirty } from "../src/role-editor-state.js";
import { sessionUserID } from "../src/session-identity.js";

test("registered-account role notifications use the server identity rather than the local device key", () => {
    const state = { myClientID: "connection", myUniqueID: "device-key", clients: [{ client_id: "connection", unique_id: "account" }] };
    assert.deepEqual(mentionFlags({ role_mentions: ["account"] }, sessionUserID(state)), { direct: false, role: true });
    assert.equal(sessionUserID({ myUniqueID: "guest-key", clients: [{ unique_id: "other" }] }), "guest-key");
});

test("only authoritative recipient metadata triggers role notifications", () => {
    assert.deepEqual(mentionFlags({ text: "@admin @moderator <@&12>", mentions: [], role_mentions: [] }, "me"), { direct: false, role: false });
    assert.deepEqual(mentionFlags({ mentions: ["other"], role_mentions: ["me"] }, "me"), { direct: false, role: true });
    assert.deepEqual(mentionFlags({ mentions: ["me"], role_mentions: ["other"] }, "me"), { direct: true, role: false });
    assert.deepEqual(mentionFlags({ mentions: [""], role_mentions: [""] }, ""), { direct: false, role: false });
});

test("role choices use stable IDs, preserve duplicate names and collapse duplicate assignments", () => {
    const clients = [{ roles: [{ id: 12, name: "Raid leaders" }, { id: 13, name: "Raid leaders" }] }, { roles: [{ id: 12, name: "Raid leaders" }] }];
    assert.deepEqual(roleMentionChoices(clients, "raid"), [
        { token: "<@&12>", label: "@Raid leaders", id: 12 },
        { token: "<@&13>", label: "@Raid leaders", id: 13 },
    ]);
    assert.equal(roleMentionLabel(clients, "12"), "@Raid leaders");
    assert.equal(roleMentionLabel(clients, "99"), "@role:99");
    assert.equal(roleMentionChoices(clients, "missing").length, 0);
});

test("mentionability participates in dirty state and acknowledged drafts", () => {
    const saved = { id: 12, name: "Raid leaders", mentionable: true };
    assert.equal(roleDraft(saved).mentionable, true);
    assert.equal(roleIsDirty(saved, { ...saved, mentionable: false }), true);
    assert.equal(roleDraft({}).mentionable, false);
});
