import { test, expect } from "@playwright/test";
for (const view of ["chat", "dm", "group"]) {
    test(`${view} stays above Android keyboard without document scrolling`, async ({ page }) => {
        await page.setViewportSize({ width: 390, height: 844 });
        await page.addInitScript(() => {
            const viewport = Object.assign(new EventTarget(), { height: 844, offsetTop: 0, scale: 1 });
            Object.defineProperty(window, "visualViewport", { value: viewport });
            window.__resizeVisual = (height, offsetTop = 0, scale = 1) => {
                Object.assign(viewport, { height, offsetTop, scale });
                viewport.dispatchEvent(new Event("resize"));
            };
        });
        await page.goto(`http://127.0.0.1:5175/app/ui-tests/layout.html?view=${view}`);
        await expect(page.locator("html")).toHaveAttribute("data-ready", "true");
        const input = page.locator(".composer textarea");
        await input.focus();
        for (const [height, top] of [[420, 0], [380, 20], [260, 0], [844, 0]]) {
            await page.evaluate(([height, top]) => window.__resizeVisual(height, top), [height, top]);
            await expect.poll(() => page.locator("#app").evaluate(el => Math.round(el.getBoundingClientRect().height))).toBe(height);
            const bounds = await page.locator(".composer").boundingBox();
            expect(bounds.y).toBeGreaterThanOrEqual(top);
            expect(bounds.y + bounds.height).toBeLessThanOrEqual(top + height + 1);
            const log = await page.locator(".chat-log").boundingBox();
            expect(log.height).toBeGreaterThan(0);
            expect(log.y + log.height).toBeLessThanOrEqual(bounds.y + 1);
            expect(await page.evaluate(() => window.scrollY)).toBe(0);
            await expect(input).toBeFocused();
        }
        await page.evaluate(() => window.__resizeVisual(422, 0, 2));
        await page.waitForTimeout(50);
        expect(await page.locator("#app").evaluate(el => Math.round(el.getBoundingClientRect().height))).toBe(844);
    });
}
test("floating navigation tracks the visual viewport on list screens", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("http://127.0.0.1:5175/app/ui-tests/layout.html?view=home");
    await expect(page.locator("html")).toHaveAttribute("data-ready", "true");
    await page.evaluate(() => document.documentElement.style.setProperty("--app-height", "420px"));
    const nav = await page.getByRole("navigation", { name: "Main" }).boundingBox();
    expect(nav.y + nav.height).toBeLessThan(420);
});
