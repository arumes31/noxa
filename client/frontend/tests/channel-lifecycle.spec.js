import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";

const presetContract = JSON.parse(readFileSync(new URL("../../../testdata/channel-access-presets.json", import.meta.url), "utf8"));

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__noxa = { state: { serverGeneration: 1, authorizationModel: "roles-v1" } };
        window.__channelCalls = [];
        window.__channelState = { revision: 7, channel_id: 1, name: "Project", affected_channels: 3,
            capabilities: [{ key: "view_channel", en: "View channel", de: "Kanal sehen", channel: true }],
            can_create_permanent: true, can_create_temporary: true, can_manage_access: true, everyone_id: 10,
            roles: [{ id: 20, name: "Team" }], destinations: [{ id: 0, name: "", can_sync: true }, { id: 4, name: "Support", can_sync: false }],
            grantable_capabilities: ["view_channel", "read_history", "send_messages", "connect", "speak", "share_camera", "share_screen"],
            settings: { name: "Project", topic: "Old topic", description: "", max_clients: 0, slow_mode_seconds: 0, order_index: 0,
                opus_bitrate: 32000, opus_fec: true, opus_dtx: false, opus_stereo: false } };
        window.go = { main: { App: {
            RoleMembers: async query => {
                window.__rosterCalls ||= []; window.__rosterCalls.push(query);
                return { revision: 7, more: false, entries: [{ user_id: 2, nickname: "Member", unique_id: "member" }] };
            },
            PreviewChannelAccess: async query => {
                window.__impactCalls ||= []; window.__impactCalls.push(structuredClone(query));
                if (window.__impactGate) await window.__impactGate;
                if (window.__impactFailure) throw new Error("preview failed");
                return { revision: 7, channel_id: query.scope_channel_id || query.tree.channel_id, members: query.user_ids.map(user_id => ({ user_id,
                    changes: [{ capability: "view_channel", before: query.tree.kind === "channel_move", after: query.tree.kind === "channel_create" }] })) };
            },
            RoleChannelState: async ({ kind }) => {
                const result = structuredClone(window.__channelState);
                if (kind === "channel_create") result.settings.name = "";
                return result;
            },
            ChangeRoleChannel: async (request) => {
                window.__channelCalls.push(request);
                if (window.__channelGate) await window.__channelGate;
                if (window.__channelFailure) throw new Error("stale revision");
                return { revision: 8, channel_id: 2, enforcement_pending: !!window.__channelPending };
            },
        } } };
        window.__noxa.state.activeTabID = "server-a";
        window.__noxa.state.channels = [{ ChannelID: 1, Name: "Project", HasIcon: true }, { ChannelID: 4, Name: "Support", HasIcon: true }];
        window.__noxa.toast = () => {};
        window.__iconCalls = [];
        window.__iconReads = [];
        window.go.main.App.ChannelIconGetForTab = async (tabID, channelID) => {
            window.__iconReads.push([tabID, channelID]);
            if (tabID !== window.__nativeTabID) throw new Error("server changed");
            if (window.__iconReadGate) await window.__iconReadGate;
            return { channel_id: channelID, content_type: "image/png", data_base64: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=" };
        };
        window.go.main.App.SetRoleChannelIconForTab = async (tabID, channelID, data, source) => {
            window.__iconCalls.push([tabID, channelID, data, source]);
            if (tabID !== window.__nativeTabID) throw new Error("server changed");
            if (window.__iconSaveGate) await window.__iconSaveGate;
            if (window.__iconFailure) throw new Error("write uncertain");
            return { channel_id: channelID };
        };
        window.__nativeTabID = "server-a";
        window.__tabCalls = [];
        const app = window.go.main.App;
        for (const name of ["RoleChannelState","ChangeRoleChannel","RoleMembers","PreviewChannelAccess"]) {
            const original = app[name];
            app[`${name}ForTab`] = async (tabID, request) => {
                window.__tabCalls.push({ name, tabID });
                if (tabID !== window.__nativeTabID) throw new Error("server tab changed; refresh");
                return original(request);
            };
            app[name] = () => { throw new Error("unscoped native call"); };
        }
    });
    await page.route("**/__channel_lifecycle_test__", (route) => route.fulfill({ contentType: "text/html", body: `<!doctype html><html lang="en"><head><meta name="viewport" content="width=device-width, initial-scale=1"><link rel="stylesheet" href="/src/style.css"></head><body>
        <button id="create">Create dialog</button><button id="edit">Edit dialog</button><button id="move">Move dialog</button><button id="delete">Delete dialog</button>
        <script type="module">
        import { openRoleChannel } from '/src/channel-lifecycle-ui.js';
        import { initModalSystem, closeServerDialogs } from '/src/modal.js';
        import { setLanguage } from '/src/i18n.js';
        initModalSystem(); window.language = setLanguage; window.closeServerDialogs = closeServerDialogs; window.openRoleChannel = openRoleChannel;
        for (const kind of ['create', 'edit', 'move', 'delete']) document.querySelector('#'+kind).onclick = () => openRoleChannel('channel_'+kind, 1);
        window.ready = true;
        </script></body></html>` }));
    await page.goto("/__channel_lifecycle_test__");
    await page.waitForFunction(() => window.ready);
});

