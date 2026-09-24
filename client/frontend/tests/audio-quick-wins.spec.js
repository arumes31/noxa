import { expect, test } from "./fixtures.js";

// Real WebAudio and silent MediaStream tracks, with only the native capture
// boundary controlled so permission races do not depend on physical hardware.
test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        const settings = {
            settings_version: 9, language: "en", capture_device_id: "",
            playback_device_id: "", activation_mode: "ptt", vad_threshold: 25,
            echo_cancellation: true, noise_suppression: true, volume: 100,
            chat_max_lines: 200, camera_fps: 30, sound_volume: 100, speech_volume: 100,
            play_sounds: true, effects_enabled: true, spoken_messages: true,
            speech_language: "interface", duck_effects_while_speaking: false,
            notification_matrix: {}, bookmarks: [], onboarding_done: true,
            alpha_dismissed: "test", window_opacity: 100,
        };
        window.__quickSavedSettings = structuredClone(settings);
        window.__quickSaveCount = 0;
        window.runtime = { EventsOn: () => () => {}, EventsEmit() {}, WindowIsFullscreen: async () => false };
        window.go = { main: { App: new Proxy({}, { get(_target, method) {
            return async (...args) => {
                if (method === "GetSettings") return structuredClone(window.__quickSavedSettings);
                if (method === "SaveSettings") {
                    window.__quickSavedSettings = structuredClone(args[0]);
                    window.__quickSaveCount++;
                    return "";
                }
                if (["ListTabs", "GetPermissions"].includes(method)) return [];
                if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
                if (method === "Connected" || method === "IsGuest") return false;
                if (method === "ClientVersion" || method === "ClientVersionShort") return "test";
                return "";
            };
        } }) } };
        Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: {
            enumerateDevices: async () => [{ kind: "audioinput", deviceId: "mic-test", label: "Test microphone" }],
            getUserMedia: async () => { throw new Error("capture must be explicitly configured"); },
        } });
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.openSettings);
    await page.evaluate(() => {
        const NativeContext = window.AudioContext;
        window.__micInput = new NativeContext();
        window.__micRequests = [];
        window.__micTracks = [];
        window.__micContexts = [];
        window.AudioContext = class extends NativeContext {
            constructor(...args) { super(...args); window.__micContexts.push(this); }
        };
        navigator.mediaDevices.getUserMedia = async (constraints) => {
            window.__micRequests.push(structuredClone(constraints));
            if (window.__denyMic) throw new DOMException("Browser permission refused", "NotAllowedError");
            const stream = window.__micInput.createMediaStreamDestination().stream;
            window.__micTracks.push(...stream.getTracks());
            if (window.__delayMic) await new Promise(resolve => { window.__resolveMic = resolve; });
            return stream;
        };
        window.__noxa.openSettings("capture");
    });
});

const begin = page => page.getByRole("button", { name: "Begin Test", exact: true });
const calibrate = page => page.getByRole("button", { name: "Auto-calibrate (5s ambient)", exact: true });
const loopback = page => page.getByLabel("Loopback test (hear yourself — use headphones!)", { exact: true });

async function leaveCapture(page, exit) {
    if (exit === "close") await page.locator("#set-cancel").click();
    else await page.locator('[data-page="playback"]').click();
}

async function expectReleased(page) {
    await expect.poll(() => page.evaluate(() => ({
        tracks: window.__micTracks.map(track => track.readyState),
        contexts: window.__micContexts.map(context => context.state),
    }))).toEqual({ tracks: ["ended"], contexts: expect.arrayContaining(["closed"]) });
    expect(await page.evaluate(() => window.__micContexts.every(context => context.state === "closed"))).toBe(true);
}

test("microphone test applies unsaved capture processing without changing saved settings", async ({ page }) => {
    await page.getByLabel("Echo cancellation", { exact: true }).uncheck();
    await page.getByLabel("Noise suppression", { exact: true }).uncheck();
    await begin(page).click();
    await expect.poll(() => page.evaluate(() => window.__micRequests.length)).toBe(1);
    expect(await page.evaluate(() => window.__micRequests[0])).toEqual({ audio: {
        echoCancellation: false, noiseSuppression: false,
    } });
    expect(await page.evaluate(() => ({
        echo: window.__noxa.state.settings.echo_cancellation,
        noise: window.__noxa.state.settings.noise_suppression,
    }))).toEqual({ echo: true, noise: true });
});

