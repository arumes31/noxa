import { test, expect } from "./fixtures.js";

async function mount(page, options = {}) {
    await page.route("**/__codec_test__", route => route.fulfill({ contentType: "text/html", body: '<!doctype html><title>Video codecs</title><main></main>' }));
    await page.goto("/__codec_test__");
    await page.evaluate(async options => {
        window.__codecCalls = [];
        window.__codecToasts = [];
        window.__noxa = { state: { myChannelID: 3, muted: true }, toast: value => window.__codecToasts.push(value) };
        window.runtime = { ClipboardSetText: async value => { window.__codecCopy = value; return true; } };
        (await import("/src/i18n.js")).setLanguage(options.language || "en");
        const { createVideoCodecCapabilityProbe } = await import("/src/video-codec-capabilities.js");
        const capabilities = { getCapabilities: kind => {
            window.__codecCalls.push(["advertised", kind]);
            return { codecs: [{ mimeType: "video/H264", clockRate: 90000, sdpFmtpLine: "profile-level-id=640032;packetization-mode=1" }] };
        } };
        const info = direction => async configuration => {
            window.__codecCalls.push([direction, configuration]);
            if (options.pending) return new Promise(resolve => { window.__resolveCodec = () => resolve({ supported: true, smooth: true, powerEfficient: true }); });
            if (options.failure) throw new Error("private device information must not appear");
            return { supported: true, smooth: true, powerEfficient: false };
        };
        const probe = createVideoCodecCapabilityProbe({ sender: capabilities, receiver: capabilities,
            mediaCapabilities: options.unavailable ? {} : { encodingInfo: info("encode"), decodingInfo: info("decode") } });
        const { createVideoCodecCapabilities } = await import("/src/video-codec-capabilities-ui.js");
        const root = createVideoCodecCapabilities({ check: options.status ? async () => ({ status: options.status }) : probe.run });
        document.querySelector("main").append(root);
    }, options);
}

test("codec check is explicit, bounded and copies predictions without enabling media", async ({ page }) => {
    await mount(page);
    expect(await page.evaluate(() => window.__codecCalls)).toEqual([]);
    await page.getByText("Video codec capabilities", { exact: true }).click();
    expect(await page.evaluate(() => window.__codecCalls)).toEqual([]);
    await expect(page.getByRole("button", { name: "Copy codec results" })).toBeDisabled();
    await page.getByRole("button", { name: "Check video codecs" }).click();
    await expect(page.getByRole("status")).toContainText("Check complete");
    await expect(page.getByText("Predicted support: Yes · Smooth: Yes · Power efficient: No")).toHaveCount(2);
    await expect(page.getByText("Not advertised by this browser.")).toHaveCount(8);
    await page.getByRole("button", { name: "Copy codec results" }).click();
    const data = await page.evaluate(() => ({ report: JSON.parse(window.__codecCopy), calls: window.__codecCalls, state: window.__noxa.state }));
    expect(data.report.schema).toBe("noxa_video_codec_capabilities_v1");
    expect(data.report.actual_hardware_verified).toBe(false);
    expect(data.report.counts.queries_started).toBe(2);
    expect(data.calls.filter(([kind]) => kind === "advertised")).toHaveLength(2);
    expect(data.calls.filter(([kind]) => kind === "encode")[0][1]).toEqual({ type: "webrtc", video: {
        width: 1920, height: 1080, framerate: 60, bitrate: 8000000,
        contentType: "video/H264;packetization-mode=1;profile-level-id=640032",
    } });
    expect(data.state).toEqual({ myChannelID: 3, muted: true });
});

test("unavailable prediction APIs remain unknown and export as partial", async ({ page }) => {
    await mount(page, { unavailable: true });
    await page.getByText("Video codec capabilities", { exact: true }).click();
    await page.getByRole("button", { name: "Check video codecs" }).click();
    await expect(page.getByRole("status")).toContainText("unknown does not mean unsupported");
    await expect(page.getByText("Prediction API unavailable.")).toHaveCount(2);
    await page.getByRole("button", { name: "Copy codec results" }).click();
    const report = await page.evaluate(() => JSON.parse(window.__codecCopy));
    expect(report.status).toBe("partial");
    expect(report.codecs[0].profiles[0].supported).toBeNull();
    expect(report.counts.queries_started).toBe(0);
});

test("native errors are not shown or exported", async ({ page }) => {
    await mount(page, { failure: true });
    await page.getByText("Video codec capabilities", { exact: true }).click();
    await page.getByRole("button", { name: "Check video codecs" }).click();
    await expect(page.getByText("Prediction failed; support is unknown.")).toHaveCount(2);
    await page.getByRole("button", { name: "Copy codec results" }).click();
    expect(await page.locator("body").innerText()).not.toContain("private device");
    expect(await page.evaluate(() => window.__codecCopy)).not.toContain("private device");
});

for (const status of ["busy", "cancelled"]) {
    test(`${status} is a status, not an exportable capability result`, async ({ page }) => {
        await mount(page, { status });
        await page.getByText("Video codec capabilities", { exact: true }).click();
        await page.getByRole("button", { name: "Check video codecs" }).click();
        await expect(page.getByRole("status")).toContainText(status === "busy" ? "already running" : "cancelled");
        await expect(page.getByRole("button", { name: "Check video codecs" })).toBeEnabled();
        await expect(page.getByRole("button", { name: "Copy codec results" })).toBeDisabled();
        await expect(page.locator(".video-codec-capabilities-results")).toBeEmpty();
    });
}

for (const action of ["cancel", "collapse", "remove"]) {
    test(`${action} stops queued codec queries and discards the pending result`, async ({ page }) => {
        await mount(page, { pending: true });
        await page.getByText("Video codec capabilities", { exact: true }).click();
        await page.getByRole("button", { name: "Check video codecs" }).click();
        await expect(page.getByRole("button", { name: "Check video codecs" })).toBeDisabled();
        await page.waitForFunction(() => !!window.__resolveCodec);
        if (action === "cancel") await page.getByRole("button", { name: "Cancel codec check" }).click();
        else if (action === "collapse") await page.getByText("Video codec capabilities", { exact: true }).click();
        else await page.evaluate(() => document.querySelector("main").replaceChildren());
        await page.evaluate(() => window.__resolveCodec());
        if (action !== "remove") {
            if (action === "collapse") await page.getByText("Video codec capabilities", { exact: true }).click();
            await expect(page.getByRole("status")).toHaveText("Codec check cancelled.");
            await expect(page.getByRole("button", { name: "Check video codecs" })).toBeEnabled();
            await expect(page.getByRole("button", { name: "Copy codec results" })).toBeDisabled();
        }
        expect(await page.evaluate(() => window.__codecCalls.filter(([kind]) => kind !== "advertised"))).toHaveLength(1);
        expect(await page.evaluate(() => window.__codecCopy)).toBeUndefined();
    });
}

test("German results fit a narrow panel", async ({ page }) => {
    await page.setViewportSize({ width: 420, height: 800 });
    await mount(page, { language: "de" });
    await page.getByText("Video-Codec-Fähigkeiten", { exact: true }).click();
    await page.getByRole("button", { name: "Video-Codecs prüfen" }).click();
    await expect(page.getByRole("status")).toContainText("Prüfung abgeschlossen");
    await expect(page.getByRole("heading", { name: "H264 · Senden" })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});
