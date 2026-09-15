import { expect, test } from "@playwright/test";

async function boot(page, scenario = {}) {
    await page.addInitScript((scenario) => {
        window.__calls = {};
        window.__events = {};
        window.__scenario = scenario;
        window.runtime = {
            EventsOn(name, cb) {
                (window.__events[name] ||= []).push(cb);
                return () => { window.__events[name] = window.__events[name].filter((fn) => fn !== cb); };
            },
            EventsEmit() {},
        };
        window.go = { main: { App: new Proxy({}, { get: (_, method) => async () => {
            window.__calls[method] = (window.__calls[method] || 0) + 1;
            const s = window.__scenario;
            if (method === "GetSettings") return { onboarding_done: true, alpha_dismissed: "0.4.0", updates_auto_check: s.enabled !== false, notification_matrix: {} };
            if (method === "ClientVersionShort") return "0.4.0";
            if (method === "CheckForUpdate") {
                if (s.checkPending) await new Promise((resolve) => { window.__finishCheck = resolve; });
                if (s.offline) throw new Error("offline");
                return { available: s.available !== false, version: "v0.4.1", size: 1048576 };
            }
            if (method === "DownloadAndApply") {
                for (const cb of window.__events.update_progress || []) cb(50);
                if (s.downloadPending) await new Promise((resolve) => { window.__finishDownload = resolve; });
                if (s.downloadThrows) throw new Error("connection lost");
                return s.downloadError || "";
            }
            if (method === "ApplyAndRestart") {
                if (s.restartThrows) throw new Error("launch failed");
                return s.restartError || "";
            }
            if (method === "ListTabs" || method === "ListBookmarks") return [];
            return "";
        } }) } };
    }, scenario);
    await page.goto("/");
    await expect(page.locator(".login-card")).toHaveClass(/in/);
}

