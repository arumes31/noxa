import { expect, test } from "./fixtures.js";

test("device rename failure leaves Save and Cancel usable for retry", async ({ page }) => {
    await page.route("**/device-dialog-test", route => route.fulfill({ contentType: "text/html", body: "<div id='devices'></div>" }));
    await page.goto("/device-dialog-test");
    await page.evaluate(async () => {
        window.renameCalls = 0;
        window.__noxa = { state: { clients: [], myClientID: "self" }, toast() {} };
        window.go = { main: { App: { RenameDeviceForTab: async () => {
            if (++window.renameCalls === 1) throw new Error("temporarily disconnected");
        } } } };
        const { mountMyDevices } = await import("/src/my-devices.js");
        window.deviceView = mountMyDevices(document.getElementById("devices"), { tabID: "a", fetchDevices: false });
        window.deviceView.setDevices([{ device_id: "desktop", name: "Office", kind: "desktop", current: true }]);
    });
    await page.locator(".my-device-name").click();
    await page.locator(".my-device-rename input").fill("Studio");
    await page.locator(".my-device-rename button[type=submit]").click();
    await expect(page.locator(".my-device-rename button[type=submit]")).toBeEnabled();
    await expect(page.locator(".my-device-rename .dlg-cancel")).toBeEnabled();
    await page.locator(".my-device-rename button[type=submit]").click();
    await expect(page.locator(".my-device-name")).toHaveText("Studio");
    expect(await page.evaluate(() => window.renameCalls)).toBe(2);
    await page.evaluate(() => window.deviceView.dispose());
});
