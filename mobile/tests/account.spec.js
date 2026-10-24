import { expect, test } from "@playwright/test";
import net from "node:net";

import { fromBase64, sealDM } from "../src/core/crypto.js";
import { Msg } from "../src/core/frame.js";
import { desktopSession } from "./harness.mjs";
import { linkPhone, nextDeviceEvent, openServerToMembers, publishKeyRecord, registerDesktop } from "./helpers.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
const run = Date.now().toString(36);
let info;

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
});

function trackErrors(page) {
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    return errors;
}

async function signInAsGuest(page, name) {
    await page.locator("summary").filter({ hasText: "Continue as guest" }).click();
    await page.getByLabel("Display name", { exact: true }).fill(name);
    await page.getByRole("button", { name: "Continue as guest", exact: true }).click();
}

/** Creates a root voice channel as the owner; returns its ID. */
async function createChannel(owner, name, password = "") {
    const state = await owner.conn.request(Msg.RoleChannelQuery, { kind: "channel_create", channel_id: 0 }, Msg.RoleChannelState);
    const result = await owner.conn.request(Msg.RoleChannelChange, {
        kind: "channel_create", expected_revision: state.revision, parent_id: 0, channel_type: 2,
        settings: { name, opus_bitrate: 64000, opus_fec: true }, ...(password ? { password, set_password: true } : {}),
    }, Msg.RoleChannelResult);
    return result.channel_id;
}

