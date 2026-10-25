import { test } from "node:test";
import assert from "node:assert/strict";
import { setLanguage, t } from "../src/i18n.js";
import { PERMISSION_LABELS, deniedActionMessage, describeDeniedError, isPermissionDenial, missingPermission, missingPermissionHint, permissionLabel } from "../src/permission-denied.js";

test("(321) every permission key has a German and English label", () => {
    for (const [key, labels] of Object.entries(PERMISSION_LABELS)) {
        assert.ok(labels.en && labels.de, key);
        assert.equal(permissionLabel(key, { language: "de" }), labels.de);
        assert.equal(permissionLabel(key, { language: "en" }), labels.en);
    }
    assert.equal(permissionLabel("manage_roles", { language: "de", capabilities: [{ key: "manage_roles", en: "Roles", de: "Rollen (Server)" }] }), "Rollen (Server)", "server catalog wins");
    assert.equal(permissionLabel("manage_roles", { language: "fr" }), "Manage roles", "unknown language falls back to English");
    assert.equal(permissionLabel("future_right", { language: "en" }), "future right", "well-formed unknown key is readable");
    assert.equal(permissionLabel("<script>", { language: "en" }), "");
    assert.equal(permissionLabel("", { language: "en" }), "");
});

test("(321) the missing permission is extracted from structured errors, conventional texts and known server messages", () => {
    assert.equal(missingPermission({ permission: "ban_members" }), "ban_members");
    assert.equal(missingPermission({ capability: "Manage_Roles" }), "manage_roles");
    assert.equal(missingPermission("4: insufficient permission: kick_members"), "kick_members");
    assert.equal(missingPermission("permission denied: manage_channel_access"), "manage_channel_access");
    assert.equal(missingPermission("missing permission “upload_files”"), "upload_files");
    assert.equal(missingPermission(new Error("requires send_messages")), "send_messages");
    assert.equal(missingPermission("not a member of this channel"), "view_channel");
    assert.equal(missingPermission("you can only edit your own messages"), "manage_messages");
    assert.equal(missingPermission("you cannot manage this role or channel"), "", "ambiguous server text is not guessed");
    assert.equal(missingPermission("you cannot manage this role or channel", "manage_roles"), "manage_roles", "a caller that knows its action supplies the key");
    assert.equal(missingPermission("message not found", "manage_roles"), "", "fallback applies to denials only");
    assert.equal(missingPermission("requires a connected tab"), "", "'requires' alone is not a permission key");
    assert.equal(missingPermission("permission denied: <img onerror=x>"), "");
});

test("(321) denial detection and the shared message formats in both languages", () => {
    assert.equal(isPermissionDenial("4: permission denied"), true);
    assert.equal(isPermissionDenial("forbidden"), true);
    assert.equal(isPermissionDenial("Not Authorized"), true);
    assert.equal(isPermissionDenial("channel is full"), false);
    setLanguage("de");
    assert.equal(deniedActionMessage("kick_members"), "Aktion nicht möglich: dafür fehlt die Berechtigung „Mitglieder vom Server entfernen“.");
    assert.equal(missingPermissionHint("connect"), "Fehlende Berechtigung: Sprachkanal betreten");
    assert.equal(describeDeniedError("permission denied: ban_members"), "Aktion nicht möglich: dafür fehlt die Berechtigung „Mitglieder sperren“.");
    assert.equal(describeDeniedError("permission denied"), t("permission.denied.actionGeneric"));
    assert.equal(describeDeniedError("channel is full"), "");
    setLanguage("en");
    assert.equal(deniedActionMessage("kick_members"), "Action not possible: this requires the permission “Kick members from server”.");
    assert.equal(deniedActionMessage("<b>"), "Action not possible: a required permission is missing.");
});
