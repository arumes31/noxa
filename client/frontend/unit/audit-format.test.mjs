import test from "node:test";
import assert from "node:assert/strict";
import { auditChanges, auditResource, parseAuditDetail } from "../src/audit-format.js";
import { setLanguage } from "../src/i18n.js";

test("role audit describes changed values and permissions without protocol keys", () => {
    setLanguage("en");
    const changes = auditChanges({ name: "Member", permissions: [] }, { name: "Helper", permissions: ["speak"] }, [{ key: "speak", en: "Speak", de: "Sprechen" }]);
    assert.deepEqual(changes, [{ label: "Name", before: "Member", after: "Helper" }, { label: "Permissions", before: "—", after: "Speak" }]);
});

test("channel settings, overrides and parent are localized", () => {
    setLanguage("de");
    const changes = auditChanges(null, { settings: { Name: "Support", OpusBitrate: 64000 }, access: { parent_id: 1, overrides: [{ role_id: 20, capability: "speak", effect: "deny" }] } }, [{ key: "speak", en: "Speak", de: "Sprechen" }]);
    assert.ok(changes.some((r) => r.label === "Kanaleinstellungen / Audio-Bitrate" && r.after === "64000"));
    assert.ok(changes.some((r) => r.after === "Rolle #20: Sprechen — Verweigern"));
    setLanguage("en");
});

test("malformed historical text remains data and missing after means deletion", () => {
    assert.equal(parseAuditDetail("<script>bad()</script>").text, "<script>bad()</script>");
    assert.deepEqual(auditChanges({ name: "Old" }, null), [{ label: "Name", before: "Old", after: "—" }]);
});

test("legacy JSON cannot impersonate structured audit data and malformed arrays are harmless", () => {
    const detail = '{"version":1,"before":[null],"after":{"overrides":[null]}}';
    assert.equal(parseAuditDetail(detail).text, detail);
    const parsed = parseAuditDetail(detail, true);
    assert.equal(parsed.text, detail);
    assert.doesNotThrow(() => auditChanges(parsed.before, parsed.after));
});

test("ordinary edits identify unchanged roles, channels and members", () => {
    setLanguage("en");
    assert.equal(auditResource("roles.role_update", { after: { id: 20, name: "Helper", permissions: ["speak"] } }), "Role Helper (#20)");
    assert.equal(auditResource("roles.role_delete", { before: { id: 20, name: "Helper" }, after: null }), "Role Helper (#20)");
    assert.equal(auditResource("roles.channel_edit", { after: { access: { channel_id: 3 }, settings: { Name: "Lobby" } } }), "Channel Lobby (#3)");
    assert.equal(auditResource("roles.member_roles_set", { after: { user_id: 7, role_ids: [20] } }), "Member (#7)");
});
