import { test, expect } from "./fixtures.js";

async function mount(page, options = {}) {
    await page.route("**/__echo_test__", route => route.fulfill({ contentType: "text/html", body: '<!doctype html><title>Echo test</title><main></main><footer id="voice-bar"></footer>' }));
    await page.goto("/__echo_test__");
    await page.evaluate(async options => {
        window.__joins = [];
        window.__noxa = { state: { activeTabID: "a", serverGeneration: 1, sessionGeneration: 1, myClientID: "me", myChannelID: 3, muted: true, pttActive: false, channels: [{ ChannelID: 3, Name: "Lobby" }, { ChannelID: 7, Name: "Echo" }] } };
        window.go = { main: { App: {
            ServerInfoForTab: async () => ({ echo_channel_id: options.unavailable ? 0 : 7 }),
            JoinChannelForTab: async (tab, id) => {
                window.__joins.push([tab, id]);
                if (options.pending) await new Promise(resolve => { window.__finishJoin = resolve; });
                if (options.failure) return "permission denied";
                window.__noxa.state.myChannelID = id;
                return "";
            },
        } } };
        document.querySelector("main").append((await import("/src/network-echo.js")).createNetworkEchoTest());
    }, options);
}

test("echo test joins only explicitly, preserves mute/PTT and returns to the previous channel", async ({ page }) => {
    await mount(page);
    await expect(page.getByRole("button", { name: "Start network echo test" })).toBeEnabled();
    expect(await page.evaluate(() => window.__joins)).toEqual([]);
    await page.getByRole("button", { name: "Start network echo test" }).click();
    await expect(page.locator(".network-echo-session")).toContainText("Use your normal microphone and push-to-talk controls");
    expect(await page.evaluate(() => [window.__noxa.state.muted, window.__noxa.state.pttActive])).toEqual([true, false]);
    await page.getByRole("button", { name: "Return to previous channel" }).click();
    await expect(page.locator(".network-echo-session")).toHaveCount(0);
    expect(await page.evaluate(() => window.__joins)).toEqual([["a", 7], ["a", 3]]);
});

test("an unconfigured echo test stays unavailable without a channel move", async ({ page }) => {
    await mount(page, { unavailable: true });
    await expect(page.getByText("This server has no accessible network echo test channel.")).toBeVisible();
    await expect(page.getByRole("button", { name: "Start network echo test" })).toBeDisabled();
    expect(await page.evaluate(() => window.__joins)).toEqual([]);
});

test("join failure is visible and does not claim the echo test started", async ({ page }) => {
    await mount(page, { failure: true });
    await page.getByRole("button", { name: "Start network echo test" }).click();
    await expect(page.getByText(/Could not start the echo test: permission denied/)).toBeVisible();
    await expect(page.locator(".network-echo-session")).toHaveCount(0);
    expect(await page.evaluate(() => window.__noxa.state.myChannelID)).toBe(3);
});

test("a server switch during join cannot create a return action on the new server", async ({ page }) => {
    await mount(page, { pending: true });
    await page.getByRole("button", { name: "Start network echo test" }).click();
    await expect.poll(() => page.evaluate(() => typeof window.__finishJoin)).toBe("function");
    await page.evaluate(() => { window.__noxa.state.activeTabID = "b"; window.__noxa.state.serverGeneration++; window.__finishJoin(); });
    await expect(page.locator(".network-echo-session")).toHaveCount(0);
    expect(await page.evaluate(() => window.__joins)).toEqual([["a", 7]]);
});
