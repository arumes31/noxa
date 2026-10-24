import { devices, expect, test } from "@playwright/test";

import { Msg } from "../src/core/frame.js";
import { desktopSession, guestSession } from "./harness.mjs";
import { linkPhone, openServerToMembers, registerDesktop } from "./helpers.mjs";

// The owner's phone administers the server; members' desktop sessions see
// the effects. The steps build on each other, so they share one phone.
const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
const PNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==", "base64");

test.describe.configure({ mode: "serial" });

let info, owner, context, page;
const hall = { id: 0 };

/** Keeps a desktop session's latest tree snapshot. */
function track(desktop) {
    desktop.snapshot = null;
    desktop.conn.on(Msg.Snapshot, snapshot => { desktop.snapshot = snapshot; });
    return desktop;
}

async function member(name) {
    return track(await desktopSession(server.controlAddr, server.accounts[name], { version: info.version }));
}

/** Puts a desktop session into a voice channel so the phone's member list shows it. */
async function joinVoiceRoom(desktop) {
    await expect.poll(() => !!desktop.snapshot).toBe(true);
    const room = channelsOf(desktop.snapshot).find(c => c.SystemKind === "public" && !c.TextOnly);
    await desktop.conn.request(Msg.JoinChannel, { ack_requested: true, channel_id: room.ChannelID }, Msg.ChannelJoined);
    return desktop;
}

function channelsOf(snapshot) {
    const out = [];
    const walk = nodes => { for (const node of nodes || []) { out.push(node); walk(node.children); } };
    walk(snapshot?.root_channels);
    return out;
}

function clientsOf(snapshot) {
    return [...channelsOf(snapshot).flatMap(c => c.clients || []), ...(snapshot?.unassigned_clients || [])];
}

const closed = desktop => new Promise(resolve => desktop.conn.addEventListener("close", resolve, { once: true }));
const go = hash => page.evaluate(value => { location.hash = value; }, hash);
const dialog = () => page.getByRole("dialog").last();

async function openMemberSheet(nickname) {
    await go("#/servers");
    const menu = page.getByRole("button", { name: `Actions for ${nickname}` });
    await expect(menu).toBeVisible({ timeout: 15000 });
    await menu.click();
    await expect(dialog()).toBeVisible();
}

async function createChannel(name, { topic = "" } = {}) {
    await go("#/admin/channel-new/0");
    await page.getByLabel("Name", { exact: true }).fill(name);
    if (topic) await page.getByLabel("Topic", { exact: true }).fill(topic);
    await page.getByRole("button", { name: "Create channel" }).click();
    await expect(page).toHaveURL(/#\/admin\/channel\/\d+$/, { timeout: 15000 });
    return Number(/channel\/(\d+)$/.exec(page.url())[1]);
}

test.beforeAll(async ({ browser }) => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
    owner = track(await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version }));
    await registerDesktop(owner);
    await openServerToMembers(owner);
    context = await browser.newContext({ ...devices["Pixel 7"] });
    page = await context.newPage();
    page.on("pageerror", error => console.log("pageerror:", error.message));
    await linkPhone(page, owner, server.webURL, expect);
    // The passkey offer and the install hint both say "Not now".
    await page.getByRole("dialog").getByRole("button", { name: "Not now" }).click({ timeout: 5000 }).catch(() => {});
    await page.getByRole("complementary").getByRole("button", { name: "Not now" }).click({ timeout: 2000 }).catch(() => {});
});

test.afterAll(async () => {
    await context?.close();
    // Free the owner's device slot for the other specs.
    const list = await owner.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" }).catch(() => ({ devices: [] }));
    for (const device of list.devices.filter(d => d.kind === "web" && !d.current)) {
        await owner.conn.request(Msg.DeviceRequest, { action: "revoke", device_id: device.device_id }, Msg.DeviceResult).catch(() => {});
    }
    owner?.conn.close();
});

test("the owner finds Administration on You", async () => {
    await page.getByRole("link", { name: "You", exact: true }).click();
    await page.getByRole("link", { name: /Administration/ }).click();
    await expect(page.getByRole("heading", { name: "Administration" })).toBeVisible();
    for (const name of ["Roles", "Ban list", "Audit log", "Custom emoji", "Server settings"]) {
        await expect(page.getByRole("link", { name: new RegExp(name) })).toBeVisible();
    }
});

