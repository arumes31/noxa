import { expect, test } from "./fixtures.js";

test("protected roles require an owner save before modal-confirmed deletion", async ({ page }) => {
    await page.evaluate(() => { window.__roleState.policy.roles.find(r => r.id === 20).deletion_protected = true; });
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    const roles = page.getByRole("dialog", { name: "Roles", exact: true }).first();
    await roles.getByRole("button", { name: "Member", exact: true }).click();
    const remove = roles.getByRole("button", { name: "Delete", exact: true });
    await expect(remove).toBeDisabled();
    await roles.getByLabel("Protect from deletion", { exact: true }).uncheck();
    await expect(remove).toBeDisabled();
    await roles.getByRole("button", { name: "Save", exact: true }).click();
    await expect(remove).toBeEnabled();
    expect(await page.evaluate(() => window.__roleCalls.at(-1).role.deletion_protected)).toBe(false);
    await remove.click();
    const confirmation = page.getByRole("dialog", { name: "Roles", exact: true }).last();
    await expect(confirmation).toContainText('Delete “Member”?');
    await confirmation.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(await page.evaluate(() => window.__roleCalls.some(c => c.kind === "role_delete"))).toBe(false);
    await remove.click();
    await confirmation.getByRole("button", { name: "Delete", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__roleCalls.at(-1).kind)).toBe("role_delete");
});

test("delegated role managers cannot change deletion protection", async ({ page }) => {
    await page.evaluate(() => { window.__roleState.actor_id = 2; window.__roleState.policy.roles.find(r => r.id === 20).deletion_protected = true; });
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    const roles = page.getByRole("dialog", { name: "Roles", exact: true });
    await roles.getByRole("button", { name: "Member", exact: true }).click();
    await expect(roles.getByLabel("Protect from deletion", { exact: true })).toBeDisabled();
    await expect(roles.getByRole("button", { name: "Delete", exact: true })).toBeDisabled();
});

test("role editor refreshes after managing member assignments", async ({ page }) => {
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    const roles = page.getByRole("dialog", { name: "Roles", exact: true });
    await roles.getByRole("button", { name: "Member", exact: true }).click();
    await roles.getByRole("button", { name: "Members", exact: true }).click();
    const members = page.getByRole("dialog", { name: "Members", exact: true });
    await members.getByRole("combobox").selectOption("30");
    await members.getByRole("checkbox", { name: /^Bob\b/ }).check();
    await members.getByRole("button", { name: "Add role", exact: true }).click();
    await expect(members.getByRole("status")).toHaveText("0 selected");
    await members.getByRole("button", { name: "Close", exact: true }).click();
    await roles.getByLabel("Role name", { exact: true }).fill("Members renamed");
    await roles.getByRole("button", { name: "Save", exact: true }).click();
    await expect(roles.getByRole("status")).toHaveText("Role saved.");
    await expect(roles.locator(".role-error")).toBeEmpty();
    expect(await page.evaluate(() => window.__roleCalls.at(-1))).toMatchObject({
        kind: "role_update", expected_revision: 2, role: { id: 20, name: "Members renamed" },
    });
});

test("opening member management preserves an unconfirmed role draft", async ({ page }) => {
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    const roles = page.getByRole("dialog", { name: "Roles", exact: true });
    await roles.getByRole("button", { name: "Member", exact: true }).click();
    await roles.getByLabel("Role name", { exact: true }).fill("Draft name");
    await roles.getByRole("button", { name: "Members", exact: true }).click();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(page.getByRole("dialog", { name: "Members", exact: true })).toHaveCount(0);
    await expect(roles.getByLabel("Role name", { exact: true })).toHaveValue("Draft name");
    await roles.getByRole("button", { name: "Members", exact: true }).click();
    await page.getByRole("dialog", { name: "Roles", exact: true }).last().getByRole("button", { name: "Discard changes", exact: true }).click();
    const members = page.getByRole("dialog", { name: "Members", exact: true });
    await expect(members.getByRole("checkbox", { name: /^Bob\b/ })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(roles.getByLabel("Role name", { exact: true })).toHaveValue("Member");
    await expect(roles.getByRole("button", { name: "Members", exact: true })).toBeFocused();
});

test("closing member management waits for its pending assignment before refreshing", async ({ page }) => {
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    const roles = page.getByRole("dialog", { name: "Roles", exact: true });
    await roles.getByRole("button", { name: "Member", exact: true }).click();
    await roles.getByRole("button", { name: "Members", exact: true }).click();
    const members = page.getByRole("dialog", { name: "Members", exact: true });
    await members.getByRole("combobox").selectOption("30");
    await members.getByRole("checkbox", { name: /^Bob\b/ }).check();
    await page.evaluate(() => { window.__roleGate = new Promise(resolve => { window.__releaseAssignment = resolve; }); });
    await members.getByRole("button", { name: "Add role", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__roleCalls.length)).toBe(1);
    await members.getByRole("button", { name: "Close", exact: true }).click();
    await expect(members).toHaveCount(0);
    await expect(roles.getByLabel("Role name", { exact: true })).toBeDisabled();
    await page.evaluate(() => { window.__releaseAssignment(); window.__roleGate = null; });
    await expect.poll(() => page.evaluate(() => window.__roleState.policy.revision)).toBe(2);
    await roles.getByLabel("Role name", { exact: true }).fill("After pending change");
    await roles.getByRole("button", { name: "Save", exact: true }).click();
    await expect(roles.getByRole("status")).toHaveText("Role saved.");
    expect(await page.evaluate(() => window.__roleCalls.at(-1).expected_revision)).toBe(2);
});

test("role mentionability saves and reloads independently of permissions", async ({ page }) => {
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    await page.getByRole("button", { name: "Member", exact: true }).click();
    await page.getByLabel("Allow everyone to mention this role", { exact: true }).check();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByLabel("Allow everyone to mention this role", { exact: true })).toBeChecked();
    expect(await page.evaluate(() => window.__roleCalls.at(-1).role)).toMatchObject({ id: 20, mentionable: true, permissions: ["speak"] });
    await page.getByRole("button", { name: "Moderator", exact: true }).click();
    await page.getByRole("button", { name: "Member", exact: true }).click();
    await expect(page.getByLabel("Allow everyone to mention this role", { exact: true })).toBeChecked();
    await page.getByLabel("Allow everyone to mention this role", { exact: true }).uncheck();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__roleState.policy.roles.find(role => role.id === 20).mentionable)).toBe(false);
});

