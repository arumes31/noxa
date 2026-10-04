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
        window.__events = {};
        window.runtime = { EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
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
            window.__micDestination = window.__micInput.createMediaStreamDestination();
            const stream = window.__micDestination.stream;
            window.__micTracks.push(...stream.getTracks());
            if (window.__delayMic) await new Promise(resolve => { window.__resolveMic = resolve; });
            return stream;
        };
        window.__noxa.openSettings("capture");
    });
});

const begin = page => page.getByRole("button", { name: "Begin Test", exact: true });
const calibrate = page => page.getByRole("button", { name: "Calibrate voice activation", exact: true });
const loopback = page => page.getByLabel("Loopback test (hear yourself — use headphones!)", { exact: true });

test("VAD threshold adjusts on the meter with pointer and keyboard without saving", async ({ page }, testInfo) => {
    await page.getByLabel("Voice Activity Detection", { exact: true }).check();
    const slider = page.getByRole("slider", { name: "VAD threshold", exact: true });
    await expect(slider).toBeVisible();
    await slider.focus();
    await page.keyboard.press("ArrowRight");
    await expect(slider).toHaveAttribute("aria-valuenow", "26");
    await begin(page).click();
    await expect(page.getByRole("meter", { name: "Microphone level" })).toBeVisible();
    const bounds = await slider.boundingBox();
    // Drag to -20 dBFS: amplitude .1, hence 50% of the VAD amplitude range (.2).
    await page.mouse.move(bounds.x + bounds.width / 2, bounds.y + bounds.height / 2);
    await page.mouse.down();
    await page.mouse.move(bounds.x + bounds.width * 2 / 3, bounds.y + bounds.height / 2, { steps: 5 });
    await page.mouse.up();
    await expect(slider).toHaveAttribute("aria-valuenow", "50");
    await expect(page.getByRole("spinbutton", { name: "VAD threshold", exact: true })).toHaveValue("50");
    await expect(page.locator(".settings-save-status")).toContainText("Unsaved changes");
    expect(await page.evaluate(() => window.__quickSavedSettings.vad_threshold)).toBe(25);
    expect(await page.evaluate(() => window.__micRequests.length)).toBe(1);
    await page.screenshot({ path: testInfo.outputPath("vad-on-meter.png") });
    await slider.focus(); await page.keyboard.press("Home");
    await expect(slider).toHaveAttribute("aria-valuenow", "1");
    await page.keyboard.press("End");
    await expect(slider).toHaveAttribute("aria-valuenow", "100");
    await page.locator("#set-cancel").click();
    expect(await page.evaluate(() => window.__quickSavedSettings.vad_threshold)).toBe(25);
});

test("meter threshold changes live transmission preview and Apply persists it in compact settings", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 740, height: 720 });
    await expect(page.locator(".mic-threshold-field")).toBeHidden();
    await page.getByLabel("Voice Activity Detection", { exact: true }).check();
    await begin(page).click();
    await expect.poll(() => page.evaluate(() => window.__micRequests.length)).toBe(1);
    await page.evaluate(() => {
        const tone = window.__micInput.createOscillator(), gain = window.__micInput.createGain();
        gain.gain.value = 0.15; tone.connect(gain).connect(window.__micDestination); tone.start();
        void window.__micInput.resume();
    });
    const number = page.getByRole("spinbutton", { name: "VAD threshold", exact: true });
    await number.fill("100");
    await expect(page.locator(".mic-transmission")).toHaveText("Below threshold");
    await number.fill("10");
    await expect(page.locator(".mic-transmission")).toHaveText("Would transmit");
    await expect(page.getByRole("slider", { name: "VAD threshold", exact: true })).toHaveAttribute("aria-valuenow", "10");
    await page.screenshot({ path: testInfo.outputPath("vad-compact.png") });
    await page.locator("#set-apply").click();
    expect(await page.evaluate(() => window.__quickSavedSettings.vad_threshold)).toBe(10);
    await page.getByLabel("Continuous Transmission", { exact: true }).check();
    await expect(page.locator(".mic-threshold-control")).toBeHidden();
    await expect(page.locator(".mic-threshold-field")).toBeHidden();
});

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

test("guided calibration measures quiet and speech then waits for explicit threshold acceptance", async ({ page }, testInfo) => {
    await calibrate(page).click();
    const status = page.locator("#mic-calibration-status");
    await expect(status).toContainText(/5\s*s/);
    await expect(status).toContainText(/4\s*s/);
    await page.screenshot({ path: testInfo.outputPath("microphone-calibration.png") });
    await expect(status).toContainText("Speak normally", { timeout: 7000 });
    await page.evaluate(() => {
        const tone = window.__micInput.createOscillator(), gain = window.__micInput.createGain();
        gain.gain.value = 0.15; tone.connect(gain).connect(window.__micDestination); tone.start();
        window.__micTone = tone; void window.__micInput.resume();
    });
    await expect(calibrate(page)).toBeEnabled({ timeout: 7000 });
    await expect(status).toContainText("Suggested threshold");
    expect(await page.evaluate(() => window.__quickSavedSettings.vad_threshold)).toBe(25);
    await page.getByRole("button", { name: "Use suggested threshold", exact: true }).click();
    await expect(page.locator(".settings-save-status")).toContainText("Unsaved changes");
    await expectReleased(page);
});