test("kicking a member's session disconnects it", async () => {
    const kai = await joinVoiceRoom(await member("kai"));
    const gone = closed(kai);
    await openMemberSheet(server.accounts.kai.nickname);
    await dialog().getByRole("button", { name: "Remove from server…" }).click();
    await dialog().getByLabel("Reason (optional)").fill("testing kicks");
    await dialog().getByRole("button", { name: "Remove from server", exact: true }).click();
    await gone;
    await expect(page.getByText(`${server.accounts.kai.nickname} was removed from the server.`)).toBeVisible();
});

test("a ban signs the member out, shows in the ban list and can be lifted", async () => {
    const lou = await joinVoiceRoom(await member("lou"));
    const gone = closed(lou);
    await openMemberSheet(server.accounts.lou.nickname);
    await dialog().getByRole("button", { name: "Ban…" }).click();
    await dialog().getByRole("radio", { name: "1 hour" }).click();
    await dialog().getByLabel("Reason (optional)").fill("spam links");
    await dialog().getByRole("button", { name: "Ban", exact: true }).click();
    await gone;
    await expect(desktopSession(server.controlAddr, server.accounts.lou, { version: info.version })).rejects.toThrow();

    await go("#/admin/bans");
    const card = page.locator(".admin-ban").filter({ hasText: server.accounts.lou.uid });
    await expect(card).toContainText("spam links");
    await expect(card).toContainText("Expires");
    await card.getByRole("button", { name: /Lift ban/ }).click();
    await dialog().getByRole("button", { name: "Lift ban" }).click();
    await expect(card).toHaveCount(0);
    const back = await member("lou");
    expect(back.uniqueID).toBe(server.accounts.lou.uid);
    back.conn.close();
});

test("channels created, edited and deleted on the phone reach a desktop", async () => {
    const rowan = await member("rowan");
    const id = await createChannel("e2e-room", { topic: "first topic" });
    await expect.poll(() => channelsOf(rowan.snapshot).find(c => c.ChannelID === id)?.Name, { timeout: 15000 }).toBe("e2e-room");

    await page.getByRole("link", { name: /Edit channel/ }).click();
    await page.getByLabel("Topic", { exact: true }).fill("edited topic");
    await page.getByText("Limits and voice quality").click();
    await page.getByLabel("Slow mode, seconds between messages").fill("5");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect.poll(() => channelsOf(rowan.snapshot).find(c => c.ChannelID === id)?.Topic, { timeout: 15000 }).toBe("edited topic");
    expect(channelsOf(rowan.snapshot).find(c => c.ChannelID === id).SlowModeSeconds).toBe(5);

    await page.getByRole("button", { name: "Delete channel…" }).click();
    await dialog().getByRole("button", { name: "Delete" }).click();
    await expect.poll(() => channelsOf(rowan.snapshot).some(c => c.ChannelID === id), { timeout: 15000 }).toBe(false);
    rowan.conn.close();
});

test("a role is created, edited and assigned; the member's desktop sees it", async () => {
    await go("#/admin/roles");
    await page.getByRole("button", { name: "New role" }).click();
    await dialog().getByRole("button", { name: "Member" }).click();
    await page.getByLabel("Name", { exact: true }).fill("Testers");
    await page.getByRole("switch", { name: "Show separately in the member list" }).check();
    await page.getByRole("button", { name: "Create role" }).click();
    await expect(page).toHaveURL(/#\/admin\/role\/\d+$/);
    await expect(page.getByRole("switch", { name: "Send messages" })).toBeChecked();

    // Edit: allow mentions and add a permission found by search.
    await page.getByRole("switch", { name: "Anyone can @mention this role" }).check();
    await page.getByRole("searchbox", { name: "Search permissions" }).fill("poke");
    await page.getByRole("switch", { name: "Poke members" }).check();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByText("Saved", { exact: true })).toBeVisible();
    const state = await owner.conn.request(Msg.RoleQuery, { channel_id: 0 }, Msg.RoleState);
    const role = state.policy.roles.find(r => r.name === "Testers");
    expect(role.mentionable).toBe(true);
    expect(role.hoist).toBe(true);
    expect(role.permissions).toContain("poke_members");

    const kai = await member("kai");
    await go("#/admin/members");
    await page.getByLabel("Search members").fill(server.accounts.kai.nickname);
    await page.getByRole("button", { name: "Search members" }).click();
    await page.getByRole("button", { name: new RegExp(server.accounts.kai.nickname) }).click();
    await dialog().getByRole("switch", { name: "Testers" }).check();
    await dialog().getByRole("button", { name: "Save" }).click();
    await expect(page.getByRole("button", { name: new RegExp(server.accounts.kai.nickname) })).toContainText("Testers");
    await expect.poll(() => (clientsOf(kai.snapshot).find(c => c.unique_id === kai.uniqueID)?.roles || []).map(r => r.name), { timeout: 15000 }).toContain("Testers");
    kai.conn.close();
});