test("create private channel waits for acknowledgement and sends ordinary overrides", async ({ page }) => {
    await page.getByText("Create dialog", { exact: true }).click();
    await page.getByLabel("Channel name", { exact: true }).fill("Staff");
    await page.getByRole("combobox", { name: "Channel access", exact: true }).selectOption("private");
    await page.getByLabel("Team", { exact: true }).check();
    await expect(page.getByRole("button", { name: "Create", exact: true })).toBeDisabled();
    await page.getByRole("button", { name: "Preview member changes", exact: true }).click();
    await expect(page.getByRole("button", { name: "Create", exact: true })).toBeEnabled();
    await page.evaluate(() => { window.__channelGate = new Promise((resolve) => { window.__channelFinish = resolve; }); });
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await expect(page.getByRole("button", { name: "Create", exact: true })).toBeDisabled();
    await expect(page.getByRole("dialog")).toBeVisible();
    expect(await page.evaluate(() => window.__channelCalls[0])).toMatchObject({ expected_revision: 7, parent_id: 1, channel_id: 0,
        settings: { name: "Staff" }, access: { synced: false, overrides: [{ role_id: 10, capability: "view_channel", effect: "deny" }, { role_id: 20, capability: "view_channel", effect: "allow" }] } });
    await page.evaluate(() => window.__channelFinish());
    await expect(page.getByRole("dialog")).toHaveCount(0);
});

for (const preset of presetContract.presets) test(`${preset.key} creation previews and saves the evaluator privacy contract`, async ({ page }) => {
    await page.getByText("Create dialog", { exact: true }).click();
    await page.getByLabel("Channel name", { exact: true }).fill("Preset fixture");
    await page.getByRole("combobox", { name: "Channel access", exact: true }).selectOption(preset.key);
    await expect(page.getByRole("button", { name: "Create", exact: true })).toBeDisabled();
    await page.getByRole("button", { name: "Preview member changes", exact: true }).click();
    await expect(page.getByRole("button", { name: "Create", exact: true })).toBeEnabled();
    expect(await page.evaluate(() => window.__impactCalls.at(-1).tree.access.overrides)).toEqual(preset.overrides);
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(await page.evaluate(() => window.__channelCalls[0].access)).toEqual({ synced: false, overrides: preset.overrides });
});