test("child access remains editable without disclosing parent policy", async ({ page }) => {
    await page.evaluate(() => {
        const state = window.__roleState;
        state.parent_access_available = false;
        state.parent_overrides = [];
        state.effective_overrides = [{ role_id: 10, capability: "view_channel", effect: "deny" }];
        state.policy.channels[0].synced = true;
    });
    await page.getByRole("button", { name: "Channel access", exact: true }).click();
    await expect(page.getByRole("combobox", { name: "View channel", exact: true })).toHaveValue("deny");
    await page.getByRole("button", { name: "Customize this channel", exact: true }).click();
    await expect(page.getByRole("combobox", { name: "View channel", exact: true })).toHaveValue("deny");
    await expect(page.getByRole("button", { name: "Sync with parent", exact: true })).toBeDisabled();
    await page.getByRole("button", { name: "Preview role changes", exact: true }).click();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    const change = await page.evaluate(() => window.__roleCalls[0]);
    expect(change.channel.synced).toBe(false);
    expect(change.channel.overrides).toEqual([{ role_id: 10, capability: "view_channel", effect: "deny" }]);
});

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__noxa = { state: { serverGeneration: 1 } };
        window.__roleCalls = [];
        const permissions = [
            { key: "view_channel", group: "access", en: "View channel", de: "Kanal sehen", channel: true },
            { key: "connect", group: "voice", en: "Connect to voice", de: "Sprachkanal betreten", requires: ["view_channel"], channel: true },
            { key: "speak", group: "voice", en: "Speak", de: "Sprechen", requires: ["connect"], channel: true },
            { key: "manage_roles", group: "server", en: "Manage roles", de: "Rollen verwalten" },
            { key: "administrator", group: "server", en: "Administrator", de: "Administrator" },
        ];
        window.__roleState = { actor_id: 1, capabilities: permissions, grantable_capabilities: permissions.map((c) => c.key), manageable_role_ids: [10, 20, 30],
            effective_overrides: [], parent_overrides: [{ role_id: 10, capability: "view_channel", effect: "deny" }],
            policy: { revision: 1, owner_id: 1, everyone_id: 10, members: [], channels: [{ channel_id: 2, parent_id: 1, synced: false, overrides: [] }], roles: [
                { id: 10, name: "@everyone", position: 0, permissions: ["view_channel", "connect"] },
                { id: 20, name: "Member", position: 1, color: "#abcdef", permissions: ["speak"] },
                { id: 30, name: "Moderator", position: 2, permissions: ["manage_roles"] },
            ] } };
        window.__roleMembers = [
            { user_id: 1, nickname: "Owner", unique_id: "owner", role_ids: [], manageable: false },
            { user_id: 2, nickname: "Alice", unique_id: "alice", role_ids: [20], manageable: true },
            { user_id: 3, nickname: "Bob", unique_id: "bob", role_ids: [20], manageable: true },
        ];
        window.go = { main: { App: {
            RoleState: async () => structuredClone(window.__roleState),
            RoleMembers: async (query) => {
                window.__rosterCalls ||= []; window.__rosterCalls.push(structuredClone(query));
                if (window.__membersGate) await window.__membersGate;
                const matches = window.__roleMembers.filter((m) => m.user_id > (query.after_id || 0) && m.nickname.toLowerCase().includes((query.search || "").toLowerCase()));
                return { revision: window.__roleState.policy.revision, more: matches.length > 100, entries: structuredClone(matches.slice(0, 100)) };
            },
            PreviewChannelAccess: async (query) => {
                window.__impactCalls ||= []; window.__impactCalls.push(structuredClone(query));
                if (window.__impactGate) await window.__impactGate;
                if (window.__impactFailure) throw new Error("preview unavailable");
                return window.__impactResponse || { revision: window.__roleState.policy.revision, channel_id: query.scope_channel_id || query.change.channel.channel_id,
                    roles: window.__roleState.policy.roles.map(role => ({ role_id: role.id, name: role.name, changes: [{ capability: "view_channel", before: true, after: false }] })),
                    members: query.user_ids.map(id => ({ user_id: id, permissions: [{ capability: "view_channel", before: true, after: id === 1 }], changes: id === 1 ? [] : [{ capability: "view_channel", before: true, after: false }] })) };
            },
            CheckAccess: async (query) => {
                window.__lastAccessCheck = query;
                if (window.__checkGate) await window.__checkGate;
                return window.__checkResponse || { decision: { allowed: false, reason: "everyone_override", role_ids: [10], revision: window.__roleState.policy.revision }, can_manage_member: false };
            },
            RoleChange: async (change) => {
                window.__roleCalls.push(structuredClone(change));
                if (window.__roleFailure) throw new Error(window.__roleFailure);
                if (window.__roleGate) await window.__roleGate;
                const state = window.__roleState;
                if (change.expected_revision !== state.policy.revision) throw new Error("permissions changed; refresh");
                let created = 0;
                if (change.kind === "role_create") {
                    for (const role of state.policy.roles) if (role.position) role.position++;
                    created = 50;
                    state.policy.roles.push({ ...change.role, id: created, position: 1 });
                    state.manageable_role_ids.push(created);
                } else if (change.kind === "role_update") {
                    state.policy.roles = state.policy.roles.map((r) => r.id === change.role.id ? change.role : r);
                } else if (change.kind === "roles_reorder") {
                    for (const r of state.policy.roles) r.position = change.role_ids.indexOf(r.id);
                } else if (change.kind === "role_delete") state.policy.roles = state.policy.roles.filter((r) => r.id !== change.role_id);
                else if (change.kind === "member_roles_set") {
                    if (window.__memberFailure === change.user_id) throw new Error("member change denied");
                    window.__roleMembers.find((m) => m.user_id === change.user_id).role_ids = change.role_ids;
                } else if (change.kind === "default_member_role_set") {
                    state.policy.default_member_role_id = change.role_id;
                } else if (change.kind === "owner_transfer") {
                    state.policy.owner_id = change.user_id;
                } else if (change.kind === "channel_access_set") {
                    state.policy.channels = [change.channel];
                    state.effective_overrides = change.channel.synced ? state.parent_overrides : change.channel.overrides;
                }
                return { revision: ++state.policy.revision, created_role_id: created, enforcement_pending: !!window.__enforcementPending };
            },
        } } };
        window.__noxa.state.activeTabID = "server-a";
        window.__nativeTabID = "server-a";
        window.__tabCalls = [];
        const app = window.go.main.App;
        for (const name of ["RoleState","RoleMembers","RoleChange","CheckAccess","PreviewChannelAccess"]) {
            const original = app[name];
            app[`${name}ForTab`] = async (tabID, request) => {
                window.__tabCalls.push({ name, tabID });
                if (tabID !== window.__nativeTabID) throw new Error("server tab changed; refresh");
                return original(request);
            };
            app[name] = () => { throw new Error("unscoped native call"); };
        }
    });
    await page.route("**/__role_editor_test__", (route) => route.fulfill({ contentType: "text/html", body: `<!doctype html><html lang="en"><head><meta name="viewport" content="width=device-width, initial-scale=1"><link rel="stylesheet" href="/src/style.css"></head><body><button id="launch">Roles</button><button id="channel">Channel access</button><script type="module">
        import { openRolesManager } from '/src/roles-ui.js';
        import { openChannelAccess } from '/src/channel-access-ui.js';
        import { initModalSystem } from '/src/modal.js';
        import { setLanguage } from '/src/i18n.js';
        initModalSystem();
        window.setTestLanguage = setLanguage;
        document.querySelector('#launch').onclick = openRolesManager;
        document.querySelector('#channel').onclick = () => openChannelAccess(2);
        window.ready = true;
    </script></body></html>` }));
    await page.goto("/__role_editor_test__");
    await page.waitForFunction(() => window.ready);
});