test("startup offers update before connecting and checks once", async ({ page }) => {
    await boot(page);
    const dialog = page.getByRole("dialog", { name: "Check for updates" });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText("v0.4.1");
    await expect(dialog.getByRole("button", { name: "Update now" })).toBeVisible();
    await expect(dialog).toContainText("current build: 0.4.0");
    await page.evaluate(() => window.__noxa.startupAutoCheck());
    expect(await page.evaluate(() => window.__calls.CheckForUpdate)).toBe(1);
    expect(await page.evaluate(() => window.__calls.Connect)).toBeUndefined();
    await dialog.getByRole("button", { name: "Close", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.locator("#login-addr")).toBeEnabled();
});

for (const scenario of [{ enabled: false }, { available: false }, { offline: true }]) {
    test(`startup stays quiet ${JSON.stringify(scenario)}`, async ({ page }) => {
        await boot(page, scenario);
        await expect(page.locator(".update-dlg")).toHaveCount(0);
        if (scenario.enabled === false) expect(await page.evaluate(() => window.__calls.CheckForUpdate)).toBeUndefined();
    });
}

test("update shows progress, prevents duplicates, then restarts", async ({ page }) => {
    await boot(page, { downloadPending: true });
    await page.getByRole("button", { name: "Update now" }).click();
    await expect(page.locator(".upd-pct")).toHaveText("50%");
    await expect(page.getByRole("button", { name: "Update now" })).toBeDisabled();
    await page.keyboard.press("Escape");
    await expect(page.locator(".update-dlg")).toBeVisible();
    await page.evaluate(() => window.__finishDownload());
    await page.getByRole("button", { name: "Restart now" }).click();
    expect(await page.evaluate(() => window.__calls.DownloadAndApply)).toBe(1);
    expect(await page.evaluate(() => window.__calls.ApplyAndRestart)).toBe(1);
    expect(await page.evaluate(() => window.__events.update_progress.length)).toBe(0);
});

for (const scenario of [{ downloadError: "update rejected: checksum mismatch" }, { downloadThrows: true }]) {
    test(`download failure is visible and retry works ${JSON.stringify(scenario)}`, async ({ page }) => {
        await boot(page, scenario);
        await page.getByRole("button", { name: "Update now" }).click();
        await expect(page.locator(".upd-status")).toContainText(/checksum mismatch|connection lost/);
        await expect(page.getByRole("button", { name: "Update now" })).toBeEnabled();
        await page.evaluate(() => { window.__scenario = {}; });
        await page.getByRole("button", { name: "Update now" }).click();
        await expect(page.getByRole("button", { name: "Restart now" })).toBeVisible();
        await expect(page.locator(".upd-status")).not.toHaveClass(/warn/);
    });
}

for (const scenario of [{ restartError: "permission denied" }, { restartThrows: true }]) {
    test(`restart failure is visible and retryable ${JSON.stringify(scenario)}`, async ({ page }) => {
        await boot(page, scenario);
        await page.getByRole("button", { name: "Update now" }).click();
        await page.getByRole("button", { name: "Restart now" }).click();
        await expect(page.locator(".upd-status")).toContainText(/permission denied|launch failed/);
        await expect(page.getByRole("button", { name: "Restart now" })).toBeEnabled();
    });
}

test("applied update retains restart action after closing and reopening", async ({ page }) => {
    await boot(page);
    await page.getByRole("button", { name: "Update now" }).click();
    await expect(page.getByRole("button", { name: "Restart now" })).toBeVisible();
    await page.getByRole("button", { name: "Close", exact: true }).click();
    await page.evaluate(() => window.__noxa.checkForUpdatesInteractive());
    await expect(page.getByRole("button", { name: "Restart now" })).toBeVisible();
    expect(await page.evaluate(() => window.__calls.DownloadAndApply)).toBe(1);
    expect(await page.evaluate(() => window.__calls.CheckForUpdate)).toBe(1);
});

test("manual update check works when automatic checks are disabled", async ({ page }) => {
    await boot(page, { enabled: false });
    await page.evaluate(() => window.__noxa.checkForUpdatesInteractive());
    await expect(page.getByRole("button", { name: "Update now" })).toBeVisible();
});

test("manual check displays network errors", async ({ page }) => {
    await boot(page, { offline: true });
    await page.evaluate(() => window.__noxa.checkForUpdatesInteractive());
    await expect(page.locator(".upd-status")).toContainText("check failed: Error: offline");
    await expect(page.getByRole("button", { name: "Update now" })).toBeHidden();
});

test("failed update checks retry in place and prevent duplicate requests", async ({ page }) => {
    await boot(page, { enabled: false, offline: true });
    await page.evaluate(() => window.__noxa.checkForUpdatesInteractive());
    const dialog = page.getByRole("dialog", { name: "Check for updates", exact: true });
    const retry = dialog.getByRole("button", { name: "Retry", exact: true });
    await expect(retry).toBeVisible();
    await page.evaluate(() => { window.__scenario = { checkPending: true, available: false }; });
    await retry.click();
    await expect(retry).toBeDisabled();
    await expect(dialog.locator(".upd-status")).toHaveText("checking for updates…");
    await page.evaluate(() => document.querySelector(".upd-retry").click());
    expect(await page.evaluate(() => window.__calls.CheckForUpdate)).toBe(2);
    await page.evaluate(() => window.__finishCheck());
    await expect(dialog.locator(".upd-status")).toContainText("up to date");
    await expect(dialog.locator(".upd-status")).not.toHaveClass(/warn/);
    await expect(retry).toBeHidden();
});

test("German updater translates failure, retry, progress and restart", async ({ page }) => {
    await boot(page, { enabled: false, offline: true });
    await page.evaluate(async () => {
        const { setLanguage } = await import("/src/i18n.js");
        setLanguage("de");
        await window.__noxa.checkForUpdatesInteractive();
    });
    const dialog = page.getByRole("dialog", { name: "Nach Updates suchen", exact: true });
    await expect(dialog).toBeVisible();
    await expect(dialog.locator(".upd-status")).toContainText("Suche fehlgeschlagen:");
    await page.evaluate(() => { window.__scenario = { downloadPending: true }; });
    await dialog.getByRole("button", { name: "Erneut versuchen", exact: true }).click();
    await expect(dialog.locator(".upd-status")).toHaveText("Update verfügbar: v0.4.1 (1.0 MiB)");
    await dialog.getByRole("button", { name: "Jetzt aktualisieren", exact: true }).click();
    await expect(dialog.locator(".upd-status")).toHaveText("Wird heruntergeladen…");
    await page.evaluate(() => window.__finishDownload());
    await expect(dialog.getByRole("button", { name: "Jetzt neu starten", exact: true })).toBeVisible();
    await expect(dialog.locator(".upd-status")).toHaveText("Update angewendet — Neustart erforderlich");
    await dialog.getByRole("button", { name: "Schließen", exact: true }).click();
});