test("calibration preview stays in VAD while adjusting its threshold from PTT mode", async ({ page }) => {
    await calibrate(page).click();
    await expect(page.locator("#mic-calibration-status")).toContainText("Speak normally", { timeout: 7000 });
    await page.evaluate(() => {
        const tone = window.__micInput.createOscillator(), gain = window.__micInput.createGain();
        gain.gain.value = 0.15; tone.connect(gain).connect(window.__micDestination); tone.start();
        void window.__micInput.resume();
    });
    await expect(calibrate(page)).toBeEnabled({ timeout: 7000 });
    await page.getByRole("button", { name: "Preview suggested threshold", exact: true }).click();
    const slider = page.getByRole("slider", { name: "VAD threshold", exact: true });
    await slider.focus(); await page.keyboard.press("End");
    await expect(slider).toBeVisible();
    await expect(slider).toHaveAttribute("aria-valuenow", "100");
    await expect(page.locator(".mic-transmission")).toHaveText("Below threshold");
    const bounds = await slider.boundingBox();
    await page.mouse.click(bounds.x + bounds.width * 2 / 3, bounds.y + bounds.height / 2);
    await expect(slider).toBeVisible();
    await expect(slider).toHaveAttribute("aria-valuenow", "50");
    await page.getByRole("spinbutton", { name: "VAD threshold", exact: true }).fill("10");
    await expect(slider).toHaveAttribute("aria-valuenow", "10");
    await expect(page.getByRole("button", { name: "Test push-to-talk", exact: true })).toBeHidden();
    expect(await page.evaluate(() => window.__quickSavedSettings.vad_threshold)).toBe(25);
    await page.getByRole("button", { name: "Stop", exact: true }).click();
    await expect(slider).toBeHidden();
    await page.getByRole("button", { name: "Preview suggested threshold", exact: true }).click();
    await expect(slider).toBeVisible();
    await page.getByRole("button", { name: "Use suggested threshold", exact: true }).click();
    await expect(slider).toBeHidden();
    await expect(page.getByRole("button", { name: "Test push-to-talk", exact: true })).toBeVisible();
});

test("microphone meter exposes decibels and a keyboard-only transmission preview", async ({ page }) => {
    await begin(page).click();
    await expect(page.getByRole("meter", { name: "Microphone level" })).toBeVisible();
    await expect(page.locator(".mic-readings")).toContainText("dBFS");
    await expect(page.locator("#mic-test-status")).toHaveText("No signal detected");
    const talk = page.getByRole("button", { name: "Test push-to-talk", exact: true });
    await talk.focus(); await page.keyboard.down("Space");
    await expect(page.locator(".mic-transmission")).toHaveText("Would transmit");
    await page.keyboard.up("Space");
    await expect(talk).toHaveAttribute("aria-pressed", "false");
    await page.evaluate(() => window.__events.hotkey.forEach(fn => fn("ptt_down")));
    await expect(page.locator(".mic-key-status")).toHaveText("Shortcut detected");
    expect(await page.evaluate(() => window.__noxa.state.pttActive)).toBe(false);
    await page.evaluate(() => window.__events.hotkey.forEach(fn => fn("ptt_up")));
    await expect(page.locator(".mic-key-status")).toHaveText("Shortcut released");
});

test("changing processing during a test replaces capture without saving settings", async ({ page }) => {
    await begin(page).click();
    await expect.poll(() => page.evaluate(() => window.__micRequests.length)).toBe(1);
    await page.getByLabel("Noise suppression", { exact: true }).uncheck();
    await expect.poll(() => page.evaluate(() => window.__micRequests.length)).toBe(2);
    expect(await page.evaluate(() => window.__micTracks[0].readyState)).toBe("ended");
    expect(await page.evaluate(() => window.__micRequests[1].audio.noiseSuppression)).toBe(false);
    expect(await page.evaluate(() => window.__quickSavedSettings.noise_suppression)).toBe(true);
    await expect(page.locator(".mic-device")).toContainText("Testing:");
    await expect(page.locator(".settings-save-status")).toContainText("Unsaved changes");
    await page.locator("#set-apply").click();
    await expect(page.locator(".settings-save-status")).toHaveText("Changes applied");
});

