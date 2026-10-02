import { expect, test } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__securityCalls = [];
        window.__securityEntries = [
            { id: "personal", name: "Personal", unique_id: "public-personal-identity", active: true, protection: "dpapi", security_level: 4, exported_at: "2026-10-02 13:41:58" },
            { id: "work", name: "Work", unique_id: "public-work-identity", active: false, protection: "plaintext", security_level: 0 },
        ];
        const settings = { language: "en", onboarding_done: true, alpha_dismissed: "test", bookmarks: [], chat_max_lines: 200, volume: 100, activation_mode: "ptt", identity_key_protection: "auto" };
        window.runtime = { EventsOn: () => () => {}, EventsEmit() {}, WindowIsFullscreen: async () => false, ClipboardSetText: async () => true };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            const entries = window.__securityEntries;
            if (method === "GetSettings") return structuredClone(settings);
            if (method === "SaveSettings") { window.__securityCalls.push({ method, args }); Object.assign(settings, args[0]); return ""; }
            if (method === "ListIdentities") return structuredClone(entries);
            if (method === "ApplyIdentityProtection") {
                window.__securityCalls.push({ method, args });
                if (window.__protectionFailure) throw new Error("OS store unavailable");
                for (const entry of entries) entry.protection = settings.identity_key_protection === "off" ? "plaintext" : "dpapi";
                return;
            }
            if (["RenameIdentity", "SwitchIdentity", "DeleteIdentity", "CreateIdentity", "ResetIdentity", "BackupIdentity", "RestoreIdentity", "ImproveIdentityLevel", "CancelIdentityLevel"].includes(method)) {
                window.__securityCalls.push({ method, args });
                if (window.__identityFailure) return "disk write failed";
                const entry = entries.find(item => item.id === args[0]);
                if (method === "RenameIdentity") entry.name = args[1];
                if (method === "CreateIdentity") entries.push({ id: "new", name: args[0], unique_id: "new-public-key", active: false, protection: "dpapi", security_level: 0 });
                if (method === "SwitchIdentity") for (const item of entries) item.active = item === entry;
                if (method === "DeleteIdentity") window.__securityEntries = entries.filter(item => item !== entry);
                if (method === "ResetIdentity") { entry.unique_id = "replacement-key"; entry.exported_at = ""; }
                if (method === "BackupIdentity") { if (window.__cancelExport) return false; entry.exported_at = "2026-10-02 14:00:00"; return true; }
                if (method === "RestoreIdentity") return !window.__cancelImport;
                if (method === "ImproveIdentityLevel") return new Promise(resolve => { window.__finishLevel = resolve; });
                if (method === "CancelIdentityLevel") { window.__finishLevel({ level: 5, cancelled: true }); return true; }
                return "";
            }
            if (["ListTabs", "GetPermissions", "SubscriptionsForTab"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (["Connected", "IsGuest", "GamingOverlayAvailable"].includes(method)) return false;
            if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
            if (method === "ConversationForTab") return { conversations: [] };
            if (method === "DiscussionForTab") return { threads: [], forum: false };
            return "";
        }; } }) } };
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.openSettings);
    await page.evaluate(() => window.__noxa.openSettings("security"));
    await expect(page.locator(".identity-card")).toHaveCount(2);
    await expect(page.locator("#set-apply")).toBeEnabled();
});

test("security cards show actual protection and export history without horizontal scrolling", async ({ page }, testInfo) => {
    const selected = page.locator(".identity-selected");
    await expect(selected).toContainText("Used on next connection");
    await expect(selected).toContainText("Protected by your Windows account");
    await expect(selected).toContainText("Last exported");
    const work = page.getByRole("region", { name: "Work", exact: true });
    await expect(work).toContainText("Never exported");
    await expect(work).toContainText("Stored without OS protection");
    await expect(page.getByRole("button", { name: "Replace identity key…", exact: true }).first()).toBeHidden();
    await page.screenshot({ path: testInfo.outputPath("security-desktop.png") });
    await page.setViewportSize({ width: 500, height: 780 });
    expect(await page.locator("#settings-content").evaluate(node => node.scrollWidth <= node.clientWidth + 1)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath("security-narrow.png") });
});

test("identity labels save immediately and errors stay inside settings", async ({ page }) => {
    await page.getByLabel("More actions for Personal", { exact: true }).click();
    await page.getByRole("button", { name: "Rename local label", exact: true }).first().click();
    const prompt = page.getByRole("dialog", { name: "Rename local label", exact: true });
    await expect(prompt).toContainText("does not change the name other users see");
    await prompt.getByRole("textbox").fill("Daniel’s identity");
    await prompt.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.locator(".security-status")).toHaveText("Local label saved.");
    await expect(page.locator(".identity-selected h4")).toHaveText("Daniel’s identity");
    await expect(page.locator(".settings-save-status")).toHaveText("All changes applied");
    await page.evaluate(() => { window.__identityFailure = true; });
    await page.getByRole("button", { name: "Use identity", exact: true }).click();
    await expect(page.locator(".security-error")).toContainText("disk write failed");
    await expect(page.locator(".identity-selected h4")).toHaveText("Daniel’s identity");
});