test("native tab activation cannot redirect a channel edit before frontend reset", async ({ page }) => {
    await page.getByText("Edit dialog", { exact: true }).click();
    await page.getByLabel("Topic", { exact: true }).fill("Old server topic");
    await page.evaluate(() => { window.__nativeTabID = "server-b"; });
    await page.getByRole("button", { name: "Save changes", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("draft is kept");
    await expect(page.getByLabel("Topic", { exact: true })).toHaveValue("Old server topic");
    expect(await page.evaluate(() => window.__channelCalls)).toEqual([]);
    expect(await page.evaluate(() => window.__tabCalls.at(-1))).toEqual({ name: "ChangeRoleChannel", tabID: "server-a" });
});

test("creation preview contains only policy and invalidates on role or lifetime changes", async ({ page }) => {
    await page.getByText("Create dialog", { exact: true }).click();
    await page.getByLabel("Channel name", { exact: true }).fill("Staff");
    await page.getByLabel("Password (empty = none)", { exact: true }).fill("secret");
    await page.getByRole("combobox", { name: "Channel access", exact: true }).selectOption("private");
    const preview = page.getByRole("button", { name: "Preview member changes", exact: true });
    const save = page.getByRole("button", { name: "Create", exact: true });
    await preview.click();
    await expect(save).toBeEnabled();
    await expect(page.getByRole("dialog")).toContainText("does not exist yet");
    expect(await page.evaluate(() => window.__impactCalls[0])).toEqual({ tree: { kind: "channel_create", expected_revision: 7,
        channel_id: 0, parent_id: 1, temporary: false, access: { channel_id: 0, parent_id: 1, synced: false,
            overrides: [{ role_id: 10, capability: "view_channel", effect: "deny" }] } }, user_ids: [0, 2] });
    expect(await page.evaluate(() => window.__rosterCalls[0].channel_id)).toBe(1);
    expect(await page.evaluate(() => window.__channelCalls)).toEqual([]);
    await page.getByLabel("Team", { exact: true }).check();
    await expect(save).toBeDisabled();
    await preview.click();
    await expect(save).toBeEnabled();
    await page.getByLabel("Channel lifetime").selectOption("0");
    await expect(save).toBeDisabled();
    await preview.click();
    await expect(save).toBeEnabled();
    expect(await page.evaluate(() => window.__impactCalls.at(-1).tree)).toMatchObject({ temporary: true,
        access: { overrides: [{ role_id: 10, capability: "view_channel", effect: "deny" }, { role_id: 20, capability: "view_channel", effect: "allow" }] } });
    await page.getByLabel("Channel name", { exact: true }).fill("Renamed staff");
    await expect(save).toBeEnabled();
});

test("late creation preview cannot authorize a changed policy draft", async ({ page }) => {
    await page.getByText("Create dialog", { exact: true }).click();
    await page.getByRole("combobox", { name: "Channel access", exact: true }).selectOption("private");
    await page.evaluate(() => { window.__impactGate = new Promise(resolve => { window.__finishImpact = resolve; }); });
    await page.getByRole("button", { name: "Preview member changes", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__impactCalls?.length)).toBe(1);
    await page.getByLabel("Team", { exact: true }).check();
    await page.evaluate(() => window.__finishImpact());
    await expect(page.getByRole("button", { name: "Create", exact: true })).toBeDisabled();
    await expect(page.locator(".channel-impact-member")).toHaveCount(0);
    await page.getByRole("button", { name: "Preview member changes", exact: true }).click();
    await expect(page.getByRole("button", { name: "Create", exact: true })).toBeEnabled();
});

test("move preview cannot survive a destination change or a failed retry", async ({ page }) => {
    await page.evaluate(() => { window.__channelState.destinations[1].can_sync = true; });
    await page.getByText("Move dialog", { exact: true }).click();
    const access = page.getByRole("combobox", { name: "Channel access", exact: true });
    const preview = page.getByRole("button", { name: "Preview member changes", exact: true });
    const save = page.getByRole("button", { name: "Move", exact: true });
    await access.selectOption("sync");
    await preview.click();
    await expect(save).toBeEnabled();
    await page.getByRole("combobox", { name: "Destination", exact: true }).selectOption("4");
    await access.selectOption("sync");
    await expect(save).toBeDisabled();
    await page.evaluate(() => { window.__impactFailure = true; });
    await preview.click();
    await expect(page.locator(".channel-impact [role=status]")).toContainText("could not");
    await expect(save).toBeDisabled();
    expect(await page.evaluate(() => window.__impactCalls.at(-1).tree)).toEqual({ kind: "channel_move", expected_revision: 7,
        channel_id: 1, parent_id: 4, sync_to_parent: true });
    expect(await page.evaluate(() => window.__channelCalls)).toEqual([]);
});

test("native activation prevents a pending tree preview from enabling save", async ({ page }) => {
    await page.getByText("Create dialog", { exact: true }).click();
    await page.getByRole("combobox", { name: "Channel access", exact: true }).selectOption("private");
    await page.evaluate(() => { window.__nativeTabID = "server-b"; });
    await page.getByRole("button", { name: "Preview member changes", exact: true }).click();
    await expect(page.locator(".channel-impact [role=status]")).toContainText("could not");
    await expect(page.getByRole("button", { name: "Create", exact: true })).toBeDisabled();
    expect(await page.evaluate(() => window.__impactCalls || [])).toEqual([]);
    expect(await page.evaluate(() => window.__tabCalls.at(-1))).toEqual({ name: "RoleMembers", tabID: "server-a" });
});

test("root creation previews the root roster and German grants with keyboard controls", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 800 });
    await page.evaluate(() => { window.language("de"); window.openRoleChannel("channel_create", 0); });
    const preview = page.getByRole("button", { name: "Mitgliedsänderungen prüfen", exact: true });
    await preview.focus();
    await page.keyboard.press("Enter");
    await expect(page.locator(".channel-impact-member")).toHaveCount(2);
    await expect(preview).toBeFocused();
    await expect(page.locator(".channel-impact")).toContainText("Dieser Kanal existiert noch nicht");
    await expect(page.locator(".channel-impact-member").first()).toContainText("Kanal sehen");
    expect(await page.evaluate(() => window.__rosterCalls[0].channel_id)).toBe(0);
    expect(await page.evaluate(() => window.__impactCalls[0].tree)).toMatchObject({ channel_id: 0, parent_id: 0,
        access: { channel_id: 0, parent_id: 0, synced: true } });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.locator(".channel-impact-member").last().scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath("channel-create-impact-de.png"), fullPage: true, animations: "disabled" });
});