test("microphone test honors the active music channel capture profile", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.state.myChannelID = 42;
        window.__noxa.state.channels = [{ ChannelID: 42, AudioProfile: "music" }];
    });
    await begin(page).click();
    await expect.poll(() => page.evaluate(() => window.__micRequests.length)).toBe(1);
    expect(await page.evaluate(() => window.__micRequests[0])).toEqual({ audio: {
        channelCount: 2, echoCancellation: false, noiseSuppression: false, autoGainControl: false,
    } });
});

for (const exit of ["close", "page change"]) {
    test(`active microphone meter and loopback release capture on ${exit}`, async ({ page }) => {
        await begin(page).click();
        await expect.poll(() => page.evaluate(() => window.__micContexts.length)).toBeGreaterThan(0);
        await loopback(page).check();
        await expect(loopback(page)).toBeChecked();
        await leaveCapture(page, exit);
        await expectReleased(page);
        expect(await page.evaluate(() => window.__noxa.state.localStream)).toBeNull();
    });

    test(`a microphone permission request resolved after ${exit} stops its late track`, async ({ page }) => {
        await page.evaluate(() => { window.__delayMic = true; });
        await begin(page).click();
        await expect.poll(() => page.evaluate(() => typeof window.__resolveMic)).toBe("function");
        await leaveCapture(page, exit);
        await page.evaluate(() => window.__resolveMic());
        await expect.poll(() => page.evaluate(() => window.__micTracks.map(track => track.readyState))).toEqual(["ended"]);
        expect(await page.evaluate(() => window.__micContexts.every(context => context.state === "closed"))).toBe(true);
    });
}

test("microphone testing and calibration cannot capture at the same time", async ({ page }) => {
    await page.evaluate(() => { window.__delayMic = true; });
    await begin(page).click();
    await expect(calibrate(page)).toBeDisabled();
    await page.evaluate(() => window.__resolveMic());
    await expect(page.locator(".mic-bar")).toBeVisible();
    await expect(calibrate(page)).toBeDisabled();
    await page.getByRole("button", { name: "Stop", exact: true }).click();
    await expect(calibrate(page)).toBeEnabled();
    await page.evaluate(() => { window.__delayMic = false; });
    await calibrate(page).click();
    await expect(begin(page)).toBeDisabled();
    expect(await page.evaluate(() => window.__micRequests.length)).toBe(2);
    await page.locator("#set-cancel").click();
    await expect.poll(() => page.evaluate(() => window.__micTracks.every(track => track.readyState === "ended"))).toBe(true);
});

test("ambient calibration counts down from five seconds and releases capture on completion", async ({ page }, testInfo) => {
    await calibrate(page).click();
    const status = page.locator("#mic-calibration-status");
    await expect(status).toContainText(/5\s*s/);
    await expect(status).toContainText(/4\s*s/);
    await page.screenshot({ path: testInfo.outputPath("microphone-calibration.png") });
    await expect(calibrate(page)).toBeEnabled({ timeout: 8000 });
    await expect(status).toContainText(/floor|threshold/i);
    await expectReleased(page);
});

test("closing pending calibration stops the late track without changing a reopened draft", async ({ page }) => {
    await page.evaluate(() => {
        window.__delayMic = true;
        window.__noxa.state.settings.activation_mode = "vad";
        window.__noxa.openSettings("capture");
    });
    await calibrate(page).click();
    await expect.poll(() => page.evaluate(() => typeof window.__resolveMic)).toBe("function");
    await page.locator("#set-cancel").click();
    await page.evaluate(() => window.__noxa.openSettings("capture"));
    await page.evaluate(() => window.__resolveMic());
    await expect.poll(() => page.evaluate(() => window.__micTracks.map(track => track.readyState))).toEqual(["ended"]);
    await expect(page.getByRole("slider", { name: "VAD threshold", exact: true })).toHaveValue("25");
    await expect(page.locator("#mic-calibration-status")).toBeEmpty();
    await expect(calibrate(page)).toBeEnabled();
    expect(await page.evaluate(() => window.__micContexts.every(context => context.state === "closed"))).toBe(true);
});