test("owner can set and clear the role for new members without changing existing assignments", async ({ page }) => {
    await page.evaluate(() => {
        window.__roleState.policy.roles.push({ id: 40, name: "Admin", position: 3, permissions: ["administrator"] });
        window.__roleState.policy.roles[1].permissions = null;
    });
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    const select = page.getByRole("combobox", { name: "Role for new members", exact: false });
    await expect(select).toHaveValue("0");
    await expect(select.getByRole("option")).toHaveCount(3);
    await select.selectOption("20");
    await expect(select).toHaveValue("20");
    await expect(select).toBeEnabled();
    expect(await page.evaluate(() => window.__roleCalls[0])).toEqual({ kind: "default_member_role_set", role_id: 20, expected_revision: 1 });
    await page.getByRole("button", { name: "Member", exact: true }).click();
    await page.getByLabel("Role name", { exact: true }).fill("Unsaved name");
    await expect(select).toBeDisabled();
    await page.getByRole("button", { name: "Discard changes", exact: true }).click();
    await select.selectOption("0");
    await expect(select).toHaveValue("0");
    await expect(select).toBeEnabled();
    expect(await page.evaluate(() => window.__roleCalls[1])).toEqual({ kind: "default_member_role_set", role_id: 0, expected_revision: 2 });
    expect(await page.evaluate(() => window.__roleMembers[1].role_ids)).toEqual([20]);
});

test("default member role control is unavailable to delegated role managers", async ({ page }) => {
    await page.evaluate(() => { window.__roleState.actor_id = 2; });
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    await expect(page.getByLabel("Role name", { exact: true })).toBeVisible();
    await expect(page.getByRole("combobox", { name: "Role for new members", exact: false })).toHaveCount(0);
});

test("native tab activation rejects an old role draft before frontend reset", async ({ page }) => {
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    await page.getByRole("button", { name: "Member", exact: true }).click();
    await page.getByLabel("Role name", { exact: true }).fill("Old server draft");
    await page.evaluate(() => { window.__nativeTabID = "server-b"; });
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("Permissions changed");
    await expect(page.getByLabel("Role name", { exact: true })).toHaveValue("Old server draft");
    expect(await page.evaluate(() => window.__roleCalls)).toEqual([]);
    expect(await page.evaluate(() => window.__tabCalls.at(-1))).toEqual({ name: "RoleChange", tabID: "server-a" });
});

