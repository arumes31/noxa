import { expect, test } from "@playwright/test";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");

test("Connect loads without the signed-in screens' chunks", async ({ page }) => {
    const scripts = [];
    page.on("request", request => {
        if (request.resourceType() === "script") scripts.push(new URL(request.url()).pathname);
    });
    await page.goto(`${server.webURL}/app/`);
    await expect(page.getByRole("button", { name: "Continue" })).toBeVisible();
    expect(scripts.some(path => /\/connect-[\w-]+\.js$/.test(path))).toBe(true);
    for (const chunk of ["chat", "servers", "you", "voice", "home"]) {
        expect(scripts.filter(path => new RegExp(`/${chunk}-[\\w-]+\\.js$`).test(path))).toEqual([]);
    }
});
