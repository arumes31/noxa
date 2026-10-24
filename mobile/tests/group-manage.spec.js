import { expect, test } from "@playwright/test";

import { Msg } from "../src/core/frame.js";
import { contactDesktop, group, nextEvent, openServer, phoneAccount, sendGroup } from "./conversation-helpers.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
let info;

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
    await openServer(server, info.version);
});

const get = async (desktop, id) => (await group(desktop, { action: "get", id })).conversations[0];

async function createGroup(page, name, invite = []) {
    await page.evaluate(() => { location.hash = "#/new-group"; });
    await page.getByRole("textbox", { name: "Group name" }).fill(name);
    for (const nickname of invite) await page.getByRole("checkbox", { name: nickname }).check();
    await page.getByRole("button", { name: "Create group chat" }).click();
    await expect(page.getByRole("heading", { level: 1 })).toContainText(name, { timeout: 15000 });
    return decodeURIComponent(await page.evaluate(() => location.hash.split("/")[2]));
}

/** Confirms the sheet the previous tap opened with its danger button. */
async function confirm(page, title, label) {
    await page.getByRole("dialog", { name: title }).getByRole("button", { name: label, exact: true }).click();
}

test("the phone manages a private group: activity, seen by, rename, invite, remove, owner, leave and delete", async ({ page }) => {
    page.on("pageerror", error => console.log("pageerror:", error.message));
    const vic = await contactDesktop(server, server.accounts.vic, info.version);
    const wes = await contactDesktop(server, server.accounts.wes, info.version);
    const { desktop: uma } = await phoneAccount(page, server, server.accounts.uma, info.version, expect);
    const vicName = server.accounts.vic.nickname;
    const wesName = server.accounts.wes.nickname;

    const id = await createGroup(page, "Crew", [vicName]);
    let crew = await get(vic, id);
    await group(vic, { action: "accept", id, revision: crew.revision });
    await expect(page.locator(".group-members")).toContainText(vicName, { timeout: 15000 });

    // Typing both ways.
    const typed = nextEvent(vic, "conversation_typing", data => data.conversation_id === id && data.unique_id === uma.uniqueID);
    const composer = page.getByRole("textbox", { name: /Message/ });
    await composer.pressSequentially("hi");
    await typed;
    await composer.fill("");
    vic.conn.send(Msg.Typing, { conversation_id: id });
    await expect(page.locator(".typing")).toContainText(`${vicName} is typing`);

    // The phone's message, then Vic's shared read position: "Seen by".
    const changed = nextEvent(vic, "conversation_changed", data => data.id === id && data.message_id);
    await composer.fill("hello crew");
    await composer.press("Enter");
    const { message_id: phoneMessage } = await changed;
    await group(vic, { action: "mark_read", id, read_message_id: phoneMessage, share_read: true });
    await expect(page.locator(".msg-seen")).toHaveText(`Seen by ${vicName}`);

    // Vic writes; the phone, showing the group, shares its read position.
    const read = nextEvent(vic, "conversation_read", data => data.conversation_id === id && data.unique_id === uma.uniqueID && data.message_id > phoneMessage);
    const vicMessage = await sendGroup(vic, id, "hi from Vic's desktop");
    await expect(page.getByRole("log")).toContainText("hi from Vic's desktop");
    expect((await read).message_id).toBe(vicMessage);

    // The owner deletes Vic's message for everyone.
    await page.locator(".msg", { hasText: "hi from Vic's desktop" }).click();
    await page.getByRole("button", { name: "Delete for everyone" }).click();
    await confirm(page, "Delete this message?", "Delete for everyone");
    await expect(page.getByRole("log")).not.toContainText("hi from Vic's desktop");
    const history = await group(vic, { action: "history", id });
    expect(history.messages.some(m => m.id === vicMessage)).toBe(false);

    // Group settings: roles, rename, invite and withdraw.
    await page.getByRole("button", { name: "Group settings" }).click();
    const sheet = page.getByRole("dialog", { name: "Group settings" });
    await expect(sheet.locator(".member-row", { hasText: "You" })).toContainText("Owner");
    await sheet.getByRole("textbox", { name: "Group name" }).fill("Crew 2");
    await sheet.getByRole("button", { name: "Rename" }).click();
    await expect.poll(async () => (await get(vic, id)).name).toBe("Crew 2");
    await sheet.getByRole("button", { name: "Invite members" }).click();
    await sheet.getByRole("checkbox", { name: wesName }).check();
    await sheet.getByRole("button", { name: "Send invitations" }).click();
    await expect.poll(async () => (await get(vic, id)).members.find(m => m.unique_id === wes.uniqueID)?.pending).toBe(true);
    await sheet.locator(".member-row", { hasText: wesName }).getByRole("button", { name: "Withdraw" }).click();
    await confirm(page, "Withdraw the invitation?", "Withdraw");
    await expect.poll(async () => (await get(vic, id)).members.some(m => m.unique_id === wes.uniqueID)).toBe(false);

    // Leaving needs another owner first; then the group leaves the phone.
    await expect(sheet.getByRole("button", { name: "Leave group" })).toBeDisabled();
    await sheet.locator(".member-row", { hasText: vicName }).getByRole("button", { name: "Make owner" }).click();
    await confirm(page, `Make ${vicName} the owner?`, "Make owner");
    await expect.poll(async () => (await get(vic, id)).owner).toBe(vic.uniqueID);
    await expect(sheet.locator(".member-row", { hasText: vicName })).toContainText("Owner");
    await sheet.getByRole("button", { name: "Leave group" }).click();
    await confirm(page, "Leave this group?", "Leave group");
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("Home");
    await expect.poll(async () => (await get(vic, id)).members.some(m => m.unique_id === uma.uniqueID)).toBe(false);
    await expect(page.getByRole("link", { name: /Crew 2/ })).toHaveCount(0);

    // An owner deletes a group for everyone.
    const tempID = await createGroup(page, "Temp");
    await page.getByRole("button", { name: "Group settings" }).click();
    await page.getByRole("dialog", { name: "Group settings" }).getByRole("button", { name: "Delete group" }).click();
    await confirm(page, "Delete this group?", "Delete group");
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("Home");
    const left = await group(uma, { action: "list" });
    expect(left.conversations.some(c => c.id === tempID)).toBe(false);
    uma.conn.close();
    vic.conn.close();
    wes.conn.close();
});