test("microphone capture remains reusable after success, denial, and another success", async ({ page }) => {
    for (const denied of [false, true, false]) {
        await page.evaluate(value => { window.__denyMic = value; }, denied);
        await begin(page).click();
        if (denied) {
            await expect(page.locator("#mic-test-status")).toContainText(/permission.*denied|access.*denied/i);
        } else {
            await expect(page.locator(".mic-bar")).toBeVisible();
            await page.getByRole("button", { name: "Stop", exact: true }).click();
        }
        await expect(begin(page)).toBeEnabled();
        await expect(calibrate(page)).toBeEnabled();
        await expect.poll(() => page.evaluate(() => ({
            tracksStopped: window.__micTracks.every(track => track.readyState === "ended"),
            contextsClosed: window.__micContexts.every(context => context.state === "closed"),
        }))).toEqual({ tracksStopped: true, contextsClosed: true });
    }
    expect(await page.evaluate(() => window.__micRequests.length)).toBe(3);
});

test("silent microphone input is reported separately from permission denial", async ({ page }) => {
    await begin(page).click();
    await expect(page.locator("#mic-test-status")).toContainText(/silent|no (sound|audio|signal).*detected/i, { timeout: 6000 });
    await expect(page.locator("#mic-test-status")).not.toContainText(/denied|permission/i);
    await page.getByRole("button", { name: "Stop", exact: true }).click();
    await page.evaluate(() => { window.__denyMic = true; });
    await begin(page).click();
    await expect(page.locator("#mic-test-status")).toContainText(/permission.*denied|access.*denied/i);
    await expect(page.locator("#mic-test-status")).not.toContainText(/silent|no (sound|audio|signal).*detected/i);
    await expect(begin(page)).toBeEnabled();
});

test("typed audio percentages stay synchronized, clamp safely, and persist independently", async ({ page }) => {
    await page.locator('[data-page="playback"]').click();
    const voice = page.getByRole("spinbutton", { name: "Voice volume (%)", exact: true });
    const voiceSlider = page.getByRole("slider", { name: "Voice volume", exact: true });
    await voice.fill("137");
    await expect(voiceSlider).toHaveValue("137");
    await voice.fill("");
    await voice.blur();
    await expect(voice).toHaveValue("137");
    await voice.fill("250");
    await voice.blur();
    await expect(voice).toHaveValue("200");
    await expect(voiceSlider).toHaveValue("200");
    await voice.fill("-10");
    await voice.blur();
    await expect(voice).toHaveValue("0");
    await voiceSlider.fill("125");
    await expect(voice).toHaveValue("125");
    await page.locator('[data-page="notifications"]').click();
    await page.getByRole("spinbutton", { name: "Sound volume (%)", exact: true }).fill("63");
    await page.getByRole("spinbutton", { name: "Speech volume (%)", exact: true }).fill("82");
    await expect(page.getByRole("slider", { name: "Sound volume", exact: true })).toHaveValue("63");
    await expect(page.getByRole("slider", { name: "Speech volume", exact: true })).toHaveValue("82");
    expect(await page.evaluate(() => ({ volume: window.__noxa.state.settings.volume, sound: window.__noxa.state.settings.sound_volume, speech: window.__noxa.state.settings.speech_volume })))
        .toEqual({ volume: 100, sound: 100, speech: 100 });
    await page.locator("#set-ok").click();
    await expect(page.locator("#settings-overlay")).toHaveCount(0);
    expect(await page.evaluate(() => window.__quickSavedSettings)).toMatchObject({ volume: 125, sound_volume: 63, speech_volume: 82 });
    await page.evaluate(() => window.__noxa.openSettings("notifications"));
    await expect(page.getByRole("spinbutton", { name: "Sound volume (%)", exact: true })).toHaveValue("63");
    await expect(page.getByRole("spinbutton", { name: "Speech volume (%)", exact: true })).toHaveValue("82");
    await page.locator('[data-page="playback"]').click();
    await expect(voice).toHaveValue("125");
});

