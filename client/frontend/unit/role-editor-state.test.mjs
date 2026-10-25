import assert from "node:assert/strict";
import { test } from "node:test";
import { roleDraft, roleIsDirty, roleTemplate, reorderedRoles } from "../src/role-editor-state.js";
import * as roleEditorStateModule from "../src/role-editor-state.js";

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

test("(335) a stale role form detects server changes to the same role and tolerates unrelated ones", () => {
    const { roleFormBaseline, roleSaveConflict } = roleEditorStateModule;
    const open = { policy: { revision: 7, roles: [{ id: 1, name: "Member", permissions: ["speak", "connect"] }, { id: 2, name: "Mod", permissions: ["kick_members"] }] } };
    const baseline = roleFormBaseline(open, 1);
    assert.equal(baseline.revision, 7);
    assert.equal(roleSaveConflict(baseline, open), null, "unchanged snapshot is no conflict");
    const unrelated = { policy: { revision: 8, roles: [{ id: 1, name: "Member", permissions: ["connect", "speak"] }, { id: 2, name: "Moderator", permissions: ["kick_members", "ban_members"] }] } };
    assert.deepEqual(roleSaveConflict(baseline, unrelated), { kind: "policy", blocking: false, revision: 8, name: "Member" }, "another role changed: rebase, do not block");
    const sameRole = { policy: { revision: 9, roles: [{ id: 1, name: "Member", color: "#ff0000", permissions: ["speak", "connect"] }, { id: 2, name: "Mod", permissions: ["kick_members"] }] } };
    assert.deepEqual(roleSaveConflict(baseline, sameRole), { kind: "role", blocking: true, revision: 9, name: "Member" });
    const deleted = { policy: { revision: 10, roles: [{ id: 2, name: "Mod", permissions: ["kick_members"] }] } };
    assert.deepEqual(roleSaveConflict(baseline, deleted), { kind: "deleted", blocking: true, revision: 10, name: "" });
    assert.equal(roleSaveConflict(baseline, null), null);
    assert.equal(roleSaveConflict(null, open), null);
});

test("(335) a new-role form only needs the revision and rebases onto any newer snapshot", () => {
    const { roleFormBaseline, roleSaveConflict } = roleEditorStateModule;
    const baseline = roleFormBaseline({ policy: { revision: 3, roles: [] } }, 0);
    assert.equal(baseline.exists, true);
    assert.equal(roleSaveConflict(baseline, { policy: { revision: 3, roles: [] } }), null);
    assert.equal(roleSaveConflict(baseline, { policy: { revision: 4, roles: [{ id: 9, name: "x" }] } }).blocking, false);
    assert.equal(roleSaveConflict(baseline, { policy: { revision: 4, roles: [] } }).revision, 4);
});