const escapeQuery = text => text.replace(/\\/g, "\\\\").replace(/ /g, "\\s").replace(/\|/g, "\\p").replace(/\//g, "\\/").replace(/\n/g, "\\n");

/** Runs ServerQuery commands as the owner; resolves with the status lines. */
function serverQuery(commands) {
    return new Promise((resolve, reject) => {
        const socket = net.connect(server.queryAddr.port, server.queryAddr.host);
        const queue = [`login ${escapeQuery(server.accounts.owner.uid)} ${escapeQuery(server.accounts.owner.password)} authorization_model=roles-v1`, ...commands];
        const statuses = [];
        let buffer = "";
        let banner = 2;
        const next = () => {
            const command = queue.shift();
            if (command === undefined) {
                socket.end("quit\n");
                resolve(statuses);
                return;
            }
            socket.write(command + "\n");
        };
        socket.on("data", data => {
            buffer += data;
            let index;
            while ((index = buffer.indexOf("\n")) >= 0) {
                const line = buffer.slice(0, index).trim();
                buffer = buffer.slice(index + 1);
                if (banner > 0) {
                    if (--banner === 0) next();
                    continue;
                }
                if (line.startsWith("error id=")) {
                    statuses.push(line);
                    if (!line.startsWith("error id=0 ")) {
                        socket.destroy();
                        reject(new Error(line));
                        return;
                    }
                    next();
                }
            }
        });
        socket.on("error", reject);
    });
}

function setServerText(key, value) {
    return serverQuery([`servertextset data=${escapeQuery(JSON.stringify({ key, value }))}`]);
}

test("the server list registers this server, adds another and switches", async ({ page }) => {
    const errors = trackErrors(page);
    await page.goto(`${server.webURL}/app/`);
    await signInAsGuest(page, `lister-${run}`);
    await page.getByRole("link", { name: "You", exact: true }).click();
    const current = page.getByRole("button", { name: `Open ${info.server_name}` });
    await expect(current).toHaveAttribute("aria-current", "true");
    await expect(current).toContainText("This server");

    await page.getByRole("button", { name: "Add server", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Add server" });
    await dialog.getByLabel("Server address").fill("other.example");
    await dialog.getByRole("button", { name: "Add server" }).click();
    await expect(page.getByRole("button", { name: "Open other.example" })).toBeVisible();
    // The list is per browser: it survives a reload.
    await page.reload();
    await page.getByRole("link", { name: "You", exact: true }).click();
    await expect(page.getByRole("button", { name: "Open other.example" })).toBeVisible();

    // The Servers tab's menu switches servers; this one opens Home.
    await page.getByRole("link", { name: "Servers", exact: true }).click();
    await page.getByRole("button", { name: "Server menu" }).click();
    await page.getByRole("button", { name: "Switch server" }).click();
    await page.getByRole("dialog", { name: "Switch server" }).getByRole("button", { name: `Open ${info.server_name}` }).click();
    await expect(page).toHaveURL(/\/app\/#\/home$/);
    await expect(page.getByRole("heading", { name: "Home" })).toBeVisible();
    expect(errors).toEqual([]);
});

test("an invitation link joins its channel after signing in", async ({ page }) => {
    const errors = trackErrors(page);
    const owner = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    await openServerToMembers(owner);
    const name = `invited-${run}`;
    const channelID = await createChannel(owner, name);
    const invite = await owner.conn.request(Msg.ChannelInviteRequest, { action: "regenerate", channel_id: channelID, access: "guest" }, Msg.ChannelInviteResult);
    expect(invite.token).toHaveLength(43);

    await page.goto(`${server.webURL}/invite/${channelID}#${invite.token}`);
    await expect(page.getByText("You were invited to a channel on this server.")).toBeVisible();
    // The token left the address bar.
    expect(new URL(page.url()).pathname).toBe("/app/");
    await signInAsGuest(page, `invitee-${run}`);
    await expect(page.getByText(`You joined #${name}.`)).toBeVisible({ timeout: 15000 });
    await expect(page).toHaveURL(new RegExp(`#/chat/${channelID}$`));

    // A link with an unknown token is refused.
    await page.evaluate(() => { location.hash = "#/servers"; });
    await page.getByRole("button", { name: "Server menu" }).click();
    await page.getByRole("button", { name: "Join with invite link" }).click();
    const dialog = page.getByRole("dialog", { name: "Join with invite link" });
    await dialog.getByLabel("Invitation link").fill(`noxa://127.0.0.1:12345/invite/${channelID}#${"B".repeat(42)}w`);
    await dialog.getByRole("button", { name: "Join" }).click();
    await expect(page.getByText("This invitation is invalid or has expired.")).toBeVisible();
    owner.conn.close();
    expect(errors).toEqual([]);
});

test("the server rules must be accepted before chatting", async ({ page }) => {
    const errors = trackErrors(page);
    const owner = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    await openServerToMembers(owner);
    owner.conn.close();
    await setServerText("server_rules", `Be kind ${run}.\nNo spam.`);
    try {
        await page.goto(`${server.webURL}/app/`);
        await signInAsGuest(page, `ruly-${run}`);
        const rules = page.getByRole("alertdialog", { name: "Server rules" });
        await expect(rules).toBeVisible({ timeout: 15000 });
        await expect(rules).toContainText(`Be kind ${run}.`);
        await rules.getByRole("button", { name: "Accept" }).click();
        await expect(rules).toHaveCount(0);
        // Chat works once accepted: the global chat's channels are subscribed.
        await page.getByRole("link", { name: "Servers", exact: true }).click();
        await expect(page.getByRole("link", { name: "Server chat" })).toBeVisible();

        // Guests are asked on every connect; declining signs out.
        await page.reload();
        await expect(rules).toBeVisible({ timeout: 15000 });
        await rules.getByRole("button", { name: "Decline and sign out" }).click();
        await expect(page.getByRole("button", { name: "Sign in with passkey" })).toBeVisible();
    } finally {
        await setServerText("server_rules", "");
    }
    expect(errors).toEqual([]);
});

test("joining a password-protected channel asks for its password", async ({ page }) => {
    const errors = trackErrors(page);
    const owner = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    await openServerToMembers(owner);
    const name = `vault-${run}`;
    await createChannel(owner, name, "sesame");
    owner.conn.close();

    await page.goto(`${server.webURL}/app/`);
    await signInAsGuest(page, `safecracker-${run}`);
    await page.getByRole("link", { name: "Servers", exact: true }).click();
    const row = page.locator(".row", { hasText: name }).filter({ has: page.getByRole("button", { name: `Join ${name}` }) });
    await expect(row.getByRole("img", { name: "Password protected" })).toBeVisible();

    await row.getByRole("button", { name: `Join ${name}` }).click();
    let dialog = page.getByRole("dialog", { name: "Channel password" });
    await dialog.getByLabel("Password").fill("wrong");
    await dialog.getByRole("button", { name: "Join" }).click();
    await expect(page.getByText(/Could not join|join/i).last()).toBeVisible();
    await expect(page).not.toHaveURL(/#\/voice$/);

    await row.getByRole("button", { name: `Join ${name}` }).click();
    dialog = page.getByRole("dialog", { name: "Channel password" });
    await dialog.getByLabel("Password").fill("sesame");
    await dialog.getByRole("button", { name: "Join" }).click();
    await expect(page).toHaveURL(/#\/voice$/, { timeout: 20000 });
    await expect(page.getByText("Voice connected")).toBeVisible({ timeout: 20000 });
    expect(errors).toEqual([]);
});

test("devices renamed on the phone show on the desktop", async ({ page }) => {
    const errors = trackErrors(page);
    const desktop = await desktopSession(server.controlAddr, server.accounts.mia, { version: info.version });
    const desktopDevice = await registerDesktop(desktop);
    await linkPhone(page, desktop, server.webURL, expect);
    await page.getByRole("link", { name: "You", exact: true }).click();

    const renamed = nextDeviceEvent(desktop.conn, "renamed", event => event.device?.name === `Pocket ${run}`);
    await page.locator(".acct-device", { hasText: "This device" }).getByRole("button", { name: /^Rename / }).click();
    let dialog = page.getByRole("dialog");
    await dialog.getByLabel("Device name").fill(`Pocket ${run}`);
    await dialog.getByRole("button", { name: "Rename" }).click();
    await renamed;
    await expect(page.locator(".acct-device", { hasText: "This device" })).toContainText(`Pocket ${run}`);

    // The desktop can be renamed from the phone too.
    await page.locator(".acct-device", { hasText: "Master device" }).getByRole("button", { name: /^Rename / }).click();
    dialog = page.getByRole("dialog");
    await dialog.getByLabel("Device name").fill(`Desk ${run}`);
    await dialog.getByRole("button", { name: "Rename" }).click();
    await expect(page.locator(".acct-device", { hasText: "Master device" })).toContainText(`Desk ${run}`);

    const list = await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    expect(list.devices.find(d => d.device_id === desktopDevice).name).toBe(`Desk ${run}`);
    expect(list.devices.find(d => d.kind === "web").name).toBe(`Pocket ${run}`);
    desktop.conn.close();
    expect(errors).toEqual([]);
});

test("a phone copies its direct-message history to a newly linked phone", async ({ page, browser }) => {
    test.setTimeout(120000);
    const errors = trackErrors(page);
    const desktop = await desktopSession(server.controlAddr, server.accounts.ivy, { version: info.version });
    await registerDesktop(desktop);
    await publishKeyRecord(desktop);
    const linked = await linkPhone(page, desktop, server.webURL, expect);

    // The first phone holds a conversation (copied from the desktop).
    const list = await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    const phoneA = list.devices.find(d => d.kind === "web" && d.x25519_public_key === linked.x25519_public_key);
    const history = [{ peer: "uid-sam", peer_nickname: "Sam", from: "uid-sam", from_nickname: "Sam", body: `hello from Sam ${run}`, sent_at: 1700000000, client_msg_id: `sam-${run}` }];
    const sealed = fromBase64(sealDM(JSON.stringify(history), fromBase64(phoneA.x25519_public_key), desktop.identity.xSecret));
    await desktop.conn.request(Msg.DeviceRequest, { action: "history_put", device_id: phoneA.device_id, history: { seq: 0, total: 1, data: btoa(String.fromCharCode(...sealed)) } },
        Msg.DeviceResult, { accept: r => r.action === "history_put" });
    await expect(page.getByRole("link", { name: /Sam/ })).toContainText(`hello from Sam ${run}`, { timeout: 15000 });

    // A second phone is linked; the first offers to copy its history.
    const context = await browser.newContext({ ...test.info().project.use });
    const second = await context.newPage();
    const secondErrors = trackErrors(second);
    await linkPhone(second, desktop, server.webURL, expect);
    const card = page.getByRole("group", { name: /^New device linked/ });
    await expect(card).toBeVisible({ timeout: 15000 });
    await card.getByRole("button", { name: "Copy history" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("radio", { name: "Everything" }).check();
    await dialog.getByRole("button", { name: "Copy history" }).click();
    await expect(page.getByText("Copied 1 messages to")).toBeVisible({ timeout: 15000 });
    await expect(card).toHaveCount(0);

    await expect(second.getByRole("link", { name: /Sam/ })).toContainText(`hello from Sam ${run}`, { timeout: 15000 });
    await context.close();
    desktop.conn.close();
    expect(errors).toEqual([]);
    expect(secondErrors).toEqual([]);
});

test("a device key backup signs the same device in again", async ({ page, browser }) => {
    test.setTimeout(120000);
    const errors = trackErrors(page);
    const desktop = await desktopSession(server.controlAddr, server.accounts.kai, { version: info.version });
    await registerDesktop(desktop);
    await linkPhone(page, desktop, server.webURL, expect);
    await page.getByRole("link", { name: "You", exact: true }).click();
    const fingerprint = (await page.locator(".acct-device", { hasText: "This device" }).locator(".mono").textContent()).trim();

    await page.getByRole("button", { name: "Back up this device's keys" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("Passphrase", { exact: true }).fill("long enough passphrase");
    await dialog.getByLabel("Repeat passphrase").fill("long enough passphrase");
    const [download] = await Promise.all([page.waitForEvent("download"), dialog.getByRole("button", { name: "Save backup" }).click()]);
    expect(download.suggestedFilename()).toMatch(/\.noxa-backup\.json$/);
    const file = await download.path();

    // A browser without the device's storage restores it from the file.
    const context = await browser.newContext({ ...test.info().project.use });
    const fresh = await context.newPage();
    const freshErrors = trackErrors(fresh);
    await fresh.goto(`${server.webURL}/app/`);
    await fresh.locator("summary").filter({ hasText: "Restore from backup" }).click();
    await fresh.getByLabel("Backup file").setInputFiles(file);
    await fresh.getByLabel("Passphrase", { exact: true }).fill("wrong passphrase");
    await fresh.getByRole("button", { name: "Restore and sign in" }).click();
    await expect(fresh.getByText("Wrong passphrase, or the file is damaged.")).toBeVisible({ timeout: 15000 });
    await fresh.getByLabel("Passphrase", { exact: true }).fill("long enough passphrase");
    await fresh.getByRole("button", { name: "Restore and sign in" }).click();
    await expect(fresh.getByRole("navigation", { name: "Main" })).toBeVisible({ timeout: 15000 });
    await fresh.getByRole("link", { name: "You", exact: true }).click();
    await expect(fresh.locator(".acct-device", { hasText: "This device" }).locator(".mono")).toHaveText(fingerprint);
    const list = await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    expect(list.devices.filter(d => d.kind === "web")).toHaveLength(1);
    await context.close();
    desktop.conn.close();
    expect(errors).toEqual([]);
    expect(freshErrors).toEqual([]);
});
