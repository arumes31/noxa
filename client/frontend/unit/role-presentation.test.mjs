import test from "node:test";
import assert from "node:assert/strict";
import { memberRoles, roleColor, hoistedRoles } from "../src/role-presentation.js";

test("snapshot roles order cosmetics and hoist each member once", () => {
    const low = { id: 20, name: "Helper", position: 1, hoist: true, color: "#abcdef" };
    const high = { id: 30, name: "Moderator", position: 2, hoist: true };
    const state = { clients: [{ unique_id: "a", roles: [low, high] }, { unique_id: "b", roles: [low] }, { unique_id: "guest" }] };
    assert.deepEqual(memberRoles(state, "a"), [high, low]);
    assert.deepEqual(state.clients[0].roles, [low, high]);
    assert.deepEqual(hoistedRoles(state).map((h) => [h.group.id, h.members.map((m) => m.unique_id)]), [[30, ["a"]], [20, ["b"]]]);
    assert.deepEqual(memberRoles(state, "guest"), []);
    state.clients = [];
    assert.deepEqual(memberRoles(state, "a"), []);
    assert.deepEqual(hoistedRoles(state), []);
});

test("role colors admit only the stored hex format", () => {
    assert.equal(roleColor("#AbCdEf"), "#AbCdEf");
    for (const input of [null, "red", "var(--secret)", "url(https://example.invalid)", "#123"]) assert.equal(roleColor(input), "");
});
