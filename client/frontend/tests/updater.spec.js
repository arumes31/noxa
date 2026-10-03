import { expect, test } from "./fixtures.js";

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
            if (method === "GetSettings") {
                if (s.settingsThrows) throw new Error("settings bridge unavailable");
                if (s.settingsPending) await new Promise(resolve => { window.__finishSettings = resolve; });
                return { onboarding_done: true, alpha_dismissed: "0.4.0", updates_auto_check: s.enabled !== false, notification_matrix: {} };
            }
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
            if (method === "CancelUpdate") {
                if (s.cancelThrows) throw new Error("cancel unavailable");
                s.downloadError = "update cancelled";
                window.__finishDownload?.();
                return true;
            }
            if (method === "GetUpdatePhase") return s.restored ? "restored" : "ready";
            if (method === "ApplyAndRestart") {
                if (s.restartPending) await new Promise(resolve => { window.__finishRestart = resolve; });
                if (s.restartThrows) throw new Error("launch failed");
                return s.restartError || "";
            }
            if (method === "ListTabs" || method === "ListBookmarks") return [];
            return "";
        } }) } };
    }, scenario);
    await page.goto("/");
    if (!scenario.settingsPending) await expect(page.locator(".login-card")).toHaveClass(/(?:^|\s)in(?:\s|$)/);
}

test("replacement readiness waits for native startup and rendered login", async ({ page }) => {
    await boot(page, { settingsPending: true, enabled: false });
    await page.waitForFunction(() => typeof window.__finishSettings === "function");
    expect(await page.evaluate(() => window.__calls.ConfirmUpdateStartup)).toBeUndefined();
    await page.evaluate(() => window.__finishSettings());
    await expect(page.locator(".login-card")).toHaveClass(/(?:^|\s)in(?:\s|$)/);
    await expect.poll(() => page.evaluate(() => window.__calls.ConfirmUpdateStartup)).toBe(1);
});

test("broken native settings startup cannot confirm a healthy replacement", async ({ page }) => {
    await boot(page, { settingsThrows: true });
    await expect(page.locator(".login-card")).toHaveClass(/(?:^|\s)in(?:\s|$)/);
    await expect(page.getByRole("button", { name: "Update now" })).toBeVisible();
    expect(await page.evaluate(() => window.__calls.ConfirmUpdateStartup)).toBeUndefined();
});

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

test("download can be cancelled and retried without showing an installation failure", async ({ page }) => {
    await boot(page, { downloadPending: true });
    await page.getByRole("button", { name: "Update now" }).click();
    await page.getByRole("button", { name: "Cancel download", exact: true }).click();
    await expect(page.locator(".upd-status")).toHaveText("Download cancelled. You can try again.");
    await expect(page.locator(".upd-status")).not.toHaveClass(/warn/);
    await expect(page.getByRole("button", { name: "Update now" })).toBeEnabled();
    expect(await page.evaluate(() => window.__events.update_phase.length)).toBe(0);
    await page.evaluate(() => { window.__scenario = {}; });
    await page.getByRole("button", { name: "Update now" }).click();
    await expect(page.getByRole("button", { name: "Restart now" })).toBeVisible();
});

test("verification and installation show distinct stages and cannot be cancelled", async ({ page }) => {
    await boot(page, { downloadPending: true });
    await page.getByRole("button", { name: "Update now" }).click();
    await expect(page.getByRole("button", { name: "Cancel download", exact: true })).toBeVisible();
    for (const [phase, label] of [["verifying", "Verifying update…"], ["installing", "Installing update…"]]) {
        await page.evaluate(phase => window.__events.update_phase.forEach(cb => cb(phase)), phase);
        await expect(page.locator(".upd-status")).toHaveText(label);
        await expect(page.locator(".upd-cancel")).toBeHidden();
        await expect(page.getByRole("button", { name: "Processing…", exact: true })).toBeDisabled();
        await expect(page.locator(".upd-speed")).toBeHidden();
    }
    await page.evaluate(() => window.__finishDownload());
    await expect(page.locator(".upd-status")).toHaveText("Ready to restart");
    expect(await page.evaluate(() => window.__events.update_phase.length)).toBe(0);
});

test("a restored failed startup offers a fresh update check", async ({ page }) => {
    await boot(page, { restartError: "Previous version restored after startup failed", restored: true });
    await page.getByRole("button", { name: "Update now" }).click();
    await page.getByRole("button", { name: "Restart now" }).click();
    await expect(page.locator(".upd-status")).toContainText("Previous version restored");
    await expect(page.getByRole("button", { name: "Restart now" })).toBeHidden();
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await expect(page.getByRole("button", { name: "Update now" })).toBeEnabled();
});

test("restart remains visibly processing until startup succeeds or recovers", async ({ page }) => {
    await boot(page, { restartPending: true, restartError: "startup failed" });
    await page.getByRole("button", { name: "Update now" }).click();
    await page.getByRole("button", { name: "Restart now" }).click();
    await expect(page.getByRole("button", { name: "Processing…", exact: true })).toBeDisabled();
    await expect(page.locator(".upd-status")).toContainText("Starting the updated app");
    await page.keyboard.press("Escape");
    await expect(page.locator(".update-dlg")).toBeVisible();
    await expect(page.getByRole("button", { name: "Close", exact: true })).toBeDisabled();
    await page.evaluate(() => window.__finishRestart());
    await expect(page.getByRole("button", { name: "Restart now" })).toBeEnabled();
    await expect(page.locator(".upd-status")).toContainText("startup failed");
});