test("a channel access preset is saved after preview and verified by an access check", async () => {
    hall.id = await createChannel("e2e-hall");
    await page.getByRole("link", { name: /Access and permissions/ }).click();
    await page.getByRole("button", { name: "Read-only" }).click();
    await expect(page.getByRole("button", { name: "Save access" })).toBeDisabled();
    await page.getByRole("button", { name: "Preview changes" }).click();
    await expect(page.locator(".admin-impact")).toContainText("Send messages");
    await page.getByRole("button", { name: "Save access" }).click();
    await expect(page.getByText("Access saved")).toBeVisible();
    await expect(page.getByRole("button", { name: "Read-only" })).toHaveAttribute("aria-pressed", "true");
    // The densest admin screen still fits a narrow phone without sideways scrolling.
    await page.setViewportSize({ width: 360, height: 780 });
    expect(await page.evaluate(() => { const body = document.querySelector(".admin-body"); return body.scrollWidth <= body.clientWidth; })).toBe(true);

    const check = page.getByRole("region", { name: "Check access for a member" });
    await check.getByLabel("Permission").selectOption("send_messages");
    await check.getByRole("button", { name: "Check" }).click();
    await expect(check.locator(".admin-result")).toContainText("denied");
    await check.getByLabel("Permission").selectOption("read_history");
    await check.getByRole("button", { name: "Check" }).click();
    await expect(check.locator(".admin-result")).toContainText("allowed");
});

test("an invite link is created on the phone and redeemed by a guest", async () => {
    await go(`#/admin/invites/${hall.id}`);
    await page.getByRole("button", { name: "Create link" }).click();
    const link = await page.getByLabel("Invite link").inputValue();
    const [, channel, token] = /#\/invite\/(\d+)\/([A-Za-z0-9_-]{43})$/.exec(link);
    expect(Number(channel)).toBe(hall.id);
    const guest = await guestSession(server.controlAddr, { version: info.version, nickname: "Invited guest" });
    const result = await guest.conn.request(Msg.ChannelInviteRequest, { action: "resolve", channel_id: hall.id, token }, Msg.ChannelInviteResult);
    expect(result.active).toBe(true);
    // A wrong token is refused.
    await expect(guest.conn.request(Msg.ChannelInviteRequest, { action: "resolve", channel_id: hall.id, token: `${token.slice(0, 42)}${token.at(-1) === "A" ? "B" : "A"}` }, Msg.ChannelInviteResult)).rejects.toThrow();
    guest.conn.close();

    await page.getByRole("button", { name: "Revoke link" }).click();
    await dialog().getByRole("button", { name: "Revoke link" }).click();
    await expect(page.getByText("No invite link yet.")).toBeVisible();
});

test("a webhook is created with a one-time token and listed", async () => {
    await go(`#/admin/webhooks/${hall.id}`);
    await expect(page.getByText("No webhooks yet.")).toBeVisible();
    await page.getByLabel("Name").fill("CI bot");
    await page.getByRole("button", { name: "Create webhook" }).click();
    await expect(page.getByRole("alert")).toContainText("shown only once");
    expect(await page.getByLabel("Bearer token").inputValue()).toMatch(/^[A-Za-z0-9_-]{43}$/);
    await expect(page.locator(".list .row").filter({ hasText: "CI bot" })).toBeVisible();
    const hooks = await owner.conn.request(Msg.WebhookRequest, { action: "list", channel_id: hall.id }, Msg.WebhookResult, { accept: r => r.action === "list" });
    expect(hooks.hooks.map(h => h.name)).toEqual(["CI bot"]);
});