test("recording is bounded, plays on the selected output, and is discarded on exit", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.state.settings.playback_device_id = "test-headset";
        window.__noxa.openSettings("capture");
        window.__sinkCalls = [];
        HTMLMediaElement.prototype.setSinkId = async function (id) { window.__sinkCalls.push(id); window.__previewAudio = this; };
        const revoke = URL.revokeObjectURL.bind(URL); window.__revoked = [];
        URL.revokeObjectURL = url => { window.__revoked.push(url); revoke(url); };
    });
    await page.getByRole("button", { name: "Record 5 seconds", exact: true }).click();
    await expect(page.locator("#mic-test-status")).toContainText("Recording —");
    await page.evaluate(() => {
        const tone = window.__micInput.createOscillator(), gain = window.__micInput.createGain();
        gain.gain.value = 0.1; tone.connect(gain).connect(window.__micDestination); tone.start();
        window.__micTone = tone; void window.__micInput.resume();
    });
    const listen = page.getByRole("button", { name: "Listen to recording", exact: true });
    await expect(listen).toBeEnabled({ timeout: 8000 });
    await expectReleased(page);
    await listen.click();
    await expect.poll(() => page.evaluate(() => window.__sinkCalls)).toEqual(["test-headset"]);
    await expect.poll(() => page.evaluate(() => window.__previewAudio.paused)).toBe(false);
    await page.locator("#set-cancel").click();
    expect(await page.evaluate(() => ({ paused: window.__previewAudio.paused, revoked: window.__revoked.length }))).toEqual({ paused: true, revoked: 1 });
});

test("monitoring uses the chosen output and reports device failures without losing capture", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.state.settings.playback_device_id = "missing-headset";
        window.__noxa.openSettings("capture");
        HTMLMediaElement.prototype.setSinkId = async () => { throw new Error("Output disconnected"); };
        const create = AudioContext.prototype.createMediaStreamDestination;
        window.__loopbackTracks = [];
        AudioContext.prototype.createMediaStreamDestination = function () {
            const node = create.call(this); window.__loopbackTracks.push(...node.stream.getTracks()); return node;
        };
    });
    await begin(page).click(); await loopback(page).click();
    await expect(page.locator("#mic-test-status")).toContainText("Output disconnected");
    await expect(loopback(page)).not.toBeChecked();
    expect(await page.evaluate(() => window.__micTracks[0].readyState)).toBe("live");
    expect(await page.evaluate(() => window.__loopbackTracks.map(track => track.readyState))).toEqual(["ended"]);
});

test("microphone settings expose a threshold preview and remain usable in narrow windows", async ({ page }, testInfo) => {
    await page.getByLabel("Voice Activity Detection", { exact: true }).check();
    await begin(page).click();
    await expect(page.locator(".mic-threshold-label")).toHaveText("Voice activation threshold: 25%");
    await expect(page.locator(".mic-transmission")).toHaveText("Below threshold");
    await page.evaluate(() => {
        const tone = window.__micInput.createOscillator(), gain = window.__micInput.createGain();
        gain.gain.value = 0.15; tone.connect(gain).connect(window.__micDestination); tone.start();
        window.__micTone = tone; void window.__micInput.resume();
    });
    await expect(page.locator(".mic-transmission")).toHaveText("Would transmit");
    await expect(page.locator("#mic-test-status")).toHaveText("Good level");
    for (const width of [1280, 640]) {
        await page.setViewportSize({ width, height: 900 });
        await page.getByRole("meter").scrollIntoViewIfNeeded();
        expect(await page.locator("#settings-content").evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true);
        await page.screenshot({ path: testInfo.outputPath(`microphone-${width}.png`) });
    }
    await page.evaluate(() => window.__micTone.stop());
    await expect(page.locator(".mic-transmission")).toHaveText("Below threshold");
});

test("leaving during output selection never starts delayed microphone playback", async ({ page }) => {
    await page.evaluate(() => {
        HTMLMediaElement.prototype.setSinkId = function () {
            window.__delayedAudio = this;
            return new Promise(resolve => { window.__finishOutput = resolve; });
        };
    });
    await begin(page).click(); await loopback(page).check();
    await expect.poll(() => page.evaluate(() => typeof window.__finishOutput)).toBe("function");
    await page.locator("#set-cancel").click();
    await page.evaluate(() => window.__finishOutput());
    expect(await page.evaluate(() => ({ paused: window.__delayedAudio.paused, stream: window.__delayedAudio.srcObject }))).toEqual({ paused: true, stream: null });
    await expectReleased(page);
});

test("cancelled recordings never publish a late replay sample", async ({ page }) => {
    await page.getByRole("button", { name: "Record 5 seconds", exact: true }).click();
    await expect(page.locator("#mic-test-status")).toContainText("Recording —");
    await page.locator("#set-cancel").click();
    await page.evaluate(() => window.__noxa.openSettings("capture"));
    await expect(page.getByRole("button", { name: "Listen to recording", exact: true })).toBeDisabled();
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
    await expect(page.getByRole("slider", { name: "VAD threshold", exact: true })).toHaveAttribute("aria-valuenow", "25");
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
    await page.locator(".notification-event-effects > summary").click();
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
    await page.locator(".notification-event-speech > summary").click();
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
    await page.locator(".notification-event-speech > summary").click();
    await expect(page.getByRole("button", { name: "Preview You were banned from the server.", exact: true })).toBeVisible();
    await page.locator("#set-cancel").click();
    await page.evaluate(() => window.__noxa.openSettings("notifications"));
    await expect(language).toHaveValue("de");
});
