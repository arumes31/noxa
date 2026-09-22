import { expect, test } from "@playwright/test";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__noxa = { state: { serverGeneration: 1 } };
        window.__auditCalls = [];
        window.__auditResponse = { capabilities: [{ key: "speak", en: "Speak", de: "Sprechen" }], entries: [
            { id: 8, created_at: 1789900000, restricted: true },
            { id: 7, structured: true, created_at: 1789900000, actor: "Owner", target: "authorization", action: "roles.role_update", detail: JSON.stringify({ version: 1, revision: 9,
                before: { name: "Member", permissions: [] }, after: { name: "Helper <img src=x onerror=alert(1)>", permissions: ["speak"] } }) },
        ] };
        window.go = { main: { App: { AuditLog: async (before) => {
            window.__auditCalls.push(before);
            if (window.__auditGate) await window.__auditGate;
            if (window.__auditFail) throw new Error("denied");
            return before ? { entries: [] } : structuredClone(window.__auditResponse);
        } } } };
        window.__noxa.state.activeTabID = "server-a";
        window.__nativeTabID = "server-a";
        window.__auditTabCalls = [];
        const app = window.go.main.App, original = app.AuditLog;
        app.AuditLogForTab = async (tabID, before, limit) => {
            window.__auditTabCalls.push({ tabID, before, limit });
            if (tabID !== window.__nativeTabID) throw new Error("server tab changed; refresh");
            return original(before);
        };
        app.AuditLog = () => { throw new Error("unscoped native call"); };
    });
    await page.route("**/__audit_test__", (route) => route.fulfill({ contentType: "text/html", body: `<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"><link rel="stylesheet" href="/src/style.css"></head><body><button id="launch">Audit</button><script type="module">
        import { openAuditLog } from '/src/audit-ui.js';
        import { initModalSystem } from '/src/modal.js';
        import { setLanguage } from '/src/i18n.js';
        initModalSystem(); window.setTestLanguage = setLanguage;
        document.querySelector('#launch').onclick = openAuditLog; window.ready = true;
    </script></body></html>` }));
    await page.goto("/__audit_test__");
    await page.waitForFunction(() => window.ready);
});

test("native tab activation cannot mix another server into audit pagination", async ({ page }) => {
    await page.getByRole("button", { name: "Audit", exact: true }).click();
    await expect(page.getByText("Protected record", { exact: true })).toBeVisible();
    await page.evaluate(() => { window.__nativeTabID = "server-b"; });
    await page.getByRole("button", { name: "Load older", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("Could not load records");
    expect(await page.evaluate(() => window.__auditCalls)).toEqual([0]);
    expect(await page.evaluate(() => window.__auditTabCalls)).toEqual([
        { tabID: "server-a", before: 0, limit: 50 }, { tabID: "server-a", before: 7, limit: 50 },
    ]);
    await expect(page.locator(".audit-record")).toHaveCount(0);
});

test("audit shows readable before/after, redaction, safe text and page cursor", async ({ page }) => {
    await page.getByRole("button", { name: "Audit", exact: true }).click();
    await expect(page.getByText("Protected record", { exact: true })).toBeVisible();
    await expect(page.getByRole("columnheader", { name: "Before", exact: true })).toBeVisible();
    await expect(page.getByRole("cell", { name: "Speak", exact: true })).toBeVisible();
    await expect(page.locator(".audit-record img")).toHaveCount(0);
    await expect(page.getByText("Helper <img src=x onerror=alert(1)>", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Load older", exact: true }).click();
    await expect(page.getByRole("button", { name: "Load older", exact: true })).toBeDisabled();
    expect(await page.evaluate(() => window.__auditCalls)).toEqual([0, 7]);
});

test("German compact audit supports keyboard search and close", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 760 });
    await page.evaluate(() => window.setTestLanguage("de"));
    await page.getByRole("button", { name: "Audit", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Prüfprotokoll", exact: true })).toBeVisible();
    const search = page.getByRole("searchbox", { name: "Einträge filtern", exact: true });
    await search.fill("Helper");
    await expect(page.getByText("Geschützter Eintrag", { exact: true })).toHaveCount(0);
    await expect(page.getByRole("cell", { name: "Sprechen", exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: "test-results/audit-compact.png" });
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Audit", exact: true })).toBeFocused();
});

test("late replies from another server are discarded", async ({ page }) => {
    await page.evaluate(() => { window.__auditGate = new Promise((resolve) => { window.__finishAudit = resolve; }); });
    await page.getByRole("button", { name: "Audit", exact: true }).click();
    await page.evaluate(() => { window.__noxa.state.serverGeneration++; window.__finishAudit(); });
    await expect(page.getByText("Member", { exact: true })).toHaveCount(0);
});

test("denied audit can be retried without keeping stale content", async ({ page }) => {
    await page.evaluate(() => { window.__auditFail = true; });
    await page.getByRole("button", { name: "Audit", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("Could not load records");
    await page.evaluate(() => { window.__auditFail = false; });
    await page.getByRole("button", { name: "Load older", exact: true }).click();
    await expect(page.getByRole("cell", { name: "Member", exact: true })).toBeVisible();
});

test("historical JSON stays plain text and unchanged role identity remains visible", async ({ page }) => {
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.evaluate(() => {
        window.__auditResponse.entries = [
            { id: 10, created_at: 1789900000, actor: "Old owner", target: "group", action: "group_rename", detail: '{"version":1,"before":[null]}' },
            { id: 9, structured: true, created_at: 1789900000, actor: "Owner", target: "authorization", action: "roles.role_update",
                detail: JSON.stringify({ version: 1, before: { id: 20, name: "Helper", permissions: [] }, after: { id: 20, name: "Helper", permissions: ["speak"] } }) },
        ];
    });
    await page.getByRole("button", { name: "Audit", exact: true }).click();
    await expect(page.getByText('{"version":1,"before":[null]}', { exact: true })).toBeVisible();
    await expect(page.getByText("Target: Role Helper (#20)", { exact: true })).toBeVisible();
    await expect(page.getByRole("cell", { name: "Speak", exact: true })).toBeVisible();
    expect(errors).toEqual([]);
});
