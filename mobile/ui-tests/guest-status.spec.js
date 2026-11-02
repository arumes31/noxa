import { test, expect } from "@playwright/test";
for (const view of ["servers", "members", "profile"]) {
    test(`guest account badge is explicit in ${view}, independent of Member permissions`, async ({ page }) => {
        await page.setViewportSize({ width: 320, height: 844 });
        await page.goto("http://127.0.0.1:5175/app/ui-tests/layout.html?view=servers");
        await expect(page.locator("html")).toHaveAttribute("data-ready", "true");
        await page.evaluate(async view => {
            const { app } = await import("/app/src/app.js");
            const client = app.session.clients()[1];
            Object.assign(client, { is_guest: true, on_mobile: true, roles: [{ id: 3, name: "Member", position: 1 }] });
            app.session.clients()[0].nickname = "Guest by name";
            if (view === "servers") (await import("/app/src/ui/servers.js")).renderServers(document.getElementById("app"));
            if (view === "members") (await import("/app/src/ui/members.js")).renderMembers(document.getElementById("app"));
            if (view === "profile") (await import("/app/src/ui/profile.js")).openProfile({ clientID: client.client_id });
        }, view);
        const badge = page.getByLabel("Guest account", { exact: true });
        await expect(badge).toHaveText("Guest");
        await expect(badge).toBeVisible();
        expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(320);
        if (view !== "servers") await expect(page.locator('.role-chip').filter({ hasText: 'Member' })).toBeVisible();
        expect(await page.evaluate(async () => (await import("/app/src/app.js")).app.session.clients()[1].roles[0].name)).toBe("Member");
    });
}
