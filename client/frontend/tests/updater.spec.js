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
        window.go = { main: { App: new Proxy({}, { get: (_, method) => method === "UpdateStartupPending" && window.__scenario.legacyBridge ? undefined : async () => {
            window.__calls[method] = (window.__calls[method] || 0) + 1;
            const s = window.__scenario;
            if (method === "UpdateStartupPending") {
                if (s.transitionQueryPending) await new Promise(resolve => { window.__finishTransitionQuery = resolve; });
                if (s.transitionQueryThrows) throw new Error("startup status unavailable");
                return s.transitionPending === true;
            }
            if (method === "GetSettings") {
                if (s.settingsThrows) throw new Error("settings bridge unavailable");
                if (s.settingsPending) await new Promise(resolve => { window.__finishSettings = resolve; });
                return { onboarding_done: s.onboardingDone !== false, alpha_dismissed: "0.4.0", updates_auto_check: s.enabled !== false, updates_auto_install: s.autoInstall === true ? undefined : false, updates_check_on_reconnect: s.reconnectEnabled !== false, notification_matrix: {}, notify_connection: s.previousCrash === true, language: s.language || "en", bookmarks: s.bookmarks || [] };
            }
            if (method === "SessionInfoForTab") return { connected: !s.disconnected, client_id: s.clientID || "alice" };
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
            if (method === "LastCrash") return s.previousCrash ? "previous crash" : "";
            if (method === "ListTabs" || method === "ListBookmarks") return [];
            return "";
        } }) } };
    }, scenario);
    await page.goto("/");
    if (!scenario.settingsPending && !scenario.transitionQueryPending) await expect(page.locator(".login-card")).toHaveClass(/(?:^|\s)in(?:\s|$)/);
}


for (const transitionPending of [false, true]) {
    test(`delayed startup status preserves meta flows only for normal startup (${transitionPending})`, async ({ page }) => {
        await boot(page, { transitionQueryPending: true, transitionPending, onboardingDone: false, previousCrash: true, enabled: false });
        await page.waitForFunction(() => typeof window.__finishTransitionQuery === "function" && !!window.__noxaMeta);
        await page.waitForTimeout(700); // The original 600 ms startup timer has already fired.
        expect(await page.evaluate(() => window.__calls.LastCrash || 0)).toBe(0);
        await expect(page.locator(".dlg.onboarding")).toHaveCount(0);
        await page.evaluate(() => window.__finishTransitionQuery());
        await expect.poll(() => page.evaluate(() => window.__calls.ConfirmUpdateStartup)).toBe(1);
        if (transitionPending) {
            await page.waitForTimeout(450);
            expect(await page.evaluate(() => window.__calls.LastCrash || 0)).toBe(0);
            await expect(page.locator(".dlg.onboarding")).toHaveCount(0);
            await expect(page.getByRole("dialog", { name: "Completing update…", exact: true })).toBeVisible();
        } else {
            await expect.poll(() => page.evaluate(() => window.__calls.LastCrash)).toBe(1);
            await expect(page.locator(".toast").filter({ hasText: /noXa crashed last time/ })).toBeVisible();
            await expect(page.locator(".dlg.onboarding")).toBeVisible();
        }
        expect(await page.evaluate(() => window.__calls.SaveSettings || 0)).toBe(0);
    });
}