test("parent draft previews a synced descendant without enumerating members", async ({ page }) => {
    await page.evaluate(() => {
        window.__roleState.impact_channel_ids = [4];
        window.__noxa.state.channels = [{ ChannelID: 4, Name: "Design" }];
    });
    await page.getByRole("button", { name: "Channel access", exact: true }).click();
    await page.getByRole("button", { name: "Private", exact: true }).click();
    const scope = page.getByRole("combobox", { name: "Preview channel", exact: true });
    await expect(scope.getByRole("option")).toHaveCount(2);
    await expect(scope).toContainText("Design (#4)");
    await scope.selectOption("4");
    await page.getByRole("button", { name: "Preview role changes", exact: true }).click();
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeEnabled();
    expect(await page.evaluate(() => window.__impactCalls.at(-1))).toMatchObject({ scope_channel_id: 4,
        change: { channel: { channel_id: 2 } } });
    expect(await page.evaluate(() => window.__rosterCalls)).toBeUndefined();
    await scope.selectOption("2");
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
    await expect(page.locator(".channel-impact-member")).toHaveCount(0);
    await page.getByRole("button", { name: "Preview role changes", exact: true }).click();
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeEnabled();
    expect(await page.evaluate(() => window.__impactCalls.at(-1).scope_channel_id)).toBeUndefined();
    expect(await page.evaluate(() => window.__rosterCalls)).toBeUndefined();
});

test("late scoped preview cannot authorize a different descendant", async ({ page }) => {
    await page.evaluate(() => { window.__roleState.impact_channel_ids = [4, 5]; });
    await page.getByRole("button", { name: "Channel access", exact: true }).click();
    await page.getByRole("button", { name: "Private", exact: true }).click();
    const scope = page.getByRole("combobox", { name: "Preview channel", exact: true });
    await scope.selectOption("4");
    await page.evaluate(() => { window.__impactGate = new Promise(resolve => { window.__finishImpact = resolve; }); });
    await page.getByRole("button", { name: "Preview role changes", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__impactCalls?.length)).toBe(1);
    await scope.selectOption("5");
    await page.evaluate(() => window.__finishImpact());
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
    await expect(page.locator(".channel-impact-member")).toHaveCount(0);
    await page.getByRole("button", { name: "Preview role changes", exact: true }).click();
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeEnabled();
    expect(await page.evaluate(() => window.__impactCalls.at(-1).scope_channel_id)).toBe(5);
});

test("scoped preview rejects a response for the edited parent", async ({ page }) => {
    await page.evaluate(() => {
        window.__roleState.impact_channel_ids = [4];
        window.__impactResponse = { revision: 1, channel_id: 2, members: [0, 1, 2, 3].map(user_id => ({ user_id, changes: [] })) };
    });
    await page.getByRole("button", { name: "Channel access", exact: true }).click();
    await page.getByRole("button", { name: "Private", exact: true }).click();
    await page.getByRole("combobox", { name: "Preview channel", exact: true }).selectOption("4");
    await page.getByRole("button", { name: "Preview role changes", exact: true }).click();
    await expect(page.locator(".channel-impact [role=status]")).toContainText("could not");
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
});

test("changing override subject cannot retain a descendant review after panel recreation", async ({ page }) => {
    await page.evaluate(() => { window.__roleState.impact_channel_ids = [4]; });
    await page.getByRole("button", { name: "Channel access", exact: true }).click();
    await page.getByRole("button", { name: "Private", exact: true }).click();
    const scope = page.getByRole("combobox", { name: "Preview channel", exact: true });
    await scope.selectOption("4");
    await page.getByRole("button", { name: "Preview role changes", exact: true }).click();
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeEnabled();
    await page.getByRole("combobox", { name: "Role or member", exact: true }).selectOption("role:20");
    await expect(scope).toHaveValue("2");
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
    await page.getByRole("button", { name: "Preview role changes", exact: true }).click();
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeEnabled();
});

test("member batch stops on native activation between acknowledgements", async ({ page }) => {
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    await page.getByRole("button", { name: "Members", exact: true }).click();
    await page.getByRole("checkbox", { name: "Alice Member" }).check();
    await page.getByRole("checkbox", { name: "Bob Member" }).check();
    await page.evaluate(() => {
        window.__roleGate = new Promise((resolve) => { window.__finishRole = resolve; });
    });
    await page.getByRole("button", { name: "Add role", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__roleCalls.length)).toBe(1);
    await page.evaluate(() => { window.__nativeTabID = "server-b"; window.__finishRole(); });
    await expect(page.getByRole("dialog").last().getByRole("alert")).toContainText("remaining changes were stopped");
    expect(await page.evaluate(() => window.__roleCalls.map((c) => c.user_id))).toEqual([2]);
    expect(await page.evaluate(() => window.__tabCalls.filter((c) => c.name === "RoleChange"))).toEqual([
        { name: "RoleChange", tabID: "server-a" }, { name: "RoleChange", tabID: "server-a" },
    ]);
});

test("channel access save and preview stay bound to the opening tab", async ({ page }) => {
    await page.getByRole("button", { name: "Channel access", exact: true }).click();
    await page.getByRole("button", { name: "Private", exact: true }).click();
    await page.getByRole("button", { name: "Preview role changes", exact: true }).click();
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeEnabled();
    await page.evaluate(() => { window.__nativeTabID = "server-b"; });
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("Permissions changed");
    const panel = page.locator(".access-check");
    await panel.getByRole("button", { name: "Check access", exact: true }).click();
    await expect(panel).toContainText("Permissions changed");
    await panel.getByRole("searchbox").fill("Alice");
    await panel.getByRole("searchbox").press("Tab");
    await expect.poll(() => page.evaluate(() => window.__tabCalls.at(-1)?.name)).toBe("RoleMembers");
    expect(await page.evaluate(() => window.__roleCalls)).toEqual([]);
    expect(await page.evaluate(() => window.__lastAccessCheck)).toBeUndefined();
    expect(await page.evaluate(() => window.__tabCalls.every((c) => c.tabID === "server-a"))).toBe(true);
});

