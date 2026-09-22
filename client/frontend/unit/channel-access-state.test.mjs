import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { applyChannelPreset, channelOverrideChanges, setChannelOverride } from "../src/channel-access-state.js";

const presetContract = JSON.parse(readFileSync(new URL("../../../testdata/channel-access-presets.json", import.meta.url), "utf8"));
for (const preset of presetContract.presets) test(`${preset.key} matches the evaluator privacy contract and preserves exceptions`, () => {
    assert.deepEqual(applyChannelPreset([], 10, preset.key), preset.overrides);
    const exceptions = [{ role_id: 30, capability: "view_channel", effect: "allow" }, { user_id: 4, capability: "view_channel", effect: "deny" }];
    const initial = structuredClone(exceptions);
    const actual = applyChannelPreset(initial, 10, preset.key);
    assert.deepEqual(actual, [...exceptions, ...preset.overrides]);
    assert.deepEqual(initial, exceptions);
});

test("inherit removes an override without touching other subjects", () => {
    const rows = [{ role_id: 1, capability: "speak", effect: "deny" }, { user_id: 1, capability: "speak", effect: "allow" }];
    assert.deepEqual(setChannelOverride(rows, { role_id: 1 }, "speak", "inherit"), [rows[1]]);
    assert.equal(rows.length, 2);
});

test("presets preserve role and member exceptions and replace everyone rules", () => {
    const exception = { user_id: 42, capability: "send_messages", effect: "allow" };
    const rows = [{ role_id: 1, capability: "send_messages", effect: "allow" }, exception];
    const next = applyChannelPreset(rows, 1, "readOnlyPreset");
    assert.ok(next.includes(exception));
    assert.equal(next.filter((o) => o.role_id === 1 && o.capability === "send_messages").length, 1);
    assert.equal(next.find((o) => o.role_id === 1 && o.capability === "send_messages").effect, "deny");
    assert.equal(rows[0].effect, "allow");
});

test("sync differences include changed, removed and added subjects", () => {
    const before = [{ role_id: 1, capability: "view_channel", effect: "allow" }, { user_id: 2, capability: "speak", effect: "deny" }];
    const after = [{ role_id: 1, capability: "view_channel", effect: "deny" }, { role_id: 3, capability: "speak", effect: "allow" }];
    const changes = channelOverrideChanges(before, after);
    assert.equal(changes.length, 3);
    assert.equal(changes.find((row) => row.user_id === 2).after, "inherit");
    assert.equal(changes.find((row) => row.role_id === 3).before, "inherit");
    assert.deepEqual(channelOverrideChanges(before, [...before].reverse()), []);
});
