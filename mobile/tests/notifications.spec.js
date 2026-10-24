import { expect, test } from "@playwright/test";

import { ChatSession } from "../src/core/chat-session.js";
import { Msg } from "../src/core/frame.js";
import { desktopSession } from "./harness.mjs";
import { linkPhone, nextDeviceEvent, openServerToMembers } from "./helpers.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
let info;

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
});

test("unread mentions show on the phone and reads sync with the desktop", async ({ page }) => {
    page.on("pageerror", error => console.log("pageerror:", error.message));
    const desktop = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    await openServerToMembers(desktop);
    await linkPhone(page, desktop, server.webURL, expect);

    // Another member mentions the owner in the server chat.
    const rowan = await desktopSession(server.controlAddr, server.accounts.rowan, { version: info.version });
    const rowanChat = new ChatSession(rowan.conn, rowan.identity, rowan.auth);
    await rowanChat.send(0, `hey @${server.accounts.owner.nickname}, look`);
    const row = page.getByRole("link", { name: /Server chat/ });
    await expect(row.getByLabel("1 unread")).toHaveText("@1");

    // Reading it on the desktop clears the phone's badge.
    await desktop.conn.send(Msg.DeviceRequest, { action: "read", read: { scope: "channel", id: "0" } });
    await expect(row.getByLabel("1 unread")).toHaveCount(0);

    // Opening the chat on the phone tells the desktop.
    await rowanChat.send(0, "second message");
    await expect(row.getByLabel("1 unread")).toHaveText("1");
    const readOnPhone = nextDeviceEvent(desktop.conn, "read", event => event.read?.scope === "channel" && event.read?.id === "0");
    await row.click();
    await expect(page.getByRole("log")).toContainText("second message");
    await readOnPhone;

    // The server offers push (its key reached the phone); headless Chromium
    // reports notifications as blocked.
    await page.getByRole("link", { name: "Back" }).click();
    await page.getByRole("link", { name: "You", exact: true }).click();
    await expect(page.getByText("Notifications are blocked in this browser's site settings.")).toBeVisible();

    rowanChat.close();
    desktop.conn.close();
});
