import { expect, test } from "@playwright/test";

// Task 4 review P3-8: the identity-change sheet shows the exact fingerprint
// that a confirmation pins (dm.trustNewIdentity), not only the contact's name.
test("the identity-change sheet shows the fingerprint being confirmed", async ({ page }) => {
    await page.goto("http://127.0.0.1:5175/app/ui-tests/layout.html?view=chat");
    const fingerprint = "a1b2 c3d4 e5f6 0718 293a 4b5c 6d7e 8f90";
    const result = page.evaluate(async fp => {
        const { confirmIdentityChange } = await import("/app/src/ui/message-view.js");
        return confirmIdentityChange("Bob", fp);
    }, fingerprint);
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("Bob");
    await expect(dialog.locator(".trust-fingerprint")).toHaveText(`New fingerprint: ${fingerprint}`);
    await dialog.getByRole("button", { name: "Trust new identity" }).click();
    expect(await result).toBe(true);
});

test("cancelling the identity-change sheet trusts nothing", async ({ page }) => {
    await page.goto("http://127.0.0.1:5175/app/ui-tests/layout.html?view=chat");
    const result = page.evaluate(async () => {
        const { confirmIdentityChange } = await import("/app/src/ui/message-view.js");
        return confirmIdentityChange("Bob", "ffff");
    });
    await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();
    expect(await result).toBe(false);
});