test("role editor creates a role only after acknowledgement", async ({ page }) => {
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    await page.getByRole("button", { name: "Create role", exact: true }).click();
    await page.getByLabel("Role name", { exact: true }).fill("Helpers");
    await page.getByLabel("Speak", { exact: false }).check();
    await page.evaluate(() => { window.__roleGate = new Promise((resolve) => { window.__finishRole = resolve; }); });
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByRole("navigation").getByRole("button", { name: "Helpers", exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
    await page.evaluate(() => window.__finishRole());
    await expect(page.getByRole("navigation").getByRole("button", { name: "Helpers", exact: true })).toBeVisible();
    expect(await page.evaluate(() => window.__roleCalls[0].expected_revision)).toBe(1);
});

test("ownership transfer confirms the member and waits for acknowledgement", async ({ page }) => {
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    await page.getByRole("button", { name: "Members", exact: true }).click();
    await page.getByRole("checkbox", { name: "Alice Member" }).check();
    await page.getByRole("button", { name: "Transfer ownership", exact: true }).click();
    await expect(page.getByRole("dialog").last()).toContainText("Alice (member #2)");
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(await page.evaluate(() => window.__roleCalls)).toHaveLength(0);
    await page.evaluate(() => { window.__roleGate = new Promise((resolve) => { window.__finishRole = resolve; }); });
    await page.getByRole("button", { name: "Transfer ownership", exact: true }).click();
    await page.getByRole("dialog").last().getByRole("button", { name: "Transfer ownership", exact: true }).click();
    await expect(page.getByRole("button", { name: "Transfer ownership", exact: true })).toBeDisabled();
    expect(await page.evaluate(() => window.__roleState.policy.owner_id)).toBe(1);
    await page.evaluate(() => window.__finishRole());
    await expect(page.getByRole("status")).toContainText("Ownership transferred");
    await expect(page.getByRole("button", { name: "Transfer ownership", exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Refresh", exact: true })).toBeDisabled();
    expect(await page.evaluate(() => window.__roleCalls)).toEqual([{ kind: "owner_transfer", expected_revision: 1, user_id: 2 }]);
});

test("non-owner role manager cannot offer ownership transfer", async ({ page }) => {
    await page.evaluate(() => { window.__roleState.actor_id = 3; });
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    await page.getByRole("button", { name: "Members", exact: true }).click();
    await expect(page.getByLabel("Search members")).toBeVisible();
    await expect(page.getByRole("button", { name: "Transfer ownership", exact: true })).toHaveCount(0);
});

test("stale role edit retains draft and supports explicit discard", async ({ page }) => {
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    await page.getByRole("button", { name: "Member", exact: true }).click();
    await page.getByLabel("Role name", { exact: true }).fill("Draft name");
    await page.evaluate(() => { window.__roleFailure = "permissions changed; refresh before saving"; });
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("Permissions changed");
    await expect(page.getByLabel("Role name", { exact: true })).toHaveValue("Draft name");
    await page.getByRole("button", { name: "Discard changes", exact: true }).click();
    await expect(page.getByLabel("Role name", { exact: true })).toHaveValue("Member");
});

test("saved change with pending enforcement cannot be retried", async ({ page }) => {
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    await page.getByRole("button", { name: "Member", exact: true }).click();
    await page.getByLabel("Role name", { exact: true }).fill("Helpers");
    await page.evaluate(() => { window.__enforcementPending = true; });
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("The change was saved. Protected actions are paused");
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
    await expect(page.getByRole("button", { name: "Create role", exact: true })).toBeDisabled();
    expect(await page.evaluate(() => window.__roleCalls.length)).toBe(1);
});

test("German editor supports keyboard ordering and narrow layouts", async ({ page }) => {
    await page.setViewportSize({ width: 380, height: 800 });
    await page.evaluate(() => window.setTestLanguage("de"));
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    const up = page.getByRole("button", { name: "Rolle nach oben verschieben: Member", exact: true });
    await up.focus();
    await page.keyboard.press("Enter");
    await expect.poll(() => page.evaluate(() => window.__roleCalls.length)).toBe(1);
    expect(await page.evaluate(() => window.__roleCalls[0].role_ids)).toEqual([10, 30, 20]);
    await expect(page.getByLabel("Berechtigungen suchen", { exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test("server change during save prevents follow-up calls and stale UI", async ({ page }) => {
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    await page.getByRole("button", { name: "Member", exact: true }).click();
    await page.getByLabel("Role name", { exact: true }).fill("Other server draft");
    await page.evaluate(() => { window.__roleGate = new Promise((resolve) => { window.__finishRole = resolve; }); });
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await page.evaluate(async () => {
        window.__noxa.state.serverGeneration++;
        const { closeServerDialogs } = await import("/src/modal.js");
        closeServerDialogs();
        window.__finishRole();
    });
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(await page.evaluate(() => window.__roleCalls.length)).toBe(1);
});

test("member batch shows committed and failed results separately", async ({ page }) => {
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    await page.getByRole("button", { name: "Members", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Members", exact: true });
    await dialog.getByRole("checkbox", { name: /Alice/ }).check();
    await dialog.getByRole("checkbox", { name: /Bob/ }).check();
    await dialog.getByRole("combobox", { name: "Choose a role", exact: true }).selectOption("30");
    await page.evaluate(() => { window.__memberFailure = 3; });
    await dialog.getByRole("button", { name: "Add role", exact: true }).click();
    await expect(dialog.getByRole("alert")).toContainText("remaining changes were stopped");
    await expect(dialog.locator(".role-member-row").filter({ hasText: "Alice" })).toContainText("Saved");
    await expect(dialog.locator(".role-member-row").filter({ hasText: "Bob" })).toContainText("Not saved");
    await expect(dialog.getByRole("button", { name: "Add role", exact: true })).toBeDisabled();
    expect(await page.evaluate(() => window.__roleCalls.map((c) => c.expected_revision))).toEqual([1, 2]);
});

test("channel presets save ordinary overrides and access check stays read-only", async ({ page }) => {
    await page.getByRole("button", { name: "Channel access", exact: true }).click();
    await page.getByRole("button", { name: "Private", exact: true }).click();
    await expect(page.getByRole("combobox", { name: "View channel", exact: true })).toHaveValue("deny");
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
    await page.getByRole("button", { name: "Preview role changes", exact: true }).click();
    await expect(page.locator(".channel-impact-members").first()).toContainText("View channel: Allowed → Denied");
    expect(await page.evaluate(() => window.__roleCalls.length)).toBe(0);
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
    const changes = await page.evaluate(() => window.__roleCalls);
    expect(changes[0].channel.overrides).toEqual([{ role_id: 10, capability: "view_channel", effect: "deny" }]);
    await page.getByRole("button", { name: "Check access", exact: true }).click();
    await expect(page.locator(".access-check")).toContainText("Denied — Channel rule for @everyone");
    expect(await page.evaluate(() => window.__roleCalls.length)).toBe(1);
    expect(await page.evaluate(() => window.__lastAccessCheck)).toMatchObject({ channel_id: 2, user_id: 0, expected_revision: 2 });
});

test("sync confirmation replaces custom policy and Customize copies parent overrides", async ({ page }) => {
    await page.getByRole("button", { name: "Channel access", exact: true }).click();
    await page.getByRole("button", { name: "Public", exact: true }).click();
    await page.getByRole("button", { name: "Sync with parent", exact: true }).click();
    await expect(page.getByRole("dialog").last()).toContainText("@everyone · View channel: Allow → Deny");
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    await page.getByRole("button", { name: "Preview role changes", exact: true }).click();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByRole("button", { name: "Customize this channel", exact: true })).toBeEnabled();
    await page.getByRole("button", { name: "Customize this channel", exact: true }).click();
    await expect(page.getByRole("combobox", { name: "View channel", exact: true })).toHaveValue("deny");
    await page.getByRole("button", { name: "Preview role changes", exact: true }).click();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__roleCalls.length)).toBe(2);
    expect(await page.evaluate(() => window.__roleCalls[0].channel)).toMatchObject({ synced: true, overrides: [] });
    expect(await page.evaluate(() => window.__roleCalls[1].channel)).toMatchObject({ synced: false, overrides: [{ role_id: 10, capability: "view_channel", effect: "deny" }] });
});

test("member selection retains keyboard focus", async ({ page }) => {
    await page.getByRole("button", { name: "Roles", exact: true }).click();
    await page.getByRole("button", { name: "Members", exact: true }).click();
    const member = page.getByRole("checkbox", { name: /Alice/ });
    await member.focus();
    await page.keyboard.press("Space");
    await expect(member).toBeChecked();
    await expect(member).toBeFocused();
});

test("customizing an unsaved sync copies the displayed parent rules", async ({ page }) => {
    await page.getByRole("button", { name: "Channel access", exact: true }).click();
    await page.getByRole("button", { name: "Public", exact: true }).click();
    await page.getByRole("button", { name: "Sync with parent", exact: true }).click();
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    await page.getByRole("button", { name: "Customize this channel", exact: true }).click();
    await expect(page.getByRole("combobox", { name: "View channel", exact: true })).toHaveValue("deny");
    expect(await page.evaluate(() => window.__roleCalls.length)).toBe(0);
});

test("read-only access picker includes protected members and explains hierarchy and prerequisites", async ({ page }) => {
    await page.getByRole("button", { name: "Channel access", exact: true }).click();
    const panel = page.locator(".access-check");
    await panel.getByRole("searchbox", { name: "Find a member to check", exact: true }).fill("Owner");
    await panel.getByRole("searchbox").press("Tab");
    await expect(panel.getByRole("combobox", { name: "Member to check", exact: true })).toContainText("Owner (#1)");
    await panel.getByRole("combobox", { name: "Member to check", exact: true }).selectOption("1");
    await page.evaluate(() => { window.__checkResponse = { decision: { allowed: false, reason: "requires", requirement: "view_channel", role_ids: [10], revision: 1 }, can_manage_member: false }; });
    await panel.getByRole("button", { name: "Check access", exact: true }).click();
    await expect(panel).toContainText("A required permission is missing (View channel): @everyone · Revision 1");
    await expect(panel).toContainText("Your hierarchy prevents you from moderating this member.");
    expect(await page.evaluate(() => window.__lastAccessCheck.user_id)).toBe(1);
    expect(await page.evaluate(() => window.__roleCalls.length)).toBe(0);
});

test("access preview discards replies after selecting a different member and rejects stale revisions", async ({ page }) => {
    await page.getByRole("button", { name: "Channel access", exact: true }).click();
    const panel = page.locator(".access-check");
    await panel.getByRole("searchbox").fill("Alice");
    await panel.getByRole("searchbox").press("Tab");
    await expect(panel.getByRole("combobox", { name: "Member to check", exact: true })).toContainText("Alice (#2)");
    await page.evaluate(() => { window.__checkGate = new Promise((resolve) => { window.__finishCheck = resolve; }); });
    await panel.getByRole("button", { name: "Check access", exact: true }).click();
    await panel.getByRole("combobox", { name: "Member to check", exact: true }).selectOption("2");
    await page.evaluate(() => { window.__finishCheck(); window.__checkGate = null; });
    await expect(panel).not.toContainText("Denied —");
    await expect(panel.getByRole("button", { name: "Check access", exact: true })).toBeEnabled();
    await page.evaluate(() => { window.__checkResponse = { decision: { allowed: true, reason: "owner", revision: 2 }, can_manage_member: true }; });
    await panel.getByRole("button", { name: "Check access", exact: true }).click();
    await expect(panel).toContainText("Permissions changed");
    await expect(panel).not.toContainText("Allowed —");
});

test("German access preview supports keyboard checking in a compact dialog", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 800 });
    await page.evaluate(() => {
        window.setTestLanguage("de");
        window.__checkResponse = { decision: { allowed: true, reason: "role_grant", role_ids: [20], revision: 1 }, can_manage_member: true };
    });
    await page.getByRole("button", { name: "Channel access", exact: true }).click();
    const panel = page.locator(".access-check");
    await panel.getByRole("combobox", { name: "Zu prüfendes Mitglied", exact: true }).focus();
    await page.keyboard.press("Tab");
    await expect(panel.getByRole("combobox", { name: "Zu prüfende Berechtigung", exact: true })).toBeFocused();
    await page.keyboard.press("Tab");
    await page.keyboard.press("Enter");
    await expect(panel).toContainText("Erlaubt — Durch Serverrollen erlaubt: Member · Revision 1");
    await expect(panel).toContainText("Deine Hierarchie erlaubt die Moderation dieses Mitglieds.");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await panel.scrollIntoViewIfNeeded();
    await page.screenshot({ path: "../../.cache/role-access-de.png" });
});

async function installMemberContextMenu(page) {
    await page.evaluate(async () => {
        window.runtime = { EventsOn() {} };
        window.__noxa.$ = id => document.getElementById(id);
        window.__noxa.toast = () => {};
        Object.assign(window.__noxa.state, { myClientID: "self", clients: [{ client_id: "bob-session", unique_id: "bob", nickname: "Bob", channel_id: 2 }], settings: {} });
        document.body.insertAdjacentHTML("beforeend", '<button id="channel-create-btn">New channel</button><div id="channel-tree"><div tabindex="0" class="client" data-clid="bob-session">Bob session</div></div>');
        const { initClientInfo } = await import("/src/clientinfo.js");
        initClientInfo();
    });
}

test("member context menu assigns a role to the preselected target using the keyboard", async ({ page }) => {
    await installMemberContextMenu(page);
    const target = page.getByText("Bob session", { exact: true });
    await target.focus(); await page.keyboard.press("Shift+F10");
    const assign = page.getByRole("menuitem", { name: "Assign roles…", exact: true });
    await expect(assign).toBeVisible();
    await page.keyboard.press("ArrowDown"); await page.keyboard.press("ArrowDown");
    await expect(assign).toBeFocused();
    await page.keyboard.press("Enter");
    const dialog = page.getByRole("dialog", { name: "Members", exact: true });
    await expect(dialog.getByRole("searchbox")).toHaveValue("bob");
    await expect(dialog.getByRole("checkbox", { name: /^Bob\b/ })).toBeChecked();
    await dialog.getByRole("combobox").selectOption("30");
    await dialog.getByRole("button", { name: "Add role", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__roleCalls.at(-1))).toMatchObject({ kind: "member_roles_set", user_id: 3, role_ids: [20, 30] });
});

for (const reason of ["actor", "target", "server switch"]) test(`member context menu does not offer assignment after ${reason} restriction`, async ({ page }) => {
    await installMemberContextMenu(page);
    await page.evaluate(reason => {
        if (reason === "actor") window.__roleState.manageable_role_ids = [];
        if (reason === "target") window.__roleMembers.find(m => m.unique_id === "bob").manageable = false;
        if (reason === "server switch") window.__membersGate = new Promise(resolve => { window.__finishMemberMenu = resolve; });
    }, reason);
    await page.getByText("Bob session", { exact: true }).click({ button: "right" });
    if (reason === "server switch") {
        await expect.poll(() => page.evaluate(() => window.__rosterCalls?.length || 0)).toBe(1);
        await page.evaluate(() => { window.__noxa.state.serverGeneration++; window.__finishMemberMenu(); });
    } else await expect.poll(() => page.evaluate(() => window.__tabCalls.length)).toBeGreaterThan(0);
    await expect(page.getByRole("menuitem", { name: "Assign roles…", exact: true })).toHaveCount(0);
    expect(await page.evaluate(() => window.__roleCalls)).toEqual([]);
});

test.describe("draft role and member impact", () => {
    test.beforeEach(async ({ page }) => {
        await page.getByRole("button", { name: "Channel access", exact: true }).click();
        await page.getByRole("button", { name: "Private", exact: true }).click();
    });
    test("shows roles without enumerating members, then invalidates on edit", async ({ page }) => {
        const save = page.getByRole("button", { name: "Save", exact: true });
        await expect(save).toBeDisabled();
        await page.getByRole("button", { name: "Preview role changes", exact: true }).click();
        await expect(save).toBeEnabled();
        const rows = page.locator(".channel-impact-member");
        await expect(rows).toHaveCount(3);
        await expect(rows.first()).toContainText("@everyone");
        expect(await page.evaluate(() => window.__tabCalls.some(c => c.name === "RoleMembers"))).toBe(false);
        await expect(rows.first()).toContainText("Allowed → Denied");
        expect(await page.evaluate(() => window.__impactCalls[0].user_ids)).toEqual([0]);
        expect(await page.evaluate(() => window.__roleCalls)).toEqual([]);
        await page.getByRole("combobox", { name: "View channel", exact: true }).selectOption("allow");
        await expect(rows).toHaveCount(0); await expect(save).toBeDisabled();
    });
    test("an earlier draft reply cannot authorize saving the edited draft", async ({ page }) => {
        await page.evaluate(() => { window.__impactGate = new Promise(resolve => { window.__finishImpact = resolve; }); });
        await page.getByRole("button", { name: "Preview role changes", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__impactCalls?.length)).toBe(1);
        await page.getByRole("combobox", { name: "View channel", exact: true }).selectOption("allow");
        await page.evaluate(() => { window.__finishImpact(); window.__impactGate = null; });
        await expect(page.locator(".channel-impact-member")).toHaveCount(0);
        await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
        await page.getByRole("button", { name: "Preview role changes", exact: true }).click();
        await expect(page.getByRole("button", { name: "Save", exact: true })).toBeEnabled();
        expect(await page.evaluate(() => window.__impactCalls[1].change.channel.overrides[0].effect)).toBe("allow");
    });
    test("member search includes unchanged permissions and clears stale matches", async ({ page }) => {
        const search = page.getByLabel("Find member permissions (name or unique ID)");
        const check = page.getByRole("button", { name: "Check member permissions", exact: true });
        await expect(check).toBeDisabled();
        await search.fill("Owner"); await search.press("Enter");
        await expect(page.locator(".channel-impact-member").filter({ hasText: "Owner" })).toContainText("Allowed → Allowed");
        expect(await page.evaluate(() => window.__impactCalls.at(-1).user_ids)).toEqual([1]);
        await search.fill("Alice");
        await expect(page.locator(".channel-impact-member").filter({ hasText: "Owner" })).toHaveCount(0);
        await check.click();
        await expect(page.locator(".channel-impact-member").filter({ hasText: "Alice" })).toContainText("Allowed → Denied");
        expect(await page.evaluate(() => window.__rosterCalls.at(-1).search)).toBe("Alice");
        expect(await page.evaluate(() => window.__roleCalls)).toEqual([]);
        await search.fill("Nobody matches"); await check.click();
        await expect(page.locator(".channel-impact-member")).toHaveCount(3);
        await expect(page.locator(".channel-impact")).toContainText("No matching members");
    });
    test("native activation between roster and preview rejects the old draft", async ({ page }) => {
        await page.evaluate(() => { window.__membersGate = new Promise(resolve => { window.__finishMembers = resolve; }); });
        await page.getByLabel("Find member permissions (name or unique ID)").fill("Alice");
        await page.getByRole("button", { name: "Check member permissions", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__tabCalls.at(-1)?.name)).toBe("RoleMembers");
        await page.evaluate(() => { window.__nativeTabID = "server-b"; window.__finishMembers(); });
        await expect(page.locator(".channel-impact")).toContainText("The draft could not be checked");
        await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
        expect(await page.evaluate(() => window.__impactCalls)).toBeUndefined();
    });
    test("server reset during roster loading stops the preview request", async ({ page }) => {
        await page.evaluate(() => { window.__membersGate = new Promise(resolve => { window.__finishMembers = resolve; }); });
        await page.getByLabel("Find member permissions (name or unique ID)").fill("Alice");
        await page.getByRole("button", { name: "Check member permissions", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__tabCalls.at(-1)?.name)).toBe("RoleMembers");
        await page.evaluate(async () => {
            window.__noxa.state.serverGeneration++;
            (await import("/src/modal.js")).closeServerDialogs();
            window.__finishMembers();
        });
        await expect(page.getByRole("dialog")).toHaveCount(0);
        expect(await page.evaluate(() => window.__impactCalls)).toBeUndefined();
    });
    test("bounded pages explicitly disclose remaining members", async ({ page }) => {
        await page.evaluate(() => { window.__roleMembers = Array.from({ length: 103 }, (_, i) => ({ user_id: i + 1, nickname: `Member ${i + 1}`, unique_id: `uid-${i + 1}` })); });
        await page.getByLabel("Find member permissions (name or unique ID)").fill("Member");
        await page.getByRole("button", { name: "Check member permissions", exact: true }).click();
        await expect(page.locator(".channel-impact-member")).toHaveCount(103);
        await expect(page.locator(".channel-impact")).toContainText("not a complete server impact report");
        await page.getByRole("button", { name: "Next members", exact: true }).click();
        await expect(page.locator(".channel-impact-member")).toHaveCount(6);
        await expect(page.locator(".channel-impact-member").first()).toContainText("Allowed → Denied");
        await expect(page.locator(".channel-impact-member").nth(3)).toContainText("Member 101");
        await expect(page.getByRole("button", { name: "Next members", exact: true })).toBeHidden();
        expect(await page.evaluate(() => window.__impactCalls.map(c => c.user_ids.length))).toEqual([100, 3]);
    });
    test("mismatched revision cannot enable saving", async ({ page }) => {
        await page.evaluate(() => { window.__impactResponse = { revision: 2, channel_id: 2, members: [] }; });
        await page.getByRole("button", { name: "Preview role changes", exact: true }).click();
        await expect(page.locator(".channel-impact")).toContainText("The draft could not be checked");
        await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
        expect(await page.evaluate(() => window.__roleCalls)).toEqual([]);
    });
});

test("German draft impact fits a compact dialog and supports keyboard review", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 800 });
    await page.evaluate(() => { window.setTestLanguage("de"); window.__roleState.impact_channel_ids = [4]; });
    await page.getByRole("button", { name: "Channel access", exact: true }).click();
    await page.getByRole("button", { name: "Privat", exact: true }).click();
    const scope = page.getByRole("combobox", { name: "Kanal für die Vorschau", exact: true });
    await scope.focus(); await page.keyboard.press("ArrowDown"); await page.keyboard.press("Enter");
    await expect(scope).toHaveValue("4");
    const preview = page.getByRole("button", { name: "Rollenänderungen prüfen", exact: true });
    await preview.focus(); await page.keyboard.press("Enter");
    await expect(page.locator(".channel-impact-members").first()).toContainText("Erlaubt → Verweigert");
    await expect(preview).toBeFocused();
    await expect(page.getByRole("button", { name: "Speichern", exact: true })).toBeEnabled();
    expect(await page.evaluate(() => window.__impactCalls.at(-1).scope_channel_id)).toBe(4);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.locator(".channel-impact").scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath("channel-impact-de.png"), animations: "disabled" });
});
