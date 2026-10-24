import { test, expect } from "@playwright/test";
for (const width of [320, 390, 1440]) {
    for (const view of ["home", "servers", "you", "voice", "voice-active", "chat", "dm", "group", "incoming", "call"]) {
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
                await expect(page.locator("li").filter({ hasText: "Other desktop" }).getByRole("button")).toHaveCount(1);
                await expect(page.locator("li").filter({ hasText: "Master desktop" }).getByRole("button")).toHaveCount(0);
            }
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