for (const scenario of [{ enabled: false }, { available: false }, { offline: true }]) {
    test(`startup stays quiet ${JSON.stringify(scenario)}`, async ({ page }) => {
        await boot(page, scenario);
        await expect(page.locator(".update-dlg")).toHaveCount(0);
        if (scenario.enabled === false) expect(await page.evaluate(() => window.__calls.CheckForUpdate)).toBeUndefined();
    });
}

test("update shows progress, prevents duplicates, then restarts", async ({ page }, testInfo) => {
    await boot(page, { downloadPending: true });
    await page.getByRole("button", { name: "Update now" }).click();
    await expect(page.locator(".upd-pct")).toHaveText("50%");
    await expect(page.getByRole("button", { name: "Processing…", exact: true })).toBeDisabled();
    await page.getByRole("dialog", { name: "Check for updates" }).screenshot({ path: testInfo.outputPath("update-processing.png") });
    await page.keyboard.press("Escape");
    await expect(page.locator(".update-dlg")).toBeVisible();
    await page.evaluate(() => window.__finishDownload());
    await page.getByRole("button", { name: "Restart now" }).click();
    expect(await page.evaluate(() => window.__calls.DownloadAndApply)).toBe(1);
    expect(await page.evaluate(() => window.__calls.ApplyAndRestart)).toBe(1);
    expect(await page.evaluate(() => window.__events.update_progress.length)).toBe(0);
});

test("download speed handles units, stalls, unknown sizes and completion", async ({ page }) => {
    await boot(page, { downloadPending: true });
    await page.clock.install();
    await page.getByRole("button", { name: "Update now" }).click();
    const speed = page.locator(".upd-speed");
    await expect(speed).toHaveText("Speed: —");
    await page.evaluate(() => window.__events.update_progress.forEach(cb => cb(55, { bytes_per_second: 524288 })));
    await expect(speed).toHaveText("Speed: 512.00 KiB/s");
    await page.evaluate(() => window.__events.update_progress.forEach(cb => cb(-1, { bytes_per_second: 2621440 })));
    await expect(speed).toHaveText("Speed: 2.50 MiB/s");
    await expect(page.locator(".upd-pct")).toHaveText("—");
    await page.clock.runFor(2000);
    await expect(speed).toHaveText("Speed: 0 B/s");
    await page.evaluate(() => window.__events.update_progress.forEach(cb => cb(75, { bytes_per_second: 1048576 })));
    await expect(speed).toHaveText("Speed: 1.00 MiB/s");
    await page.evaluate(() => window.__finishDownload());
    await expect(page.getByRole("button", { name: "Restart now" })).toBeVisible();
    await expect(page.locator(".upd-progress")).toBeHidden();
    expect(await page.evaluate(() => window.__events.update_progress.length)).toBe(0);
});

test("retry clears the previous attempt's download speed", async ({ page }) => {
    await boot(page, { downloadPending: true, downloadError: "connection lost" });
    await page.getByRole("button", { name: "Update now" }).click();
    await page.evaluate(() => window.__events.update_progress.forEach(cb => cb(50, { bytes_per_second: 1048576 })));
    await expect(page.locator(".upd-speed")).toHaveText("Speed: 1.00 MiB/s");
    await page.evaluate(() => window.__finishDownload());
    await expect(page.locator(".upd-progress")).toBeHidden();
    await page.getByRole("button", { name: "Update now" }).click();
    await expect(page.locator(".upd-speed")).toHaveText("Speed: —");
    await page.evaluate(() => window.__finishDownload());
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
    await expect(dialog.getByRole("button", { name: "Wird verarbeitet…", exact: true })).toBeDisabled();
    await page.evaluate(() => {
        for (const cb of window.__events.update_progress) cb(50, { bytes_per_second: 1048576 });
    });
    await expect(dialog.locator(".upd-speed")).toHaveText("Geschwindigkeit: 1.00 MiB/s");
    await expect(dialog.getByRole("button", { name: "Download abbrechen", exact: true })).toBeVisible();
    for (const [phase, label] of [["verifying", "Update wird geprüft…"], ["installing", "Update wird installiert…"]]) {
        await page.evaluate(phase => window.__events.update_phase.forEach(cb => cb(phase)), phase);
        await expect(dialog.locator(".upd-status")).toHaveText(label);
        await expect(dialog.locator(".upd-cancel")).toBeHidden();
    }
    await page.evaluate(() => window.__finishDownload());
    await expect(dialog.getByRole("button", { name: "Jetzt neu starten", exact: true })).toBeVisible();
    await expect(dialog.locator(".upd-status")).toHaveText("Bereit zum Neustart");
    await dialog.getByRole("button", { name: "Schließen", exact: true }).click();
});