test("compatibility transition waits for native status and settings before genuine readiness", async ({ page }) => {
    await boot(page, { transitionQueryPending: true, transitionPending: true, settingsPending: true, language: "de", bookmarks: [{ name: "auto", addr: "example.test:1234", nickname: "guest", auto_connect: true }] });
    await page.waitForFunction(() => typeof window.__finishTransitionQuery === "function");
    await page.waitForTimeout(350); // Cover the former unconditional auto-connect timer.
    expect(await page.evaluate(() => window.__calls.GetSettings || 0)).toBe(0);
    expect(await page.evaluate(() => window.__calls.ConfirmUpdateStartup || 0)).toBe(0);
    await page.evaluate(() => window.__finishTransitionQuery());
    await page.waitForFunction(() => typeof window.__finishSettings === "function");
    expect(await page.evaluate(() => window.__calls.ConfirmUpdateStartup || 0)).toBe(0);
    await page.evaluate(() => window.__finishSettings());
    await expect.poll(() => page.evaluate(() => window.__calls.ConfirmUpdateStartup)).toBe(1);
    await expect(page.getByRole("dialog", { name: "Update wird abgeschlossen…", exact: true })).toBeVisible();
    expect(await page.evaluate(() => !!(window.__noxaTabs && window.__noxaFiles && window.__noxaSocial && window.__noxaMeta && window.__noxaNotify))).toBe(true);
    await page.waitForTimeout(350);
    expect(await page.evaluate(() => window.__calls.ConnectGuestBookmarkTabWithID || 0)).toBe(0);
    expect(await page.evaluate(() => window.__calls.CheckForUpdate || 0)).toBe(0);
});

test("compatibility transition blocks interaction capture and persisted preferences after ACK", async ({ page }) => {
    await boot(page, { transitionPending: true, bookmarks: [{ name: "auto", addr: "example.test:1234", nickname: "guest", auto_connect: true }] });
    await expect.poll(() => page.evaluate(() => window.__calls.ConfirmUpdateStartup)).toBe(1);
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog", { name: "Completing update…", exact: true })).toBeVisible();
    await page.evaluate(async () => {
        window.__captureCalls = 0;
        navigator.mediaDevices.getUserMedia = async () => { window.__captureCalls++; throw new Error("unexpected capture"); };
        navigator.mediaDevices.getDisplayMedia = async () => { window.__captureCalls++; throw new Error("unexpected capture"); };
        await window.__noxa.connectFromLogin();
        await window.__noxaTabs.quickConnectLast();
        await window.__noxa.ensureVoiceForChannel();
        for (const cb of window.__events.hotkey || []) await cb("quick_connect");
        for (const name of ["tray_reconnect", "tray_disconnect"]) for (const cb of window.__events[name] || []) await cb();
        const video = await import("/src/video.js");
        await video.shareToggle();
        await video.cameraToggle();
        const { updateLocalSettings } = await import("/src/settings-store.js");
        await updateLocalSettings(settings => { settings.low_bandwidth = true; });
        document.getElementById("voice-mute").click();
    });
    await page.waitForTimeout(350);
    expect(await page.evaluate(() => ({ capture: window.__captureCalls, saves: window.__calls.SaveSettings || 0, connect: window.__calls.ConnectLogin || 0, guest: window.__calls.ConnectGuestBookmarkTabWithID || 0, muted: window.__noxa.state.muted, low: !!window.__noxa.state.settings.low_bandwidth }))).toEqual({ capture: 0, saves: 0, connect: 0, guest: 0, muted: false, low: false });
});

for (const scenario of [{ settingsThrows: true }, { transitionQueryThrows: true }]) {
    test(`compatibility transition never acknowledges failed startup ${JSON.stringify(scenario)}`, async ({ page }) => {
        await boot(page, { transitionPending: true, ...scenario });
        await expect(page.getByRole("dialog", { name: "Completing update…", exact: true })).toBeVisible();
        await page.waitForTimeout(350);
        expect(await page.evaluate(() => window.__calls.ConfirmUpdateStartup || 0)).toBe(0);
        expect(await page.evaluate(() => window.__calls.CheckForUpdate || 0)).toBe(0);
    });
}

test("normal startup autoconnect waits for delayed settings then remains interactive", async ({ page }) => {
    await boot(page, { settingsPending: true, enabled: false, bookmarks: [{ name: "auto", addr: "example.test:1234", nickname: "guest", auto_connect: true }] });
    await page.waitForFunction(() => typeof window.__finishSettings === "function");
    await page.waitForTimeout(350);
    expect(await page.evaluate(() => window.__calls.ConnectGuestBookmarkTabWithID || 0)).toBe(0);
    await page.evaluate(() => window.__finishSettings());
    await expect.poll(() => page.evaluate(() => window.__calls.ConnectGuestBookmarkTabWithID)).toBe(1);
    await expect(page.locator("#update-transition")).toHaveCount(0);
    await expect(page.locator("#login-addr")).toBeEnabled();
});

