import { expect, test } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__fileEvents = {};
        window.__fileCalls = [];
        window.runtime = {
            EventsOn(name, callback) { (window.__fileEvents[name] ||= []).push(callback); return () => {}; },
            EventsEmit() {}, WindowIsFullscreen: async () => false,
        };
        const settings = { language: "en", onboarding_done: true, alpha_dismissed: "test", bookmarks: [], chat_max_lines: 200, activation_mode: "ptt" };
        window.__fileList = async () => ({ entries: [{ name: "initial.txt", size: 5 }], folders: [] });
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            window.__fileCalls.push([method, ...args]);
            if (method === "GetSettings") return structuredClone(settings);
            if (["ListTabs", "ListIdentities", "GetPermissions", "SubscriptionsForTab"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (["Connected", "IsGuest", "GamingOverlayAvailable"].includes(method)) return false;
            if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
            if (method === "ConversationForTab") return { conversations: [] };
            if (method === "DiscussionForTab") return { threads: [], forum: false };
            if (method === "FileListForTab") return window.__fileList(...args);
            if (method === "DownloadPath") return "C:/audit/report.bin";
            return "";
        }; } }) } };
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxaFiles);
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        Object.assign(window.__noxa.state, { activeTabID: "files-a", myChannelID: 42, channels: [{ ChannelID: 42, Name: "Files" }], lastConnect: { addr: "files.test:12333" } });
    });
    await page.getByRole("tab", { name: "Files", exact: true }).click();
    await expect(page.locator(".fb-filename")).toHaveText("initial.txt");
});

test("transfer progress keeps the focused Cancel button stable and usable", async ({ page }) => {
    await page.evaluate(() => {
        window.__auditProgress = (transferred) => {
            for (const callback of window.__fileEvents.ft_progress || []) callback({ id: "active-file", name: "large.bin", direction: "download", status: "active", total: 1000, transferred, bytes_per_sec: 100 });
        };
        window.__auditProgress(100);
    });
    await page.locator(".fb-transfers").click();
    const cancel = page.getByRole("button", { name: "cancel transfer", exact: true });
    await cancel.focus();
    await cancel.evaluate(button => { window.__originalCancel = button; });
    await page.evaluate(() => { for (let n = 200; n <= 800; n += 100) window.__auditProgress(n); });
    await expect(cancel).toBeFocused();
    expect(await cancel.evaluate(button => button === window.__originalCancel)).toBe(true);
    await expect(page.locator(".tr-meta")).toContainText("80%");
    await cancel.press("Enter");
    await expect.poll(() => page.evaluate(() => window.__fileCalls.filter(call => call[0] === "CancelTransferForTab"))).toEqual([["CancelTransferForTab", "files-a", "active-file"]]);
});

for (const stale of ["result", "error"]) test(`late file refresh ${stale} cannot replace a newer folder listing`, async ({ page }) => {
    await page.evaluate(() => {
        let calls = 0;
        window.__fileList = async () => {
            if (++calls === 1) return new Promise((resolve, reject) => { window.__finishOldList = resolve; window.__failOldList = reject; });
            return { entries: [{ name: "newest.txt", size: 10 }], folders: [] };
        };
    });
    await page.getByRole("button", { name: "Refresh files", exact: true }).click();
    await page.waitForFunction(() => typeof window.__finishOldList === "function");
    await page.getByRole("button", { name: "Refresh files", exact: true }).click();
    await expect(page.locator(".fb-filename")).toHaveText("newest.txt");
    await page.evaluate(stale => {
        if (stale === "error") window.__failOldList(new Error("Earlier request timed out"));
        else window.__finishOldList({ entries: [{ name: "outdated.txt", size: 3 }], folders: [] });
    }, stale);
    await expect(page.locator(".fb-filename")).toHaveText("newest.txt");
    await expect(page.locator(".fb-list")).not.toContainText("Earlier request timed out");
});

for (const width of [420, 1280]) test(`transfer recovery controls and errors fit at ${width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 960 });
    await page.getByRole("button", { name: "Download", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__fileCalls.some(call => call[0] === "DownloadFileProgressForTab"))).toBe(true);
    await page.evaluate(() => {
        const id = window.__fileCalls.find(call => call[0] === "DownloadFileProgressForTab")[2];
        for (const callback of window.__fileEvents.ft_progress || []) {
            callback({ id, direction: "download", name: "quarterly-audit-report-with-a-long-file-name.bin", status: "error", error: "another download is already writing to this destination", total: 3145728, transferred: 0 });
            callback({ id: "large-active", direction: "download", name: "large-in-progress.bin", status: "active", total: 3145728, transferred: 2000000, resumed: 1500000, bytes_per_sec: 128 });
        }
    });
    await page.locator(".fb-transfers").click();
    await expect(page.getByRole("button", { name: "Resume download", exact: true })).toBeVisible();
    const bounds = await page.locator(".tr-list").evaluate(list => ({
        overflow: list.scrollWidth - list.clientWidth,
        rows: [...list.querySelectorAll(".tr-row")].map(row => ({
            nameWidth: row.querySelector(".tr-name").getBoundingClientRect().width,
            statusOverflow: row.querySelector(".tr-status").scrollWidth - row.querySelector(".tr-status").clientWidth,
            buttonOutside: row.querySelector("button").getBoundingClientRect().right - list.getBoundingClientRect().right,
        })),
    }));
    expect(bounds.overflow).toBeLessThanOrEqual(1);
    for (const row of bounds.rows) {
        expect(row.nameWidth).toBeGreaterThanOrEqual(60);
        expect(row.statusOverflow).toBeLessThanOrEqual(1);
        expect(row.buttonOutside).toBeLessThanOrEqual(0);
    }
    await page.screenshot({ path: testInfo.outputPath(`transfer-recovery-${width}.png`) });
});
