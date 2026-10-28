import { expect, test } from "@playwright/test";

// Review b8 P3-3c: a guest's accepted certificate change that cannot be
// saved shows an error instead of an unhandled rejection behind an
// unchanged banner; the old pin stays (fail closed).
test("a failed certificate re-pin shows an error", async ({ page }) => {
    const rejections = [];
    page.on("pageerror", error => rejections.push(error.message));
    await page.goto("http://127.0.0.1:5175/app/ui-tests/layout.html?view=chat");
    const result = page.evaluate(async () => {
        const { reviewCertificateChange } = await import("/app/src/ui/message-view.js");
        const status = {
            rotation: { previous: "tls-sha256:" + "ab".repeat(32), announced: "tls-sha256:" + "cd".repeat(32) },
            confirm: async () => { throw Object.assign(new Error("IndexedDB write failed"), { code: "secure_unavailable" }); },
        };
        return reviewCertificateChange(status);
    });
    await page.getByRole("dialog").getByRole("button", { name: "Accept the new certificate" }).click();
    expect(await result).toBe(false);
    await expect(page.locator("#toasts .toast-error")).toContainText("could not be saved");
    expect(rejections).toEqual([]);
});
