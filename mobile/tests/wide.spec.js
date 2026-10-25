// The wide layout (tablets, desktop browsers): runs in the "tablet" project.
import { expect, test } from "@playwright/test";

import { desktopSession } from "./harness.mjs";
import { linkPhone, openServerToMembers } from "./helpers.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
let info;

test.skip(({ viewport }) => (viewport?.width || 0) < 900, "the wide layout needs at least 900 px");

/** linkPhone declines the passkey offer and the install banner; nothing may stay on top. */
async function link(page, desktop) {
    await linkPhone(page, desktop, server.webURL, expect);
    await expect(page.getByRole("dialog")).toHaveCount(0);
}

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
});

test("the wide shell: channels, tabs, quick switcher, details, voice card, shortcuts, splitters", async ({ page }) => {
    test.setTimeout(150000);
    page.on("pageerror", error => console.log("pageerror:", error.message));
    const owner = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    await openServerToMembers(owner);
    const ivy = await desktopSession(server.controlAddr, server.accounts.ivy, { version: info.version });
    const rowan = await desktopSession(server.controlAddr, server.accounts.mia, { version: info.version });
    await link(page, rowan);
    const me = server.accounts.mia.nickname;

    // Three panes: sidebar, centre with the Home screen, details; no phone navigation bar.
    const sidebar = page.getByRole("navigation", { name: "Main" });
    const details = page.getByRole("complementary", { name: "Details" });
    const centre = page.locator(".shell-screen");
    await expect(sidebar).toBeVisible();
    await expect(details).toBeVisible();
    await expect(centre.getByRole("heading", { name: "Home" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Search or jump to" })).toBeVisible();
    await expect(page.locator("#ping-badge")).toHaveCount(1);
    await expect(page.locator("#status-quick")).toHaveCount(1);
    await expect(page.locator(".nav")).toBeHidden();
    await expect(details).toContainText(server.accounts.ivy.nickname, { timeout: 15000 });

    // A text channel opens in the centre and becomes a tab.
    await sidebar.getByRole("link", { name: /Public1/ }).click();
    await expect(page).toHaveURL(/#\/chat\/\d+$/);
    const chatURL = page.url();
    await expect(centre.getByRole("log", { name: "# Public1" })).toBeVisible();
    await expect(centre.getByRole("textbox", { name: /Public1/ })).toBeVisible();
    const tablist = page.getByRole("tablist", { name: "Open conversations" });
    await expect(tablist.getByRole("tab", { name: /Public1/ })).toHaveAttribute("aria-selected", "true");
    await expect(sidebar.getByRole("link", { name: /Public1/ })).toHaveAttribute("aria-current", "page");
    // The header's back button is hidden: the sidebar navigates.
    await expect(centre.getByRole("link", { name: "Back" })).toHaveCount(0);

    // The quick switcher opens a direct message as a second tab.
    await page.keyboard.press("Control+k");
    const switcher = page.getByRole("dialog", { name: "Search or jump to" });
    await expect(switcher).toBeVisible();
    await switcher.getByRole("combobox").fill(server.accounts.ivy.nickname);
    await expect(switcher.getByRole("option").first()).toContainText(server.accounts.ivy.nickname);
    await page.keyboard.press("Enter");
    await expect(switcher).toHaveCount(0);
    await expect(page).toHaveURL(new RegExp(`#/dm/${encodeURIComponent(ivy.uniqueID)}$`));
    const dmTab = tablist.getByRole("tab", { name: new RegExp(server.accounts.ivy.nickname) });
    await expect(dmTab).toHaveAttribute("aria-selected", "true");
    await expect(details).toContainText("Online");
    await expect(details.getByRole("button", { name: `Call ${server.accounts.ivy.nickname}` })).toBeVisible();

    // Alt+Up switches to the previous tab; the DM tab closes with its ×.
    await page.keyboard.press("Alt+ArrowUp");
    await expect(page).toHaveURL(chatURL);
    await page.getByRole("button", { name: `Close ${server.accounts.ivy.nickname}` }).click();
    await expect(dmTab).toHaveCount(0);
    await expect(tablist.getByRole("tab")).toHaveCount(1);
    // Closing the active tab falls back to Home.
    await page.getByRole("button", { name: /^Close #Public1/ }).click();
    await expect(page).toHaveURL(/#\/home$/);
    await expect(tablist).toBeHidden();

    // The switcher jumps to a channel: text before voice for the same name.
    await page.getByRole("button", { name: "Search or jump to" }).click();
    await switcher.getByRole("combobox").fill("public1");
    await expect(switcher.getByRole("option").first()).toContainText("Text channel");
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(chatURL);
    await expect(centre.getByRole("log", { name: "# Public1" })).toBeVisible();

    // Esc closes sheets, which are centred dialogs here.
    await sidebar.getByRole("button", { name: "New message or group" }).click();
    const sheetDialog = page.getByRole("dialog", { name: "New message or group" });
    await expect(sheetDialog).toBeVisible();
    const box = await sheetDialog.boundingBox();
    const viewport = page.viewportSize();
    expect(Math.abs(box.y + box.height / 2 - viewport.height / 2)).toBeLessThan(40);
    await page.keyboard.press("Escape");
    await expect(sheetDialog).toHaveCount(0);

    // Joining voice from the sidebar shows the side card; the chat stays in the centre.
    await sidebar.getByRole("button", { name: "Join Public1" }).click();
    const card = page.getByRole("region", { name: "Voice room" });
    await expect(card).toBeVisible({ timeout: 20000 });
    await expect(card).toContainText("Voice connected");
    await expect(page).toHaveURL(chatURL);
    // The details pane lists the channel's members, the voice tree shows them too.
    await expect(details.getByRole("heading", { name: /Members of Public1/ })).toBeVisible({ timeout: 20000 });
    await expect(details).toContainText(new RegExp(`${me}\\S* \\(you\\)`));
    await expect(sidebar.getByRole("button", { name: "Voice connected in Public1" })).toBeVisible();
    await page.screenshot({ path: test.info().outputPath("wide-voice.png") });

    // Keyboard shortcuts mute and deafen.
    const mute = card.getByRole("button", { name: "Mute" });
    await expect(mute).toHaveAttribute("aria-pressed", "false");
    await page.keyboard.press("Control+Shift+M");
    await expect(mute).toHaveAttribute("aria-pressed", "true");
    await page.keyboard.press("Control+Shift+M");
    await expect(mute).toHaveAttribute("aria-pressed", "false");
    await page.keyboard.press("Control+Shift+D");
    await expect(card.getByRole("button", { name: "Deafen" })).toHaveAttribute("aria-pressed", "true");
    await card.getByRole("button", { name: "Deafen" }).click();
    await expect(card.getByRole("button", { name: "Deafen" })).toHaveAttribute("aria-pressed", "false");

    // The details pane collapses and comes back.
    const toggle = page.getByRole("button", { name: "Details pane" });
    await toggle.click();
    await expect(details).toBeHidden();
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await toggle.click();
    await expect(details).toBeVisible();

    // Dragging the splitter resizes the sidebar; the width survives a reload.
    await card.getByRole("button", { name: "Leave" }).click();
    await expect(card).toHaveCount(0, { timeout: 20000 });
    const side = page.locator(".shell-side");
    const before = await side.evaluate(node => node.getBoundingClientRect().width);
    const handle = await page.getByRole("separator", { name: "Resize sidebar" }).boundingBox();
    await page.mouse.move(handle.x + handle.width / 2, handle.y + 200);
    await page.mouse.down();
    await page.mouse.move(handle.x + handle.width / 2 + 40, handle.y + 200, { steps: 4 });
    await page.mouse.move(handle.x + handle.width / 2 + 80, handle.y + 200, { steps: 4 });
    await page.mouse.up();
    await expect.poll(() => side.evaluate(node => Math.round(node.getBoundingClientRect().width))).toBe(Math.round(before + 80));
    await page.reload();
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible({ timeout: 20000 });
    await expect.poll(() => page.locator(".shell-side").evaluate(node => Math.round(node.getBoundingClientRect().width))).toBe(Math.round(before + 80));
    await expect(page.getByRole("tablist", { name: "Open conversations" }).getByRole("tab", { name: /Public1/ })).toBeVisible();
    // Double-click resets the default width.
    await page.getByRole("separator", { name: "Resize sidebar" }).dblclick();
    await expect.poll(() => page.locator(".shell-side").evaluate(node => Math.round(node.getBoundingClientRect().width))).toBe(Math.round(before));

    owner.conn.close();
    ivy.conn.close();
    rowan.conn.close();
});

test("the phone layout returns below 900 px", async ({ page }) => {
    const rowan = await desktopSession(server.controlAddr, server.accounts.kai, { version: info.version });
    await link(page, rowan);
    await expect(page.locator(".shell")).toHaveCount(1);
    await page.setViewportSize({ width: 600, height: 900 });
    await expect(page.locator(".shell")).toHaveCount(0);
    const phoneNav = page.locator("nav.nav");
    await expect(phoneNav).toBeVisible();
    await expect(page.getByRole("heading", { name: "Home" })).toBeVisible();
    // Sheets are bottom sheets again.
    await page.getByRole("button", { name: "New message or group" }).click();
    const box = await page.getByRole("dialog", { name: "New message or group" }).boundingBox();
    expect(Math.round(box.y + box.height)).toBeGreaterThanOrEqual(895);
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    // A landscape phone keeps the bottom navigation as a slim strip.
    await page.setViewportSize({ width: 844, height: 390 });
    await expect(page.locator(".shell")).toHaveCount(0);
    // The app height follows the viewport on the next animation frame.
    await expect.poll(async () => { const box = await phoneNav.boundingBox(); return box.y + box.height; }).toBeLessThanOrEqual(390);
    const strip = await phoneNav.boundingBox();
    expect(strip.height).toBeLessThanOrEqual(44);
    await page.setViewportSize({ width: 1194, height: 834 });
    await expect(page.locator(".shell")).toHaveCount(1);
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
    rowan.conn.close();
});
