import { expect, test } from "@playwright/test";
const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");

test("guest can sign in, reload and sign out without linking a device", async ({ page }) => {
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    await page.goto(`${server.webURL}/app/`);
    await page.locator("summary").filter({ hasText: "Continue as guest" }).click();
    await page.getByLabel("Display name", { exact: true }).fill("Guest visitor");
    await page.getByRole("button", { name: "Continue as guest", exact: true }).click();
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
    await page.getByRole("link", { name: "You", exact: true }).click();
    await expect(page.getByText(/^Guest visitor(#M\d+)?$/)).toBeVisible();
    await expect(page.getByRole("button", { name: "Save login as passkey" })).toHaveCount(0);
    await page.evaluate(() => {
        localStorage.setItem("noxa.settings", JSON.stringify({ theme: "arctic", contacts: [{ uid: "private-contact", name: "Private contact" }], notes: { "private-contact": "Private note" }, presenceMessage: "Private status" }));
        localStorage.setItem("noxa.servers", JSON.stringify([{ origin: "https://example.test", name: "Saved server" }]));
    });
    await page.reload();
    await expect(page.getByText(/^Guest visitor(#M\d+)?$/)).toBeVisible();
    await page.getByRole("button", { name: "Sign out of this device", exact: true }).click();
    await page.getByRole("button", { name: "Sign out of this device", exact: true }).last().click();
    await expect(page.getByRole("button", { name: "Sign in with passkey" })).toBeVisible();
    const stored = await page.evaluate(() => ({ settings: JSON.parse(localStorage.getItem("noxa.settings")), servers: localStorage.getItem("noxa.servers") }));
    expect(stored.settings.theme).toBe("arctic");
    expect(stored.settings.contacts).toBeUndefined();
    expect(stored.settings.notes).toBeUndefined();
    expect(stored.settings.presenceMessage).toBeUndefined();
    expect(stored.servers).toContain("Saved server");
    expect(errors).toEqual([]);
});
