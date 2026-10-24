import { test, expect } from "@playwright/test";

test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/web/info", route => route.fulfill({ json: { server_name: "The Common Room", protocol_version: 1 } }));
});

for (const width of [320, 390, 1440]) {
    test(`sign-in stays compact and usable at ${width}px`, async ({ page }, testInfo) => {
        const errors = [];
        page.on("pageerror", error => errors.push(error.message));
        await page.setViewportSize({ width, height: 844 });
        await page.goto("/app/");
        const passkey = page.getByRole("button", { name: "Sign in with passkey" });
        await expect(passkey).toBeVisible();
        const icon = await passkey.locator("svg").boundingBox();
        const button = await passkey.boundingBox();
        expect(icon.width).toBe(20);
        expect(icon.height).toBe(20);
        expect(button.height).toBeLessThanOrEqual(56);
        await expect(page.getByRole("button", { name: "Continue", exact: true })).toBeInViewport();
        expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
        const code = page.getByLabel("Code from your desktop");
        await page.getByRole("button", { name: "Continue", exact: true }).click();
        await expect(code).toBeFocused();
        expect(await code.evaluate(input => input.validity.valueMissing)).toBe(true);
        await code.fill("ABCD-EFGH");
        expect(await code.evaluate(input => input.checkValidity())).toBe(true);
        await page.screenshot({ path: testInfo.outputPath(`signin-${width}.png`) });
        expect(errors).toEqual([]);
    });
}

test("unsupported passkeys explain how to continue", async ({ page }) => {
    await page.addInitScript(() => { window.PublicKeyCredential = undefined; });
    await page.goto("/app/");
    await expect(page.getByRole("button", { name: "Sign in with passkey" })).toBeDisabled();
    await expect(page.getByText("This browser cannot use passkeys.", { exact: true })).toBeVisible();
    await expect(page.getByLabel("Code from your desktop")).toBeEnabled();
});
