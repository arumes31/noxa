import { readFile } from "node:fs/promises";

import { expect, test } from "@playwright/test";

import { desktopSession } from "./harness.mjs";
import { linkPhone } from "./helpers.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
let info;

/** The passkey offer after linking hides the screen; decline it. */
async function declinePasskey(page) {
    await page.getByRole("dialog").getByRole("button", { name: "Not now" }).click({ timeout: 5000 }).catch(() => {});
}

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
});

test("diagnostics shows live ping, runs the connection test and exports logs", async ({ page }) => {
    test.setTimeout(90000);
    page.on("pageerror", error => console.log("pageerror:", error.message));
    const desktop = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    await linkPhone(page, desktop, server.webURL, expect);
    await declinePasskey(page);
    await page.evaluate(() => console.warn("diagnostics e2e marker"));

    // The Servers header carries the ping badge.
    await page.getByRole("link", { name: "Servers", exact: true }).click();
    const badge = page.locator(".ping-badge");
    await expect(badge).toHaveText(/\d+ ms/);
    await expect(badge).toHaveAttribute("data-quality", /good|fair|poor/);
    await badge.click();

    await expect(page.getByRole("heading", { name: "Diagnostics" })).toBeVisible();
    await expect(page.locator('[data-diag="ping"]')).toHaveText(/\d+ ms/);
    await expect(page.locator('[data-diag="reconnects"]')).toHaveText("0");
    await expect(page.getByText("Join a voice channel to see its statistics.")).toBeVisible();

    await page.getByRole("button", { name: "Run test" }).click();
    await expect(page.getByText("Test complete")).toBeVisible({ timeout: 30000 });
    await expect(page.locator('[data-diag="test-ping"]')).toHaveText(/\d+ ms/);
    await expect(page.locator('[data-diag="throughput"]')).toHaveText(/\d.* (k|M)bit\/s/);

    const pending = page.waitForEvent("download");
    await page.getByRole("button", { name: "Export logs" }).click();
    const download = await pending;
    const text = (await readFile(await download.path())).toString("utf8");
    expect(text).toContain("noXa diagnostics");
    expect(text).toContain("[log]");
    expect(text).toContain("diagnostics e2e marker");
    desktop.conn.close();
});
