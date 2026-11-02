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

test("guest login is hidden unless the server advertises it", async ({ page }) => {
    await page.goto("/app/");
    await expect(page.locator("summary").filter({ hasText: "Continue as guest" })).toHaveCount(0);
});

test("enabled guest login asks for a name and an advertised server password", async ({ page }) => {
    await page.route("**/api/v1/web/info", route => route.fulfill({ json: { server_name: "Guest server", protocol_version: 1, guest_login: true, server_password_required: true } }));
    await page.goto("/app/");
    await page.locator("summary").filter({ hasText: "Continue as guest" }).click();
    await expect(page.getByLabel("Display name", { exact: true })).toBeVisible();
    await expect(page.getByLabel("Server password", { exact: true })).toHaveAttribute("type", "password");
    await page.getByRole("button", { name: "Continue as guest", exact: true }).click();
    await expect(page.getByLabel("Display name", { exact: true })).toBeFocused();
});


test("link failures remain visible after transient notifications would have vanished", async ({ page }) => {
    await page.goto("http://127.0.0.1:5175/app/");
    await expect(page.getByRole("button", { name: "Continue", exact: true })).toBeVisible();
    await page.evaluate(async () => {
        const { Connection } = await import("/app/src/core/connection.js");
        const { runLink } = await import("/app/src/ui/connect.js");
        const original = Connection.open;
        Connection.open = async () => { throw new Error('Link failed <img src=x onerror=alert(1)>'); };
        try { await runLink(document.querySelector("main").parentElement, { code: "ABCD-EFGH" }, { onSignedIn: () => { throw new Error("must not sign in"); } }); }
        finally { Connection.open = original; }
    });
    const alert = page.getByRole("alert");
    await expect(alert).toContainText("Link failed <img src=x onerror=alert(1)>");
    await expect(alert).toBeFocused();
    await page.clock.install(); await page.clock.fastForward(10000);
    await expect(alert).toContainText("Link failed");
    await expect(alert.locator("img")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Continue", exact: true })).toBeEnabled();
});

test("QR scan storage failures are explained without an unhandled rejection", async ({ page }) => {
    const errors = []; page.on("pageerror", error => errors.push(error.message));
    await page.goto("http://127.0.0.1:5175/app/");
    await expect(page.getByRole("button", { name: "Scan QR code" })).toBeVisible();
    await page.evaluate(() => { indexedDB.open = () => { throw new Error("Browser storage unavailable"); }; });
    await page.getByRole("button", { name: "Scan QR code" }).click();
    await expect(page.getByRole("alert")).toContainText("Browser storage unavailable");
    await expect(page.locator(".qr-dialog")).toHaveCount(0);
    expect(errors).toEqual([]);
});


test("starting and cancelling a fresh QR scan clears an earlier failure", async ({ page }) => {
    await page.goto("http://127.0.0.1:5175/app/");
    await expect(page.getByRole("button", {name:"Scan QR code"})).toBeVisible();
    await page.evaluate(()=>{window.__originalIDBOpen=indexedDB.open;indexedDB.open=()=>{throw new Error("Earlier storage error");};});
    await page.getByRole("button", {name:"Scan QR code"}).click();
    await expect(page.getByRole("alert")).toContainText("Earlier storage error");
    await page.evaluate(()=>{indexedDB.open=window.__originalIDBOpen;});
    await page.getByRole("button", {name:"Scan QR code"}).click();
    await expect(page.locator(".qr-dialog")).toBeVisible();
    await expect(page.getByRole("alert")).toBeEmpty();
    await page.locator(".qr-dialog").getByRole("button",{name:"Cancel",exact:true}).click();
    await expect(page.getByRole("alert")).toBeEmpty();
});
