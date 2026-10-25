import { test, expect } from "@playwright/test";
for (const width of [320, 390, 1440]) {
    for (const view of ["home", "files", "inbox", "new-chat", "new-group", "servers", "you", "voice", "voice-active", "chat", "dm", "group", "incoming", "call"]) {
        test(`${view} fits ${width}px`, async ({ page }, testInfo) => {
            const errors = [];
            page.on("pageerror", error => errors.push(error.message));
            await page.setViewportSize({ width, height: 844 });
            await page.goto(`http://127.0.0.1:5175/app/ui-tests/layout.html?view=${view}`);
            await expect(page.locator("html")).toHaveAttribute("data-ready", "true");
            await expect(page.locator(".screen")).toBeVisible();
            if (["chat", "dm", "group"].includes(view)) {
                await expect(page.locator(".msg")).toHaveCount(60);
                await expect(page.locator(".composer")).toBeInViewport();
                await expect(page.getByRole("button", { name: "Send", exact: true })).toBeInViewport();
            }
            if (view === "voice") await expect(page.getByRole("button", { name: "Lobby", exact: true })).toHaveCount(0);
            if (view === "you") {
                // Every device can be renamed; only other, non-master devices can be signed out.
                await expect(page.locator("li").filter({ hasText: "Other desktop" }).getByRole("button", { name: "Sign out", exact: true })).toHaveCount(1);
                await expect(page.locator("li").filter({ hasText: "Master desktop" }).getByRole("button", { name: "Sign out", exact: true })).toHaveCount(0);
                await expect(page.locator("li").filter({ hasText: "Master desktop" }).getByRole("button", { name: /^Rename/ })).toHaveCount(1);
            }
            // Overflow can be clipped by #app, so document width alone misses it.
            for (const control of await page.locator(".screen-header a, .screen-header button").all()) {
                const rect = await control.boundingBox();
                expect(rect.x).toBeGreaterThanOrEqual(0);
                expect(rect.x + rect.width).toBeLessThanOrEqual(width + 1);
                expect(rect.width).toBeGreaterThanOrEqual(40);
            }
            expect(await page.locator(".screen").innerText()).not.toMatch(/\b(?:null|undefined)\b/);
            expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
            expect(await page.locator("svg").evaluateAll(nodes => nodes.every(n => n.getBoundingClientRect().width <= 26 && n.getBoundingClientRect().height <= 26))).toBe(true);
            await page.screenshot({ path: testInfo.outputPath(`${view}-${width}.png`) });
            expect(errors).toEqual([]);
        });
    }
}

test("mobile server list hides idle phones and counts accounts once", async ({ page }) => {
    await page.goto("http://127.0.0.1:5175/app/ui-tests/layout.html?view=presence");
    await expect(page.locator(".screen")).toContainText("2 online");
    await expect(page.locator(".screen")).not.toContainText("Daniel mobile");
    await expect(page.locator(".screen")).toContainText("Daniel");
});

for (const failure of [false, true]) {
    test(`sign-out preserves passkeys and handles failure=${failure}`, async ({ page }) => {
        const errors = [];
        page.on("pageerror", error => errors.push(error.message));
        await page.goto(`http://127.0.0.1:5175/app/ui-tests/layout.html?view=${failure ? "signout-error" : "signout"}`);
        await page.getByRole("button", { name: "Sign out of this device", exact: true }).click();
        await expect(page.getByText("Your saved passkeys remain available.", { exact: false })).toBeVisible();
        await page.getByRole("button", { name: "Sign out of this device", exact: true }).last().click();
        await expect(page.locator("html")).toHaveAttribute("data-device-request", JSON.stringify({ action: "sign_out" }));
        if (failure) await expect(page.getByText("Server unavailable", { exact: false })).toBeVisible();
        expect(errors).toEqual([]);
    });
}

for (const kind of ["attachments", "deleted"]) {
    test(`Home uses readable ${kind} previews in channel, direct and group rows`, async ({ page }) => {
        await page.goto(`http://127.0.0.1:5175/app/ui-tests/layout.html?view=home-${kind}`);
        await expect(page.locator("html")).toHaveAttribute("data-ready", "true");
        const rows = page.locator(".list .row-sub");
        await expect(rows).toHaveCount(3);
        for (const row of await rows.all()) {
            await expect(row).toContainText(kind === "attachments" ? "example.txt" : "Message deleted");
            await expect(row).not.toContainText("[file:");
            await expect(row).not.toContainText("AAAAAAAA");
        }
    });
}
