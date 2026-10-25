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


test("a missing lazy route chunk offers recovery and preserves sign-in after reload", async ({ page }) => {
    await page.goto(`${server.webURL}/app/`);
    await page.locator("summary").filter({ hasText: "Continue as guest" }).click();
    await page.getByLabel("Display name", { exact: true }).fill("Route recovery test");
    await page.getByRole("button", { name: "Continue as guest", exact: true }).click();
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
    await page.route("**/assets/you-*.js", route => route.abort());
    await page.getByRole("link", { name: "You", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("This page could not be loaded");
    await expect(page.getByRole("heading", { name: "Home", exact: true })).toHaveCount(0);
    await page.unroute("**/assets/you-*.js");
    await page.getByRole("button", { name: "Reload noXa", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Settings", exact: true })).toBeVisible();
    await expect(page.getByText(/^Route recovery test(#M\d+)?$/)).toBeVisible();
});
