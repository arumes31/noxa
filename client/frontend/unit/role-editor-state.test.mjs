import assert from "node:assert/strict";
import { test } from "node:test";
import { roleDraft, roleIsDirty, roleTemplate, reorderedRoles } from "../src/role-editor-state.js";

test("role drafts are isolated and permission ordering does not cause dirty state", () => {
    const saved = { id: 1, name: "Member", permissions: ["speak", "connect"] };
    const draft = roleDraft(saved);
    assert.equal(roleIsDirty(saved, draft), false);
    draft.permissions.push("manage_roles");
    assert.equal(roleIsDirty(saved, draft), true);
    assert.deepEqual(saved.permissions, ["speak", "connect"]);
    draft.name = "Changed";
    assert.equal(saved.name, "Member");
});

test("templates cannot propose grants outside the server-provided allowance", () => {
    assert.deepEqual(roleTemplate("administrator", ["speak"]), []);
    assert.deepEqual(roleTemplate("member", ["view_channel", "connect"]), ["view_channel", "connect"]);
});

test("keyboard ordering is lowest-first on the wire and never moves everyone", () => {
    const roles = [{ id: 3, position: 2 }, { id: 1, position: 0 }, { id: 2, position: 1 }];
    assert.deepEqual(reorderedRoles(roles, 2, 1), [1, 3, 2]);
    assert.equal(reorderedRoles(roles, 1, 1), null);
    assert.equal(reorderedRoles(roles, 2, -1), null);
    assert.deepEqual(roles.map((r) => r.id), [3, 1, 2]);
});