test("temporary creator cannot choose permanent type or custom access", async ({ page }) => {
    await page.evaluate(() => { window.__channelState.can_create_permanent = false; window.__channelState.can_manage_access = false; });
    await page.getByText("Create dialog", { exact: true }).click();
    await expect(page.getByLabel("Channel lifetime")).toHaveValue("0");
    await expect(page.getByRole("option", { name: "Permanent", exact: true })).toHaveJSProperty("disabled", true);
    await expect(page.getByRole("option", { name: "Private", exact: true })).toHaveJSProperty("disabled", true);
    await page.getByLabel("Channel name", { exact: true }).fill("Quick chat");
    await page.getByRole("button", { name: "Create", exact: true }).click();
    expect(await page.evaluate(() => window.__channelCalls[0])).toMatchObject({ channel_type: 0 });
    expect(await page.evaluate(() => window.__channelCalls[0].access)).toBeUndefined();
});

test("move keeps access by default and sync requires confirmation", async ({ page }) => {
    await page.getByText("Move dialog", { exact: true }).click();
    await expect(page.getByRole("combobox", { name: "Channel access", exact: true })).toHaveValue("keep");
    await page.getByRole("button", { name: "Move", exact: true }).click();
    expect(await page.evaluate(() => window.__channelCalls[0])).toMatchObject({ kind: "channel_move", parent_id: 0, sync_to_parent: false });
    await page.getByText("Move dialog", { exact: true }).click();
    await page.getByRole("combobox", { name: "Destination", exact: true }).selectOption("4");
    await expect(page.getByRole("option", { name: "Sync with destination", exact: true })).toHaveJSProperty("disabled", true);
    await page.getByRole("combobox", { name: "Destination", exact: true }).selectOption("0");
    await page.getByRole("combobox", { name: "Channel access", exact: true }).selectOption("sync");
    await expect(page.getByRole("button", { name: "Move", exact: true })).toBeDisabled();
    await page.getByRole("button", { name: "Preview member changes", exact: true }).click();
    await page.getByRole("button", { name: "Move", exact: true }).click();
    await expect(page.getByRole("dialog").last()).toContainText("may change who can see");
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(await page.evaluate(() => window.__channelCalls.length)).toBe(1);
    await page.getByRole("button", { name: "Move", exact: true }).click();
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(await page.evaluate(() => window.__channelCalls[1].sync_to_parent)).toBe(true);
});