test("notification silence explanations reflect unsaved gates, zero volumes, and disabled events", async ({ page }) => {
    await page.locator('[data-page="notifications"]').click();
    const status = page.locator(".audio-status");
    await page.getByLabel("Application audio (effects and speech)", { exact: true }).uncheck();
    await expect(status).toContainText("Notification master is muted");
    await page.getByLabel("Do not disturb", { exact: true }).check();
    await expect(status).toContainText("Do Not Disturb is active");
    await page.getByLabel("Sound effects", { exact: true }).uncheck();
    await expect(status).toContainText("Sound effects are disabled");
    await page.getByRole("spinbutton", { name: "Sound volume (%)", exact: true }).fill("0");
    await page.getByRole("spinbutton", { name: "Speech volume (%)", exact: true }).fill("0");
    await expect(status).toContainText("Effects volume is 0%");
    await expect(status).toContainText("Announcement volume is 0%");
    await page.getByLabel("Push-to-talk on", { exact: true }).uncheck();
    await expect(status).toContainText("Disabled sound events: 1");
    await page.locator("#set-cancel").click();
    expect(await page.evaluate(() => window.__quickSaveCount)).toBe(0);
    await page.evaluate(() => window.__noxa.openSettings("notifications"));
    await expect(status).not.toContainText(/master is muted|Do Not Disturb is active|Sound effects are disabled|volume is 0%|Disabled sound events/);
});

test("unavailable output is explained when both requested and default routing fail", async ({ page }) => {
    await page.locator('[data-page="playback"]').click();
    await page.evaluate(async () => {
        const { soundEngine } = await import("/src/sounds.js");
        const ctx = soundEngine.context();
        ctx.setSinkId = async () => { throw new DOMException("Output unplugged", "NotFoundError"); };
        await soundEngine.setOutput("missing-output");
    });
    await expect(page.locator(".audio-status")).toContainText("Audio output unavailable. Reconnect the device or select another output.");
});

test("announcement language changes only fixed speech, previews the chosen asset, and persists", async ({ page }, testInfo) => {
    await page.locator('[data-page="notifications"]').click();
    await page.evaluate(async () => {
        const { soundEngine } = await import("/src/sounds.js");
        window.__quickPlayed = [];
        const play = soundEngine.play.bind(soundEngine);
        soundEngine.play = (id, options) => { window.__quickPlayed.push(id); return play(id, options); };
    });
    const language = page.getByRole("combobox", { name: "Announcement language", exact: true });
    await language.selectOption("de");
    await expect(language).toHaveValue("de");
    await language.scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath("notification-audio-controls.png") });
    await page.getByRole("button", { name: "Preview Du wurdest vom Server gebannt.", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__quickPlayed.includes("speech_de_banned"))).toBe(true);
    await page.getByRole("button", { name: "Stop preview", exact: true }).click();
    expect(await page.evaluate(() => window.__noxa.state.settings.language)).toBe("en");
    expect(await page.evaluate(() => window.__noxa.state.settings.speech_language)).toBe("interface");
    await page.getByLabel("Lower routine sounds during conversations", { exact: true }).check();
    await page.locator("#set-ok").click();
    await expect(page.locator("#settings-overlay")).toHaveCount(0);
    expect(await page.evaluate(() => window.__quickSavedSettings)).toMatchObject({ language: "en", speech_language: "de", duck_effects_while_speaking: true });
    await page.evaluate(() => window.__noxa.openSettings("notifications"));
    await expect(language).toHaveValue("de");
    await expect(page.getByLabel("Lower routine sounds during conversations", { exact: true })).toBeChecked();
    await language.selectOption("en");
    await expect(page.getByRole("button", { name: "Preview You were banned from the server.", exact: true })).toBeVisible();
    await page.locator("#set-cancel").click();
    await page.evaluate(() => window.__noxa.openSettings("notifications"));
    await expect(language).toHaveValue("de");
});
