import { expect, test } from "@playwright/test";
const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");

test("guest can sign in, reload and sign out without linking a device", async ({ page }) => {
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    await page.addInitScript(() => {
        window.__clearedBadges = 0;
        navigator.clearAppBadge = async () => { window.__clearedBadges++; };
    });
    await page.goto(`${server.webURL}/app/`);
    await page.locator("summary").filter({ hasText: "Continue as guest" }).click();
    await page.getByLabel("Display name", { exact: true }).fill("Guest visitor");
    await page.getByRole("button", { name: "Continue as guest", exact: true }).click();
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
    await page.getByRole("link", { name: "You", exact: true }).click();
    await expect(page.getByText(/^Guest visitor(#M\d+)?$/)).toBeVisible();
    await expect(page.getByRole("button", { name: "Save login as passkey" })).toHaveCount(0);
    await page.evaluate(() => {
        localStorage.setItem("noxa.settings", JSON.stringify({ theme: "arctic", contacts: [{ uid: "private-contact", name: "Private contact" }], notes: { "private-contact": "Private note" }, presenceMessage: "Private status", autoAwayMessage: "Private away status" }));
        localStorage.setItem("noxa.servers", JSON.stringify([{ origin: "https://example.test", name: "Saved server" }]));
    });
    await page.reload();
    await expect(page.getByText(/^Guest visitor(#M\d+)?$/)).toBeVisible();
    await page.evaluate(async () => {
        window.__clearedBadges = 0;
        localStorage.setItem("noxa.callReminders", JSON.stringify([{ uid: "private-peer", name: "Private reminder", at: Date.now() + 600000 }]));
        const db = await new Promise((resolve, reject) => {
            const request = indexedDB.open("noxa"); request.onsuccess = () => resolve(request.result); request.onerror = () => reject(request.error);
        });
        try {
            await new Promise((resolve, reject) => {
                const tx = db.transaction(["calls", "saved"], "readwrite");
                tx.objectStore("calls").put({ private: "old calls" }, "private-peer");
                tx.objectStore("saved").put({ private: "old references" }, "saved-messages");
                tx.oncomplete = resolve; tx.onerror = () => reject(tx.error);
            });
        } finally { db.close(); }
    });
    await page.getByRole("button", { name: "Sign out of this device", exact: true }).click();
    await page.getByRole("button", { name: "Sign out of this device", exact: true }).last().click();
    await expect(page.getByRole("button", { name: "Sign in with passkey" })).toBeVisible();
    const stored = await page.evaluate(() => ({ settings: JSON.parse(localStorage.getItem("noxa.settings")), servers: localStorage.getItem("noxa.servers") }));
    expect(stored.settings.theme).toBe("arctic");
    expect(stored.settings.contacts).toBeUndefined();
    expect(stored.settings.notes).toBeUndefined();
    expect(stored.settings.presenceMessage).toBeUndefined();
    expect(stored.settings.autoAwayMessage).toBeUndefined();
    expect(await page.evaluate(() => window.__clearedBadges)).toBeGreaterThan(0);
    expect(stored.servers).toContain("Saved server");
    await page.reload();
    await expect(page.getByRole("button", { name: "Sign in with passkey" })).toBeVisible();
    const remaining = await page.evaluate(async () => {
        const db = await new Promise((resolve, reject) => {
            const request = indexedDB.open("noxa"); request.onsuccess = () => resolve(request.result); request.onerror = () => reject(request.error);
        });
        try {
            const counts = {};
            for (const name of ["calls", "saved"]) counts[name] = await new Promise((resolve, reject) => {
                const request = db.transaction(name).objectStore(name).count();
                request.onsuccess = () => resolve(request.result); request.onerror = () => reject(request.error);
            });
            return { ...counts, reminders: localStorage.getItem("noxa.callReminders") };
        } finally { db.close(); }
    });
    expect(remaining).toEqual({ calls: 0, saved: 0, reminders: null });
    expect(errors).toEqual([]);
});
