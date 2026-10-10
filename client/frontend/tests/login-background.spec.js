import { test, expect } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.runtime = { EventsOn() { return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async () => {
            if (method === "GetSettings") return { language: "en", onboarding_done: true, alpha_dismissed: "test", recents: [] };
            if (method === "GetLoginPasswordStatus") return { supported: true, available: true };
            if (["ListTabs", "GetPermissions", "SubscriptionsForTab", "DMHistoryLoadForContext"].includes(method)) return [];
            if (["SessionInfoForTab", "IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (["Connected", "IsGuest"].includes(method)) return false;
            if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
            return "";
        }; } }) } };
    });
});

const frame = page => page.locator("#login-background").evaluate(canvas => canvas.toDataURL());

test("Aurora rain pauses on demand and stops in the connected workspace", async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "no-preference" });
    await page.goto("/");
    await expect(page.getByRole("button", { name: "Pause background animation" })).toBeVisible();
    const initial = await frame(page);
    await expect.poll(() => frame(page)).not.toBe(initial);
    await page.getByRole("button", { name: "Pause background animation" }).click();
    const paused = await frame(page);
    await page.waitForTimeout(150);
    expect(await frame(page)).toBe(paused);
    await page.getByRole("button", { name: "Play background animation" }).click();
    await expect.poll(() => frame(page)).not.toBe(paused);
    await page.evaluate(() => window.__noxa.showWorkspace());
    await expect(page.locator("#login-overlay")).toBeHidden();
    const hidden = await frame(page);
    await page.waitForTimeout(150);
    expect(await frame(page)).toBe(hidden);
    await page.evaluate(() => window.__noxa.showLogin());
    await expect.poll(() => frame(page)).not.toBe(hidden);
    await expect(page.locator("#login-addr")).toBeFocused();
});

test("reduced motion starts still, allows explicit play and follows preference changes", async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.goto("/");
    await expect(page.getByRole("button", { name: "Play background animation" })).toBeVisible();
    const still = await frame(page);
    await page.waitForTimeout(150);
    expect(await frame(page)).toBe(still);
    await page.getByRole("button", { name: "Play background animation" }).click();
    await expect.poll(() => frame(page)).not.toBe(still);
    await page.emulateMedia({ reducedMotion: "no-preference" });
    await page.emulateMedia({ reducedMotion: "reduce" });
    await expect(page.getByRole("button", { name: "Play background animation" })).toBeVisible();
    const stopped = await frame(page);
    await page.waitForTimeout(150);
    expect(await frame(page)).toBe(stopped);
    await page.evaluate(async () => {
        const { setLanguage } = await import("/src/i18n.js");
        setLanguage("de");
        window.dispatchEvent(new Event("noxa-language-changed"));
    });
    await expect(page.getByRole("button", { name: "Hintergrundanimation abspielen" })).toBeVisible();
    await page.emulateMedia({ forcedColors: "active" });
    await expect(page.locator("#login-motion")).toBeHidden();
    await expect(page.locator("#login-background")).toBeHidden();
});

test("the connection form remains usable when a canvas renderer is unavailable", async ({ page }) => {
    await page.addInitScript(() => {
        const getContext = HTMLCanvasElement.prototype.getContext;
        HTMLCanvasElement.prototype.getContext = function (...args) {
            return this.id === "login-background" ? null : getContext.apply(this, args);
        };
    });
    await page.goto("/");
    await expect(page.locator("#login-motion")).toBeHidden();
    await expect(page.locator("#login-addr")).toBeFocused();
    await page.locator("#login-addr").fill("voice.example");
    await expect(page.locator("#login-connect")).toBeEnabled();
});

test("the centered login remains readable and reachable at narrow sizes and larger type", async ({ page }, testInfo) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.goto("/");
    await expect(page.locator("#login-addr")).toBeFocused();
    for (const width of [1440, 768, 320]) {
        await page.setViewportSize({ width, height: 800 });
        await expect.poll(() => page.locator(".login-shell").evaluate(el => {
            const box = el.getBoundingClientRect();
            return Math.abs(box.x + box.width / 2 - document.getElementById("login-overlay").clientWidth / 2);
        })).toBeLessThan(1);
        expect(await page.locator("#login-overlay").evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true);
        await page.screenshot({ path: testInfo.outputPath(`aurora-${width}.png`) });
    }
    await page.evaluate(() => { document.documentElement.style.fontSize = "20px"; });
    await page.setViewportSize({ width: 320, height: 480 });
    await page.locator("#login-options summary").click();
    await page.locator("#login-serverpw").fill("test-password");
    await page.locator("#login-connect").scrollIntoViewIfNeeded();
    await expect(page.locator("#login-connect")).toBeInViewport({ ratio: 1 });
    expect(await page.locator("#login-overlay").evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true);
    await expect(page.locator(".login-brand")).toContainText("noXa");
    await expect(page.locator("#login-overlay")).not.toContainText("Join the conversation");
});
