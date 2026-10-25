import { expect, test } from "@playwright/test";

import { desktopSession } from "./harness.mjs";
import { linkPhone } from "./helpers.mjs";
import { releaseDevices } from "./device-cleanup.mjs";

// The desktop session of the running test; released after it, pass or fail.
let desktop = null;
test.afterEach(async () => {
    if (desktop) await releaseDevices(desktop);
    desktop = null;
});

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
let info;

/** The passkey offer after linking hides the screen; decline it. */
async function declinePasskey(page) {
    await page.getByRole("dialog").getByRole("button", { name: "Not now" }).click({ timeout: 5000 }).catch(() => {});
}

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
});

async function openSettings(page) {
    desktop = await desktopSession(server.controlAddr, server.accounts.lou, { version: info.version });
    await linkPhone(page, desktop, server.webURL, expect);
    await declinePasskey(page);
    await page.getByRole("link", { name: "You", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();
    return desktop;
}

const rootFontSize = page => page.evaluate(() => parseFloat(getComputedStyle(document.documentElement).fontSize));

test("theme, accent and text size apply at once and survive a reload", async ({ page }) => {
    page.on("pageerror", error => console.log("pageerror:", error.message));
    await openSettings(page);
    const html = page.locator("html");
    await expect(html).toHaveAttribute("data-theme", "midnight");

    await page.getByLabel("Theme", { exact: true }).selectOption("light");
    await expect(html).toHaveAttribute("data-theme", "light");
    expect(await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--bg").trim())).toBe("#f4f7fa");

    await page.getByRole("button", { name: "Accent #ff6b81" }).click();
    await expect(page.getByRole("button", { name: "Accent #ff6b81" })).toHaveAttribute("aria-pressed", "true");
    expect(await page.evaluate(() => document.documentElement.style.getPropertyValue("--accent"))).toBe("#ff6b81");

    expect(await rootFontSize(page)).toBe(15);
    await page.getByRole("slider", { name: "Text size" }).fill("1.2");
    await expect.poll(() => rootFontSize(page)).toBe(18);

    await page.getByRole("switch", { name: /Compact mode/ }).check();
    await expect(html).toHaveAttribute("data-compact", "");

    await page.reload();
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible({ timeout: 15000 });
    await expect(html).toHaveAttribute("data-theme", "light");
    await expect(html).toHaveAttribute("data-compact", "");
    expect(await rootFontSize(page)).toBe(18);
    expect(await page.evaluate(() => document.documentElement.style.getPropertyValue("--accent"))).toBe("#ff6b81");

    // Back to the theme default accent.
    await page.getByRole("link", { name: "You", exact: true }).click();
    await page.getByRole("button", { name: "Theme default" }).click();
    expect(await page.evaluate(() => document.documentElement.style.getPropertyValue("--accent"))).toBe("");
});

test("the search box filters setting rows across sections", async ({ page }) => {
    await openSettings(page);
    const search = page.getByRole("searchbox", { name: "Search settings" });
    await search.fill("text size");
    await expect(page.locator(".settings-section")).toHaveCount(1);
    await expect(page.getByRole("slider", { name: "Text size" })).toBeVisible();
    await expect(page.getByLabel("Theme", { exact: true })).toHaveCount(0);
    await search.fill("previews");
    await expect(page.getByRole("switch", { name: /Link previews/ })).toBeVisible();
    await search.fill("nothing like this");
    await expect(page.getByText("No setting matches “nothing like this”.")).toBeVisible();
    await search.fill("");
    // Appearance, Chat, Notifications, Voice, Direct messages, Language, Data, About.
    await expect(page.locator(".settings-section")).toHaveCount(8);

    // About shows the version and links to Diagnostics.
    await page.locator(".settings-section", { hasText: "About" }).locator("summary").click();
    await expect(page.getByText(info.version, { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Check for updates" }).click();
    await expect(page.getByText("noXa is up to date.")).toBeVisible();
});

test("switching the language reloads the app in German", async ({ page }) => {
    await openSettings(page);
    await page.locator(".settings-section", { hasText: "Language" }).locator("summary").click();
    await page.getByLabel("Interface language").selectOption("de");
    await page.getByRole("button", { name: "Jetzt neu laden" }).click();
    await expect(page.getByRole("heading", { name: "Einstellungen" })).toBeVisible({ timeout: 15000 });
    await expect(page.locator("html")).toHaveAttribute("lang", "de");
    await expect(page.getByRole("searchbox", { name: "Einstellungen durchsuchen" })).toBeVisible();
});