test("creating an identity only selects it after Use identity", async ({ page }) => {
    await page.getByRole("button", { name: "Create identity…", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Create identity…", exact: true });
    await dialog.getByRole("textbox").fill("Gaming");
    await dialog.getByRole("button", { name: "OK", exact: true }).click();
    await expect(page.locator(".security-status")).toContainText("Identity created. Choose Use identity");
    await expect(page.locator(".identity-selected h4")).toHaveText("Personal");
    await page.getByRole("region", { name: "Gaming", exact: true }).getByRole("button", { name: "Use identity", exact: true }).click();
    await expect(page.locator(".identity-selected h4")).toHaveText("Gaming");
    await expect(page.locator(".identity-selected h4")).toBeFocused();
    await expect(page.locator(".security-status")).toHaveText("Identity selected. Reconnect to use it.");
});

test("backup and import cancellation never report success", async ({ page }) => {
    await page.evaluate(() => { window.__cancelExport = true; window.__cancelImport = true; });
    const backup = page.getByRole("region", { name: "Work", exact: true }).getByRole("button", { name: "Back up identity…", exact: true });
    await backup.click();
    const warning = page.getByRole("dialog", { name: "Back up identity…", exact: true });
    await expect(warning).toContainText("without a password");
    await warning.getByRole("button", { name: "Back up identity…", exact: true }).click();
    await expect(page.locator(".security-status")).toHaveText("Cancelled. No changes made.");
    await expect(page.getByRole("region", { name: "Work", exact: true })).toContainText("Never exported");
    await page.getByRole("button", { name: "Import backup…", exact: true }).click();
    await expect(page.locator(".security-status")).toHaveText("Cancelled. No changes made.");
    await page.evaluate(() => { window.__cancelExport = false; });
    await backup.click();
    await warning.getByRole("button", { name: "Back up identity…", exact: true }).click();
    await expect(page.locator(".security-status")).toContainText("Backup exported");
    await expect(page.getByRole("region", { name: "Work", exact: true })).toContainText("Last exported");
});

test("Apply updates existing key files and a failed update remains retryable", async ({ page }) => {
    await page.getByLabel("Storage preference", { exact: true }).selectOption("off");
    await expect(page.getByText("Pending: apply to update existing identity files.", { exact: true })).toBeVisible();
    await page.evaluate(() => { window.__protectionFailure = true; });
    await page.locator("#set-apply").click();
    await expect(page.locator(".settings-save-status")).toContainText("Preferences were saved, but some identity files could not be updated");
    await expect(page.locator(".identity-selected")).toContainText("Protected by your Windows account");
    await page.evaluate(() => { window.__protectionFailure = false; });
    await page.locator("#set-apply").click();
    await expect(page.locator(".settings-save-status")).toContainText("Identity files updated");
    await expect(page.locator(".identity-selected")).toContainText("Stored without OS protection");
    await expect(page.getByText("Pending: apply to update existing identity files.", { exact: true })).toBeHidden();
    expect(await page.evaluate(() => window.__securityCalls.filter(call => call.method === "ApplyIdentityProtection").length)).toBe(2);
});

test("proof-of-work calculation shows progress and can be stopped", async ({ page }) => {
    const card = page.locator(".identity-selected");
    await card.getByText("Advanced identity tools", { exact: true }).click();
    await card.getByLabel("Target level (1–40)", { exact: true }).fill("30");
    await card.getByRole("button", { name: "Calculate level", exact: true }).click();
    await expect(page.getByRole("progressbar")).toBeVisible();
    await expect(page.locator(".security-progress")).toContainText("Calculating for Personal");
    await expect(page.locator("#set-apply")).toBeDisabled();
    await page.getByRole("button", { name: "Stop calculation", exact: true }).click();
    await expect(page.locator(".security-status")).toHaveText("Calculation stopped at level 5. Best result saved.");
    await expect(page.getByRole("progressbar")).toBeHidden();
    await expect(page.locator("#set-apply")).toBeEnabled();
});

test("identity reset names and targets the confirmed key", async ({ page }) => {
    const card = page.locator(".identity-selected");
    await card.getByText("Advanced identity tools", { exact: true }).click();
    await card.getByText("Identity reset and deletion", { exact: true }).click();
    await card.getByRole("button", { name: "Replace identity key…", exact: true }).click();
    const confirmation = page.getByRole("dialog", { name: "Replace identity key…", exact: true });
    await expect(confirmation).toContainText("Personal");
    await expect(confirmation).toContainText("roles and account");
    await confirmation.getByRole("button", { name: "Replace key", exact: true }).click();
    await expect(page.locator(".security-status")).toContainText("Identity key replaced");
    expect(await page.evaluate(() => window.__securityCalls.find(call => call.method === "ResetIdentity").args)).toEqual(["personal", "public-personal-identity"]);
});

test("unencrypted connection warning stays visible when advanced settings collapse", async ({ page }) => {
    await page.getByText("Advanced developer settings", { exact: true }).click();
    await page.getByLabel("Allow unencrypted connections", { exact: true }).check();
    await page.getByText("Advanced developer settings", { exact: true }).click();
    await expect(page.getByText(/Unencrypted connections are allowed by this preference/)).toBeVisible();
    await page.locator("#set-cancel").click();
    expect(await page.evaluate(() => window.__securityCalls.some(call => call.method === "SaveSettings"))).toBe(false);
});