test("synced move previews descendant access without changing the moved source", async ({ page }) => {
    await page.evaluate(() => { window.__channelState.impact_channel_ids = [5]; });
    await page.getByText("Move dialog", { exact: true }).click();
    await page.getByRole("combobox", { name: "Channel access", exact: true }).selectOption("sync");
    await page.getByRole("combobox", { name: "Preview channel", exact: true }).selectOption("5");
    await page.getByRole("button", { name: "Preview member changes", exact: true }).click();
    await expect(page.getByRole("button", { name: "Move", exact: true })).toBeEnabled();
    expect(await page.evaluate(() => window.__impactCalls.at(-1))).toMatchObject({ scope_channel_id: 5,
        tree: { channel_id: 1, parent_id: 0, sync_to_parent: true } });
    expect(await page.evaluate(() => window.__rosterCalls.at(-1).channel_id)).toBe(5);
    await page.getByRole("button", { name: "Move", exact: true }).click();
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(await page.evaluate(() => window.__channelCalls[0])).toMatchObject({ channel_id: 1, parent_id: 0, sync_to_parent: true });
    expect(await page.evaluate(() => window.__channelCalls[0].scope_channel_id)).toBeUndefined();
});

test("generated models preserve preview scopes on state and query objects", async ({ page }) => {
    expect(await page.evaluate(async () => {
        const { netproto } = await import("/wailsjs/go/models.ts");
        const source = { impact_channel_ids: [4, 5] };
        return {
            lifecycle: new netproto.RoleChannelState(source).impact_channel_ids,
            access: new netproto.RoleState(source).impact_channel_ids,
            query: new netproto.ChannelAccessPreview({ scope_channel_id: 4 }).scope_channel_id,
            mutationHasScope: Object.hasOwn(new netproto.RoleChannelChange(source), "impact_channel_ids"),
        };
    })).toEqual({ lifecycle: [4, 5], access: [4, 5], query: 4, mutationHasScope: false });
});