test("a custom emoji uploaded on the phone is listed for desktops", async () => {
    await go("#/admin/emoji");
    const chooser = page.waitForEvent("filechooser");
    await page.getByRole("button", { name: "Upload emoji" }).click();
    await (await chooser).setFiles({ name: "Party Time.png", mimeType: "image/png", buffer: PNG });
    await expect(dialog().getByLabel("Shortcode")).toHaveValue("party_time");
    await dialog().getByRole("button", { name: "Upload emoji" }).click();
    await expect(page.getByText(":party_time:", { exact: true })).toBeVisible();
    const rowan = await member("rowan");
    const list = await rowan.conn.request(Msg.EmojiList, {}, Msg.EmojiListResponse);
    expect(list.emojis.map(e => e.name)).toContain("party_time");
    rowan.conn.close();
});

test("the audit log lists the actions and opens their details", async () => {
    await go("#/admin/audit");
    const log = page.locator(".admin-body > .list");
    for (const action of ["kick", "ban_remove", "channel_invite_regenerate"]) await expect(log).toContainText(action, { timeout: 15000 });
    await page.getByText("Filter", { exact: true }).click();
    await page.getByLabel("Action", { exact: true }).fill("ban_remove");
    await expect(log.getByRole("button")).toHaveCount(1);
    await log.getByRole("button").first().click();
    await expect(dialog()).toContainText("ban_remove");
    await dialog().getByRole("button", { name: "Close" }).click();
});

test("a complaint filed from the member sheet is reviewed and cleared", async () => {
    const ivy = await joinVoiceRoom(await member("ivy"));
    await openMemberSheet(server.accounts.ivy.nickname);
    await dialog().getByRole("button", { name: "Report member…" }).click();
    await dialog().getByLabel("What happened?").fill("rude in voice");
    await dialog().getByRole("button", { name: "Report member", exact: true }).click();
    await expect(page.getByText("Thanks — the moderators can see your report.")).toBeVisible();
    await page.getByRole("dialog").getByRole("button", { name: "Close" }).click();

    await go("#/admin/complaints");
    const card = page.locator(".admin-cards > .card").filter({ hasText: "rude in voice" });
    await expect(card).toContainText(server.accounts.ivy.nickname);
    await card.getByRole("button", { name: /Clear all/ }).click();
    await dialog().getByRole("button", { name: "Clear" }).click();
    await expect(page.getByText("No complaints.")).toBeVisible();
    ivy.conn.close();
});

test("chat filters, server settings and a channel icon are saved", async () => {
    await go("#/admin/filters");
    await page.getByLabel("Blocked words").fill("forbiddenword\nanother");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByText("Saved", { exact: true }).first()).toBeVisible();
    const filters = await owner.conn.request(Msg.ChatFilterGet, {}, Msg.ChatFilterResponse);
    expect(filters.word_filter).toBe("forbiddenword,another");
    await owner.conn.request(Msg.ChatFilterSet, { word_filter: "" }, Msg.ChatFilterResponse);

    const original = await owner.conn.request(Msg.ServerConfigQuery, {}, Msg.ServerConfigResponse);
    await go("#/admin/server");
    const runtime = page.getByRole("form", { name: "Runtime settings" });
    await runtime.getByLabel(/Idle timeout/).fill("120");
    await runtime.getByRole("button", { name: "Save" }).click();
    await expect(page.getByText("Server settings saved")).toBeVisible();
    expect((await owner.conn.request(Msg.ServerConfigQuery, {}, Msg.ServerConfigResponse)).client_timeout_seconds).toBe(120);
    const { media_limits_management: _, ...restore } = original;
    await owner.conn.request(Msg.ServerConfigSet, restore, Msg.ServerConfigResponse);

    await go(`#/admin/channel/${hall.id}`);
    await page.getByRole("button", { name: "Channel icon" }).click();
    const chooser = page.waitForEvent("filechooser");
    await dialog().getByRole("button", { name: "Upload image" }).click();
    await (await chooser).setFiles({ name: "icon.png", mimeType: "image/png", buffer: PNG });
    await expect(page.getByText("Channel icon saved")).toBeVisible();
    await expect(page.locator(".admin-icon-preview")).toBeVisible();
});