test("older native bridge without transition API preserves normal startup", async ({ page }) => {
    await boot(page, { legacyBridge: true, enabled: false });
    await expect.poll(() => page.evaluate(() => window.__calls.ConfirmUpdateStartup)).toBe(1);
    await expect(page.locator("#update-transition")).toHaveCount(0);
    await expect(page.locator("#login-addr")).toBeEnabled();
});

test("replacement readiness waits for native startup and rendered login", async ({ page }) => {
    await boot(page, { settingsPending: true, enabled: false });
    await page.waitForFunction(() => typeof window.__finishSettings === "function");
    expect(await page.evaluate(() => window.__calls.ConfirmUpdateStartup)).toBeUndefined();
    await page.evaluate(() => window.__finishSettings());
    await expect(page.locator(".login-card")).toHaveClass(/(?:^|\s)in(?:\s|$)/);
    await expect.poll(() => page.evaluate(() => window.__calls.ConfirmUpdateStartup)).toBe(1);
});

test("reconnect offers an update by default independently of startup checking", async ({ page }) => {
    await boot(page, { enabled: false });
    await page.evaluate(() => window.__noxa.reconnectAutoCheck("server-a"));
    const dialog = page.getByRole("dialog", { name: "Check for updates", exact: true });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText("v0.4.1");
    expect(await page.evaluate(() => window.__calls.CheckForUpdate)).toBe(1);
});

for (const scenario of [{ reconnectEnabled: false }, { available: false }, { offline: true }, { disconnected: true }]) {
    test(`reconnect stays quiet ${JSON.stringify(scenario)}`, async ({ page }) => {
        await boot(page, { enabled: false, ...scenario });
        await page.evaluate(() => window.__noxa.reconnectAutoCheck("server-a"));
        await expect(page.locator(".update-dlg")).toHaveCount(0);
        expect(await page.evaluate(() => window.__calls.CheckForUpdate || 0)).toBe(scenario.reconnectEnabled === false || scenario.disconnected ? 0 : 1);
    });
}

for (const change of ["disconnect", "new-session", "disable"]) {
    test(`reconnect discards an available update after ${change}`, async ({ page }) => {
        await boot(page, { enabled: false, checkPending: true });
        await page.evaluate(() => { void window.__noxa.reconnectAutoCheck("server-a"); });
        await page.waitForFunction(() => typeof window.__finishCheck === "function");
        await page.evaluate(async change => {
            if (change === "disconnect") window.__scenario.disconnected = true;
            if (change === "new-session") window.__scenario.clientID = "replacement";
            if (change === "disable") window.__noxa.state.settings.updates_check_on_reconnect = false;
            window.__finishCheck();
            // Allow the asynchronous native session validation to settle.
            await new Promise(resolve => setTimeout(resolve, 50));
        }, change);
        await expect(page.locator(".update-dlg")).toHaveCount(0);
    });
}