test("failed edit preserves draft and requires explicit refresh", async ({ page }) => {
    await page.evaluate(() => { window.__channelFailure = true; });
    await page.getByText("Edit dialog", { exact: true }).click();
    await page.getByLabel("Topic", { exact: true }).fill("Unsaved topic");
    await page.getByRole("button", { name: "Save changes", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("draft is kept");
    await expect(page.getByLabel("Topic", { exact: true })).toHaveValue("Unsaved topic");
    await expect(page.getByRole("button", { name: "Save changes", exact: true })).toBeDisabled();
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(page.getByLabel("Topic", { exact: true })).toHaveValue("Unsaved topic");
});

test("invalid numeric settings are shown before any request", async ({ page }) => {
    await page.getByText("Edit dialog", { exact: true }).click();
    await page.getByText("Limits and audio", { exact: true }).click();
    await page.getByLabel("Participant limit (0 = unlimited)", { exact: true }).fill("-1");
    await page.getByRole("button", { name: "Save changes", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("numeric limits");
    expect(await page.evaluate(() => window.__channelCalls)).toHaveLength(0);
});

for (const [label, invalid, valid] of [
    ["Channel name", "é".repeat(128), "é".repeat(127)],
    ["Password (empty = none)", "é".repeat(2049), "é".repeat(2048)],
]) test(`Unicode byte limit for ${label} can be corrected without refreshing`, async ({ page }) => {
    await page.getByText("Create dialog", { exact: true }).click();
    await page.getByLabel("Channel name", { exact: true }).fill("Team");
    await page.getByLabel(label, { exact: true }).fill(invalid);
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("too long");
    expect(await page.evaluate(() => window.__channelCalls)).toHaveLength(0);
    await page.getByLabel(label, { exact: true }).fill(valid);
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(await page.evaluate(() => window.__channelCalls)).toHaveLength(1);
});

test("access selectors retain focus when dependent controls change", async ({ page }) => {
    await page.getByText("Create dialog", { exact: true }).click();
    const access = page.getByRole("combobox", { name: "Channel access", exact: true });
    await access.focus();
    await access.selectOption("private");
    await expect(access).toBeFocused();
    await page.keyboard.press("Tab");
    await expect(page.getByLabel("Team", { exact: true })).toBeFocused();
    await page.evaluate(() => window.closeServerDialogs());
    await page.getByText("Move dialog", { exact: true }).click();
    const destination = page.getByRole("combobox", { name: "Destination", exact: true });
    await destination.focus();
    await destination.selectOption("4");
    await expect(destination).toBeFocused();
    await destination.selectOption("0");
    await page.keyboard.press("Tab");
    await expect(access).toBeFocused();
    await access.selectOption("sync");
    await expect(access).toBeFocused();
});

test("switching servers dismisses menus holding the previous server's channel IDs", async ({ page }) => {
    await page.evaluate(async () => {
        const listeners = {};
        window.runtime = { EventsOn: (name, callback) => { listeners[name] = callback; } };
        window.__noxa.state.channels = [{ ChannelID: 1, Name: "Server A channel" }];
        document.body.insertAdjacentHTML("beforeend", '<button id="channel-create-btn">New channel</button><div id="channel-tree"><div class="channel" data-chid="1">Server A channel</div></div><button id="server-b">Server B</button>');
        const { initClientInfo } = await import("/src/clientinfo.js");
        initClientInfo();
        document.getElementById("server-b").onclick = (event) => {
            event.stopPropagation();
            window.__noxa.state.serverGeneration++;
            window.__noxa.state.channels = [{ ChannelID: 1, Name: "Server B channel" }];
            listeners.tab_reset?.();
        };
    });
    await page.getByText("Server A channel", { exact: true }).click({ button: "right" });
    await expect(page.locator(".ctx-menu")).toBeVisible();
    await page.getByRole("button", { name: "Server B", exact: true }).click();
    await expect(page.locator(".ctx-menu")).toHaveCount(0);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(await page.evaluate(() => window.__channelCalls)).toHaveLength(0);
});

test("committed pending result cannot be submitted again", async ({ page }) => {
    await page.evaluate(() => { window.__channelPending = true; });
    await page.getByText("Delete dialog", { exact: true }).click();
    await expect(page.getByRole("dialog")).toContainText("3 channels in total");
    await page.getByRole("button", { name: "Delete", exact: true }).click();
    await expect(page.getByRole("status")).toContainText("change was saved");
    await expect(page.getByRole("button", { name: "Delete", exact: true })).toBeDisabled();
    await expect(page.getByRole("button", { name: "Refresh", exact: true })).toBeDisabled();
});

test("old server replies do not revive a dialog or issue follow-up requests", async ({ page }) => {
    await page.evaluate(() => { window.__channelGate = new Promise((resolve) => { window.__channelFinish = resolve; }); });
    await page.getByText("Edit dialog", { exact: true }).click();
    await page.getByRole("button", { name: "Save changes", exact: true }).click();
    await page.evaluate(() => { window.__noxa.state.serverGeneration++; window.closeServerDialogs(); window.__channelFinish(); });
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(await page.evaluate(() => window.__channelCalls.length)).toBe(1);
});

test.describe("acknowledged channel icon editor", () => {
    async function open(page) {
        await page.getByText("Edit dialog", { exact: true }).click();
        await page.getByLabel("Topic", { exact: true }).fill("Unsaved settings");
        await page.getByRole("button", { name: "Channel icon", exact: true }).click();
        const dialog = page.getByRole("dialog", { name: "Channel icon", exact: true });
        await expect(dialog.getByRole("img", { name: "Channel icon preview" })).toBeVisible();
        return dialog;
    }
    async function reuse(dialog) {
        await dialog.getByRole("combobox", { name: "Reuse a channel icon", exact: true }).selectOption("4");
        await expect(dialog.getByRole("button", { name: "Save icon", exact: true })).toBeEnabled();
    }
    test("reuses a visible icon after acknowledgement without saving the parent draft", async ({ page }) => {
        const dialog = await open(page);
        await reuse(dialog);
        await page.evaluate(() => { window.__iconSaveGate = new Promise(resolve => { window.__finishIconSave = resolve; }); });
        await dialog.getByRole("button", { name: "Save icon", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__iconCalls)).toEqual([["server-a", 1, "", 4]]);
        await expect(dialog.getByRole("button", { name: "Save icon", exact: true })).toBeDisabled();
        await page.keyboard.press("Escape");
        await expect(dialog).toBeVisible();
        await page.evaluate(() => window.__finishIconSave());
        await expect(dialog).toHaveCount(0);
        await expect(page.getByLabel("Topic", { exact: true })).toHaveValue("Unsaved settings");
        expect(await page.evaluate(() => window.__channelCalls)).toEqual([]);
        await expect(page.getByRole("button", { name: "Channel icon", exact: true })).toBeFocused();
    });
    test("previews a compressed upload and writes only after Save icon", async ({ page }) => {
        const dialog = await open(page);
        await dialog.getByLabel("Choose an image", { exact: true }).setInputFiles({ name: "icon.png", mimeType: "image/png", buffer: Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=", "base64") });
        await expect(dialog.getByRole("button", { name: "Save icon", exact: true })).toBeEnabled();
        expect(await page.evaluate(() => window.__iconCalls)).toEqual([]);
        await expect(dialog.getByRole("img")).toHaveAttribute("src", /^data:image\/jpeg;base64,/);
        await dialog.getByRole("button", { name: "Save icon", exact: true }).click();
        await expect(dialog).toHaveCount(0);
        const calls = await page.evaluate(() => window.__iconCalls);
        expect(calls).toHaveLength(1);
        expect(calls[0][0]).toBe("server-a"); expect(calls[0][1]).toBe(1); expect(calls[0][2].length).toBeGreaterThan(0); expect(calls[0][3]).toBe(0);
    });
    test("uncertain save requires explicit refresh and leaves parent settings alone", async ({ page }) => {
        const dialog = await open(page);
        await reuse(dialog);
        await page.evaluate(() => { window.__iconFailure = true; });
        await dialog.getByRole("button", { name: "Save icon", exact: true }).click();
        await expect(dialog.getByRole("alert")).toContainText("Refresh before trying again");
        await expect(dialog.getByRole("button", { name: "Save icon", exact: true })).toBeDisabled();
        await dialog.getByRole("button", { name: "Refresh", exact: true }).click();
        await page.getByRole("dialog").last().getByRole("button", { name: "Apply", exact: true }).click();
        await expect(dialog.locator(".role-error")).toBeEmpty();
        await expect(dialog.getByRole("button", { name: "Save icon", exact: true })).toBeDisabled();
        expect(await page.evaluate(() => window.__channelCalls)).toEqual([]);
    });
    test("native tab activation cannot redirect an icon save", async ({ page }) => {
        const dialog = await open(page);
        await reuse(dialog);
        await page.evaluate(() => { window.__nativeTabID = "server-b"; });
        await dialog.getByRole("button", { name: "Save icon", exact: true }).click();
        await expect(dialog.getByRole("alert")).toContainText("Refresh before trying again");
        expect(await page.evaluate(() => window.__iconCalls)).toEqual([["server-a", 1, "", 4]]);
    });
    test("late source preview cannot revive a closed server dialog", async ({ page }) => {
        const dialog = await open(page);
        await page.evaluate(() => { window.__iconReadGate = new Promise(resolve => { window.__finishIconRead = resolve; }); });
        await dialog.getByRole("combobox", { name: "Reuse a channel icon", exact: true }).selectOption("4");
        await expect.poll(() => page.evaluate(() => window.__iconReads.length)).toBe(2);
        await page.evaluate(() => { window.__noxa.state.serverGeneration++; window.closeServerDialogs(); window.__finishIconRead(); });
        await expect(page.getByRole("dialog")).toHaveCount(0);
        expect(await page.evaluate(() => window.__iconCalls)).toEqual([]);
    });
    test("late animated image preparation cannot write into a replacement server", async ({ page }) => {
        const dialog = await open(page);
        await page.evaluate(() => {
            const original = File.prototype.arrayBuffer;
            window.__imageGate = new Promise(resolve => { window.__finishImage = resolve; });
            File.prototype.arrayBuffer = async function() { await window.__imageGate; const result = await original.call(this); window.__imageReadDone = true; return result; };
        });
        await dialog.getByLabel("Choose an image", { exact: true }).setInputFiles({ name: "icon.gif", mimeType: "image/gif", buffer: Buffer.from("R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==", "base64") });
        await expect(dialog.getByLabel("Choose an image", { exact: true })).toBeDisabled();
        await page.evaluate(() => { window.__noxa.state.serverGeneration++; window.closeServerDialogs(); window.__finishImage(); });
        await expect.poll(() => page.evaluate(() => window.__imageReadDone)).toBe(true);
        await expect(page.getByRole("dialog")).toHaveCount(0);
        expect(await page.evaluate(() => window.__iconCalls)).toEqual([]);
    });
    test("German icon controls fit a compact window and preserve keyboard focus", async ({ page }, testInfo) => {
        await page.setViewportSize({ width: 390, height: 740 });
        await page.evaluate(() => window.language("de"));
        await page.getByText("Edit dialog", { exact: true }).click();
        await page.getByRole("button", { name: "Kanalsymbol", exact: true }).click();
        const dialog = page.getByRole("dialog", { name: "Kanalsymbol", exact: true });
        await expect(dialog.getByRole("img")).toBeVisible();
        await page.screenshot({ path: testInfo.outputPath("channel-icon-de.png"), fullPage: true, animations: "disabled" });
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        await page.keyboard.press("Escape");
        await expect(dialog).toHaveCount(0);
        await expect(page.getByRole("button", { name: "Kanalsymbol", exact: true })).toBeFocused();
    });
});

test("compact German editor remains keyboard accessible", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 740 });
    await page.evaluate(() => window.language("de"));
    await page.getByText("Edit dialog", { exact: true }).click();
    await page.getByLabel("Kanalname", { exact: true }).fill("Projekt");
    await page.getByLabel("Thema", { exact: true }).fill("Neues Thema");
    await page.screenshot({ path: testInfo.outputPath("channel-editor.png"), fullPage: true });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.getByRole("button", { name: "Änderungen speichern", exact: true }).focus();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(await page.evaluate(() => window.__channelCalls[0].settings.topic)).toBe("Neues Thema");
});