test("simultaneous reconnects share one check and only one dialog", async ({ page }) => {
    await boot(page, { enabled: false, checkPending: true });
    await page.evaluate(() => {
        window.__checksFinished = Promise.all([
            window.__noxa.reconnectAutoCheck("server-a"),
            window.__noxa.reconnectAutoCheck("server-b"),
        ]);
    });
    await page.waitForFunction(() => typeof window.__finishCheck === "function");
    expect(await page.evaluate(() => window.__calls.CheckForUpdate)).toBe(1);
    await page.evaluate(async () => { window.__finishCheck(); await window.__checksFinished; });
    await expect(page.locator(".update-dlg")).toHaveCount(1);
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
    await expect(dialog.locator(".upd-current")).toHaveText("0.4.0");
    await expect(dialog.locator(".upd-current")).toHaveAttribute("aria-label", "current build: 0.4.0");
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
        await expect(page.locator(".upd-update")).toBeDisabled();
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
    await expect(page.locator(".upd-update")).toBeDisabled();
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
    await expect(page.locator(".upd-update")).toBeDisabled();
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
    await expect(dialog.locator(".upd-update")).toBeDisabled();
    await expect(dialog.locator(".upd-update")).toHaveText("Wird verarbeitet…");
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


test("startup installation defaults on and restarts once independently of notices", async ({ page }) => {
    await boot(page, { autoInstall: true, enabled: false, downloadPending: true, restartPending: true,
        bookmarks: [{ name: "auto", addr: "example.test:1234", nickname: "guest", auto_connect: true }] });
    await expect.poll(() => page.evaluate(() => window.__calls.DownloadAndApply)).toBe(1);
    await expect.poll(() => page.evaluate(() => window.__calls.ConfirmUpdateStartup)).toBe(1);
    await expect(page.locator(".upd-status")).toHaveText("downloading…");
    expect(await page.evaluate(() => window.__calls.ConnectGuestBookmarkTabWithID || 0)).toBe(0);
    await page.evaluate(() => { void window.__noxa.startupAutoCheck(); window.__finishDownload(); });
    await expect.poll(() => page.evaluate(() => window.__calls.ApplyAndRestart)).toBe(1);
    await page.evaluate(async () => { window.__finishRestart(); await window.__noxa.startupUpdateFinished(); });
    await page.waitForTimeout(350);
    expect(await page.evaluate(() => window.__calls.ConnectGuestBookmarkTabWithID || 0)).toBe(0);
    expect(await page.evaluate(() => window.__calls.CheckForUpdate)).toBe(1);
    expect(await page.evaluate(() => window.__calls.DownloadAndApply)).toBe(1);
});

for (const offline of [false, true]) {
    test(`reconnect remains notice-only with automatic startup installation enabled (${offline})`, async ({ page }) => {
        await boot(page, { autoInstall: true, available: false, offline });
        await page.evaluate(() => window.__noxa.startupUpdateFinished());
        await page.evaluate(async () => {
            window.__scenario.available = true;
            window.__scenario.offline = false;
            await window.__noxa.reconnectAutoCheck("server-a");
        });
        await expect(page.getByRole("button", { name: "Update now", exact: true })).toBeVisible();
        expect(await page.evaluate(() => window.__calls.DownloadAndApply || 0)).toBe(0);
        expect(await page.evaluate(() => window.__calls.ApplyAndRestart || 0)).toBe(0);
    });
}

test("cancelled startup download resumes autoconnect and retry stays manual", async ({ page }) => {
    await boot(page, { autoInstall: true, downloadPending: true,
        bookmarks: [{ name: "auto", addr: "example.test:1234", nickname: "guest", auto_connect: true }] });
    await page.getByRole("button", { name: "Cancel download", exact: true }).click();
    await expect(page.locator(".upd-status")).toHaveText("Download cancelled. You can try again.");
    await expect.poll(() => page.evaluate(() => window.__calls.ConnectGuestBookmarkTabWithID)).toBe(1);
    expect(await page.evaluate(() => window.__calls.ApplyAndRestart || 0)).toBe(0);
    await page.evaluate(() => { window.__scenario.downloadPending = false; window.__scenario.downloadError = ""; });
    await page.getByRole("button", { name: "Update now", exact: true }).click();
    await expect(page.getByRole("button", { name: "Restart now", exact: true })).toBeVisible();
    expect(await page.evaluate(() => window.__calls.ApplyAndRestart || 0)).toBe(0);
});

for (const scenario of [{ downloadError: "network failed" }, { restartError: "startup failed", restored: true }]) {
    test(`startup installation failure has no automatic retry ${JSON.stringify(scenario)}`, async ({ page }) => {
        await boot(page, { autoInstall: true, ...scenario });
        await page.evaluate(() => window.__noxa.startupUpdateFinished());
        await expect(page.locator(".upd-status")).toHaveClass(/warn/);
        await page.getByRole("button", { name: "Close", exact: true }).click();
        await page.evaluate(async () => {
            await window.__noxa.startupAutoCheck();
            await window.__noxa.reconnectAutoCheck("server-a");
        });
        await expect(page.getByRole("button", { name: "Update now", exact: true })).toBeVisible();
        expect(await page.evaluate(() => window.__calls.DownloadAndApply)).toBe(1);
        expect(await page.evaluate(() => window.__calls.ApplyAndRestart || 0)).toBe(scenario.restartError ? 1 : 0);
    });
}

test("disabling installation during startup check prevents download", async ({ page }) => {
    await boot(page, { autoInstall: true, enabled: false, checkPending: true });
    await page.waitForFunction(() => typeof window.__finishCheck === "function");
    await page.evaluate(async () => {
        window.__noxa.state.settings.updates_auto_install = false;
        window.__finishCheck();
        await window.__noxa.startupUpdateFinished();
    });
    expect(await page.evaluate(() => window.__calls.DownloadAndApply || 0)).toBe(0);
    await expect(page.locator(".update-dlg")).toHaveCount(0);
});

test("concurrent startup and reconnect cannot duplicate automatic installation", async ({ page }) => {
    await boot(page, { autoInstall: true, checkPending: true });
    await page.waitForFunction(() => typeof window.__finishCheck === "function");
    await page.evaluate(() => {
        window.__reconnects = Promise.all([window.__noxa.reconnectAutoCheck("server-a"), window.__noxa.reconnectAutoCheck("server-b")]);
        window.__finishCheck();
    });
    await page.evaluate(() => window.__reconnects);
    expect(await page.evaluate(() => window.__calls.CheckForUpdate)).toBe(1);
    expect(await page.evaluate(() => window.__calls.DownloadAndApply)).toBe(1);
    expect(await page.evaluate(() => window.__calls.ApplyAndRestart)).toBe(1);
});


test("violet core uses real byte counts and waits for verified completion plus three seconds", async ({ page }, testInfo) => {
    await boot(page, { downloadPending: true });
    await expect(page.locator(".upd-update")).toBeVisible();
    await page.clock.install({ time: new Date("2026-10-15T00:00:00Z") });
    await page.clock.pauseAt(new Date("2026-10-15T00:00:01Z"));
    await page.locator(".upd-update").click();
    await page.evaluate(() => {
        for (const cb of window.__events.update_progress || []) cb(25, { downloaded_bytes: 524288, total_bytes: 1048576, bytes_per_second: 262144 });
    });
    await expect(page.locator(".upd-pct")).toHaveText("50%");
    await expect(page.locator(".upd-bytes")).toHaveText("512.00 KiB / 1.00 MiB");
    await expect(page.locator(".upd-blocks")).toHaveText("40 / 80 blocks");
    await page.evaluate(() => {
        for (const cb of window.__events.update_progress || []) cb(100, { downloaded_bytes: 1048576, total_bytes: 1048576, bytes_per_second: 0 });
        for (const cb of window.__events.update_phase || []) cb("ready");
    });
    await page.clock.runFor(10000);
    expect(await page.evaluate(() => window.__calls.ApplyAndRestart || 0)).toBe(0);
    await page.evaluate(() => window.__finishDownload());
    await expect(page.locator(".upd-countdown")).toHaveText("Restarting automatically in 3 s…");
    await page.locator(".update-dlg").screenshot({ path: testInfo.outputPath("violet-countdown.png") });
    await page.clock.runFor(2999);
    expect(await page.evaluate(() => window.__calls.ApplyAndRestart || 0)).toBe(0);
    await page.clock.runFor(1);
    await expect.poll(() => page.evaluate(() => window.__calls.ApplyAndRestart)).toBe(1);
    await page.clock.runFor(10000);
    expect(await page.evaluate(() => window.__calls.ApplyAndRestart)).toBe(1);
});

test("closing the verified countdown cancels auto restart and retains manual restart", async ({ page }) => {
    await boot(page, { downloadPending: true });
    await expect(page.locator(".upd-update")).toBeVisible();
    await page.clock.install();
    await page.locator(".upd-update").click();
    await page.evaluate(() => window.__finishDownload());
    await expect(page.locator(".upd-countdown")).toBeVisible();
    await page.locator(".upd-close").click();
    await page.clock.runFor(5000);
    expect(await page.evaluate(() => window.__calls.ApplyAndRestart || 0)).toBe(0);
    await page.evaluate(() => window.__noxa.checkForUpdatesInteractive());
    await expect(page.locator(".upd-update")).toHaveText("Restart now");
    await page.locator(".upd-update").click();
    await expect.poll(() => page.evaluate(() => window.__calls.ApplyAndRestart)).toBe(1);
});

test("removed dialog cannot restart from a late successful staging result", async ({ page }) => {
    await boot(page, { downloadPending: true });
    await expect(page.locator(".upd-update")).toBeVisible();
    await page.clock.install();
    await page.locator(".upd-update").click();
    await page.evaluate(() => { document.querySelector(".update-dlg").parentElement.remove(); window.__finishDownload(); });
    await page.clock.runFor(10000);
    expect(await page.evaluate(() => window.__calls.ApplyAndRestart || 0)).toBe(0);
    expect(await page.evaluate(() => (window.__events.update_progress || []).length)).toBe(0);
});


for (const theme of ["arctic", "dark", "light", "hc"]) {
    test(`violet offer and measured core remain readable in ${theme}`, async ({ page }, testInfo) => {
        await boot(page, { downloadPending: true });
        await expect(page.locator(".upd-update")).toBeVisible();
        await page.evaluate(theme => { document.documentElement.dataset.theme = theme; }, theme);
        expect((await page.locator(".update-dlg").boundingBox()).width).toBe(590);
        await page.locator(".update-dlg").screenshot({ path: testInfo.outputPath(`violet-${theme}-offer.png`) });
        await page.locator(".upd-update").click();
        await page.evaluate(() => {
            for (const cb of window.__events.update_progress || []) cb(50, { downloaded_bytes: 20971520, total_bytes: 41943040, bytes_per_second: 8388608 });
        });
        await expect(page.locator(".upd-bits")).toHaveText("67.11 Mbit/s");
        await expect(page.locator(".upd-progress")).toHaveAttribute("aria-valuenow", "50");
        await page.locator(".update-dlg").screenshot({ path: testInfo.outputPath(`violet-${theme}-download.png`) });
        await page.setViewportSize({ width: 390, height: 780 });
        const box = await page.locator(".update-dlg").boundingBox();
        expect(box.width).toBeLessThanOrEqual(358);
        expect(box.x).toBeGreaterThanOrEqual(0);
        await expect(page.locator(".upd-cancel")).toBeVisible();
        await page.locator(".update-dlg").screenshot({ path: testInfo.outputPath(`violet-${theme}-mobile.png`) });
        expect(await page.locator(".upd-body").evaluate(el => getComputedStyle(el).animationName)).toBe("none");
    });
}

test("bloom runs only once on transition and observers retire with the dialog", async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "no-preference" });
    await boot(page, { downloadPending: true });
    await expect(page.locator(".upd-update")).toBeVisible();
    await page.locator(".upd-update").click();
    expect(await page.locator(".upd-body").evaluate(el => getComputedStyle(el).animationIterationCount)).toBe("1");
    await page.waitForTimeout(600);
    expect(await page.locator(".upd-body").evaluate(el => el.getAnimations().filter(a => a.playState === "running").length)).toBe(0);
    await page.locator(".upd-cancel").click();
    await expect(page.locator(".upd-status")).toHaveText("Download cancelled. You can try again.");
    await page.locator(".upd-close").click();
    expect(await page.evaluate(() => (window.__events.update_progress || []).length)).toBe(0);
});
