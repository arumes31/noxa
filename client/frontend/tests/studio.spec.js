import { expect, test } from "./fixtures.js";

// Real Chromium, real Studio modules, synthetic colored-canvas captures (no
// capture hardware is used or claimed). The voice peer is the same stand-in
// the media-slots specs use, so Go live runs the ordinary guarded screen
// publication (startShare) with the encrypted-session double.

const RED = [230, 20, 20], GREEN = [20, 200, 40], BLUE = [20, 40, 230];

test.beforeEach(async ({ page }) => {
    await page.route("**/studio-fixture", route => route.fulfill({ contentType: "text/html", body: `<!doctype html>
        <footer id="voice-bar"><div class="voice-buttons"><button id="voice-screen">Share</button><button id="voice-video">Camera</button><div class="voice-secondary-actions"></div></div></footer>
        <video id="local-video"></video><div id="mic-status"></div><button id="ptt-btn">Talk</button><section id="sharing-status" hidden></section>` }));
    await page.goto("/studio-fixture");
    await page.evaluate(async () => {
        // Record every audio connection to the local speakers (feedback check).
        window.__speakerConnections = 0;
        const connect = AudioNode.prototype.connect;
        AudioNode.prototype.connect = function (target, ...rest) {
            if (target instanceof AudioDestinationNode) window.__speakerConnections++;
            return connect.call(this, target, ...rest);
        };
        const colored = (rgb, fps = 30) => {
            const canvas = document.createElement("canvas"); canvas.width = 320; canvas.height = 180;
            const ctx = canvas.getContext("2d");
            const paint = () => { ctx.fillStyle = `rgb(${rgb.join(",")})`; ctx.fillRect(0, 0, 320, 180); };
            paint(); const timer = setInterval(paint, 50);
            const stream = canvas.captureStream(fps);
            stream.getVideoTracks()[0].addEventListener("ended", () => clearInterval(timer));
            return stream;
        };
        window.__captures = { display: [], camera: [], colors: [], displayCalls: 0, cameraCalls: 0, audioContexts: [] };
        const tone = () => {
            const context = new AudioContext(); const osc = context.createOscillator(); const dest = context.createMediaStreamDestination();
            osc.frequency.value = 440; osc.connect(dest); osc.start(); window.__captures.audioContexts.push(context);
            return dest.stream.getAudioTracks()[0];
        };
        Object.defineProperty(navigator.mediaDevices, "getDisplayMedia", { configurable: true, value: async options => {
            window.__captures.displayCalls++;
            window.__captures.lastDisplayOptions = options;
            const stream = colored(window.__captures.colors.shift() || [230, 20, 20]);
            if (options.audio) stream.addTrack(tone());
            window.__captures.display.push(stream);
            return stream;
        } });
        Object.defineProperty(navigator.mediaDevices, "getUserMedia", { configurable: true, value: async constraints => {
            window.__captures.cameraCalls++;
            window.__captures.cameraConstraints = constraints;
            const stream = colored(window.__captures.colors.shift() || [20, 200, 40]);
            window.__captures.camera.push(stream);
            return stream;
        } });
        // Voice peer stand-in (as in media-slots.spec.js).
        const transceivers = [];
        window.__pub = { replaceTrack: 0, offers: [], controls: [] };
        const pc = {
            signalingState: "stable", connectionState: "connected",
            getSenders: () => transceivers.filter(t => !t.stopped).map(t => t.sender),
            getTransceivers: () => transceivers.filter(t => !t.stopped),
            createOffer: async () => ({ type: "offer", sdp: "test-offer" }),
            async setLocalDescription(description) { this.signalingState = description.type === "rollback" ? "stable" : "have-local-offer"; },
            async setRemoteDescription() { this.signalingState = "stable"; },
            addTransceiver(track, options) {
                let parameters = { encodings: options.sendEncodings || [{}] };
                const sender = { track, replaceTrack: async next => { window.__pub.replaceTrack++; sender.track = next; },
                    getParameters: () => structuredClone(parameters), setParameters: async next => { parameters = structuredClone(next); } };
                const transceiver = { sender, receiver: { track }, direction: options.direction, stop() { this.stopped = true; } };
                transceivers.push(transceiver);
                return transceiver;
            },
        };
        const localStream = new MediaStream();
        window.__noxa = { state: { pc, localStream, serverGeneration: 1, activeTabID: "studio-tab", settings: {}, myChannelID: 1,
            myClientID: "self", clients: [{ client_id: "peer", channel_id: 1 }], mediaLimits: { video_max_width: 1280, video_max_height: 720, video_max_bitrate: 0 } },
        $: id => document.getElementById(id), sysMsg: message => window.__pub.controls.push(["sys", message]), toast: message => window.__pub.controls.push(["toast", message]) };
        window.go = { main: { App: {
            WebRTCOfferForTab: async (tab, _sdp, slots) => { window.__pub.offers.push(slots); return "answer"; },
            VideoStreamControlForTab: async (tab, msg) => { window.__pub.controls.push(["control", msg.action, msg.slot, msg.active]); return { ...msg, generation: "1", streams: [] }; },
            SetVideoQualityForTab: async () => "",
            AcquireDisplayCaptureLease: async () => "lease", RefreshDisplayCaptureLease: async () => {}, ReleaseDisplayCaptureLease: async () => {},
        } } };
        window.go.main.App.WebRTCOfferSecureForTab = (tab, sdp, slots) => window.go.main.App.WebRTCOfferForTab(tab, sdp, slots);
        const channelMedia = await import("/src/channel-media.js");
        const standIn = () => ({ active: true, media: { authenticated: () => true, onAuthenticated() {} }, identity: { channelID: 1 }, async start() { return this; }, stop() { this.active = false; }, setRoster() {}, protectPeer: p => p,
            peerConfig: c => c, bindSenders: async () => {}, syncReceivers: async () => {}, rescope() {}, publicationsChanged() {}, receive: () => ({ ok: false }), status: () => ({ state: "encrypted" }) });
        channelMedia.useChannelMediaFactory(standIn);
        channelMedia.useChannelMediaSession(standIn());
        (await import("/src/modal.js")).initModalSystem();
        window.__studio = await import("/src/studio-ui.js");
        window.__video = await import("/src/video.js");
        window.__studio.initStudio();
        // Pixel probe: the average color at a point of the program or preview canvas.
        window.__pixel = (which, x = 0.5, y = 0.5) => {
            const session = window.__studio.studioSessionForTest();
            const canvas = which === "program" ? session.engine.program : session.engine.preview;
            const data = canvas.getContext("2d").getImageData(Math.floor(canvas.width * x), Math.floor(canvas.height * y), 1, 1).data;
            return [data[0], data[1], data[2]];
        };
    });
});

const near = (actual, expected, tolerance = 40) => actual.every((value, i) => Math.abs(value - expected[i]) <= tolerance);
const pixel = (page, which, x, y) => page.evaluate(([w, px, py]) => window.__pixel(w, px, py), [which, x, y]);
async function expectPixel(page, which, expected, x, y) {
    await expect.poll(async () => near(await pixel(page, which, x, y), expected), { timeout: 5000 }).toBe(true);
}
async function open(page) {
    await page.getByRole("button", { name: "Compose scenes from captures, images and text and stream the program" }).click();
    await expect(page.getByRole("dialog", { name: "Studio" })).toBeVisible();
    return page.getByRole("dialog", { name: "Studio" });
}
async function addDisplay(page, color, audio = "none") {
    await page.evaluate(c => window.__captures.colors.push(c), color);
    const studio = page.getByRole("dialog", { name: "Studio" });
    await studio.locator(".studio-capture-audio").selectOption(audio);
    await studio.getByRole("button", { name: "Screen or window", exact: true }).click();
}
async function addCamera(page, color) {
    await page.evaluate(c => window.__captures.colors.push(c), color);
    await page.getByRole("dialog", { name: "Studio" }).getByRole("button", { name: "Camera", exact: true }).click();
}
const diagnostics = page => page.evaluate(() => window.__studio.studioDiagnostics());

test("opening the Studio never captures; draft edits stay off the program until Take", async ({ page }) => {
    const studio = await open(page);
    expect(await page.evaluate(() => [window.__captures.displayCalls, window.__captures.cameraCalls])).toEqual([0, 0]);
    await addDisplay(page, RED);
    await expectPixel(page, "preview", RED);
    await page.waitForTimeout(300);
    expect(near(await pixel(page, "program"), [0, 0, 0], 10)).toBe(true);
    await studio.locator(".studio-transition").selectOption("cut");
    await studio.getByRole("button", { name: "Take", exact: true }).click();
    await expectPixel(page, "program", RED);
    // A second scene with a green camera: preview changes, program does not.
    await studio.getByRole("button", { name: "Add scene" }).click();
    await addCamera(page, GREEN);
    await expectPixel(page, "preview", GREEN);
    await page.waitForTimeout(400);
    expect(near(await pixel(page, "program"), RED)).toBe(true);
    // Editing the on-air scene's draft (hide its source) does not reach the program.
    await page.keyboard.press("1");
    await studio.locator(".studio-visibility").first().click();
    await expectPixel(page, "preview", [0, 0, 0], 0.5, 0.5);
    await page.waitForTimeout(400);
    expect(near(await pixel(page, "program"), RED)).toBe(true);
    // Take (cut) the green scene.
    await page.keyboard.press("2");
    await page.keyboard.press("Control+Enter");
    await expectPixel(page, "program", GREEN);
    expect((await diagnostics(page)).program).toBe(await page.evaluate(() => window.__studio.studioSessionForTest().studio.scenes[1].id));
});

test("crossfade blends both scenes and settles; a new Take replaces a running fade", async ({ page }) => {
    const studio = await open(page);
    await addDisplay(page, RED);
    await studio.locator(".studio-transition").selectOption("cut");
    await studio.getByRole("button", { name: "Take", exact: true }).click();
    await expectPixel(page, "program", RED);
    await studio.getByRole("button", { name: "Add scene" }).click();
    await addDisplay(page, BLUE);
    await studio.locator(".studio-transition").selectOption("fade");
    await studio.locator(".studio-duration").fill("1500");
    await studio.locator(".studio-duration").press("Tab");
    expect(await page.evaluate(() => window.__studio.studioSessionForTest().studio.transition)).toEqual({ kind: "fade", ms: 1500 });
    await studio.getByRole("button", { name: "Take", exact: true }).click();
    const samples = [];
    const started = Date.now();
    while (Date.now() - started < 1300) { samples.push(await pixel(page, "program")); await page.waitForTimeout(60); }
    // Mid-transition: red AND blue present at once (a real crossfade).
    expect(samples.some(([r, , b]) => r > 70 && b > 70)).toBe(true);
    await expectPixel(page, "program", BLUE);
    await expect.poll(async () => (await diagnostics(page)).transitioning, { timeout: 3000 }).toBe(false);
    // Out-of-range durations are clamped to 100–1500 ms.
    await studio.locator(".studio-duration").fill("20");
    await studio.locator(".studio-duration").press("Tab");
    expect(await page.evaluate(() => window.__studio.studioSessionForTest().studio.transition.ms)).toBe(100);
    await studio.locator(".studio-duration").fill("1500");
    await studio.locator(".studio-duration").press("Tab");
    // Replace a running fade: back to red, then immediately to blue again.
    await page.keyboard.press("1");
    await page.keyboard.press("Control+Enter");
    await page.waitForTimeout(200);
    expect((await diagnostics(page)).transitioning).toBe(true);
    await page.keyboard.press("2");
    await page.keyboard.press("Control+Enter");
    await expectPixel(page, "program", BLUE);
    await expect.poll(async () => (await diagnostics(page)).transitioning, { timeout: 3000 }).toBe(false);
    // The outgoing holder is released after the fade: only the blue capture is held by the program.
    const captures = (await diagnostics(page)).captures;
    expect(captures.every(capture => capture.state === "live")).toBe(true);
});

test("ended or revoked sources turn neutral at once; removing a draft source keeps the live capture", async ({ page }) => {
    const studio = await open(page);
    await addDisplay(page, RED);
    await studio.locator(".studio-transition").selectOption("cut");
    await page.keyboard.press("Control+Enter");
    await expectPixel(page, "program", RED);
    // Remove the source from the draft: the program still shows it, capture alive.
    await studio.getByRole("button", { name: "Remove Screen" }).click();
    await page.waitForTimeout(300);
    expect(near(await pixel(page, "program"), RED)).toBe(true);
    expect(await page.evaluate(() => window.__captures.display[0].getVideoTracks()[0].readyState)).toBe("live");
    expect((await diagnostics(page)).captures).toEqual([{ id: "c1", refs: 1, state: "live" }]);
    // Taking the (now empty) draft releases the capture.
    await page.keyboard.press("Control+Enter");
    await expect.poll(() => page.evaluate(() => window.__captures.display[0].getVideoTracks()[0].readyState)).toBe("ended");
    expect((await diagnostics(page)).captures).toEqual([]);

    // A live capture that the user revokes (track ends) becomes neutral, not frozen.
    await addDisplay(page, BLUE);
    await page.keyboard.press("Control+Enter");
    await expectPixel(page, "program", BLUE);
    const neutralAfter = await page.evaluate(async () => {
        const track = window.__captures.display[1].getVideoTracks()[0];
        track.stop();
        track.dispatchEvent(new Event("ended"));
        const started = performance.now();
        while (performance.now() - started < 1000) {
            const [r, g, b] = window.__pixel("program");
            if (b < 60 && r < 60 && g < 60) return performance.now() - started;
            await new Promise(resolve => setTimeout(resolve, 5));
        }
        return -1;
    });
    expect(neutralAfter).toBeGreaterThanOrEqual(0);
    expect(neutralAfter).toBeLessThan(100);
    expect(near(await pixel(page, "program"), [17, 22, 28], 12)).toBe(true);
    await expect(studio.locator(".studio-source[data-state='ended']")).toBeVisible();
    // No silent restart: an explicit recapture in the draft, then Take.
    expect(await page.evaluate(() => window.__captures.displayCalls)).toBe(2);
    await page.evaluate(() => window.__captures.colors.push([20, 200, 40]));
    await studio.getByRole("button", { name: "Recapture Screen", exact: true }).click();
    await expectPixel(page, "preview", GREEN);
    expect(near(await pixel(page, "program"), [17, 22, 28], 12)).toBe(true);
    await page.keyboard.press("Control+Enter");
    await expectPixel(page, "program", GREEN);
});

test("Go live publishes the stable program through the ordinary screen publication; Take never replaces the track", async ({ page }) => {
    const studio = await open(page);
    await addDisplay(page, RED);
    await studio.locator(".studio-transition").selectOption("cut");
    await page.keyboard.press("Control+Enter");
    await studio.getByRole("button", { name: "Go live" }).click();
    await expect(studio.locator(".studio-air")).toHaveText("Live");
    const published = await page.evaluate(() => {
        const state = window.__noxa.state;
        const sender = state.shareVideoTransceiver.sender;
        return { sharing: state.screenSharing, trackKind: sender.track.kind, live: sender.track.readyState,
            fromStudio: window.__studio.studioSessionForTest().engine.captureTrack !== sender.track,
            settings: sender.track.getSettings(), studioActive: window.__video.studioShareActive(),
            audio: !!state.shareAudioTransceiver, replaceTrack: window.__pub.replaceTrack, offers: window.__pub.offers.length,
            publish: window.__pub.controls.filter(entry => entry[0] === "control" && entry[1] === "publish" && entry[2] === "screen").length };
    });
    expect(published.sharing).toBe(true);
    expect(published.trackKind).toBe("video");
    expect(published.live).toBe("live");
    expect(published.fromStudio).toBe(true); // the publication owns a clone
    expect([published.settings.width, published.settings.height]).toEqual([1280, 720]);
    expect(published.studioActive).toBe(true);
    expect(published.audio).toBe(true); // one stable studio audio track
    expect(published.offers).toBeGreaterThan(0);
    expect(published.publish).toBeGreaterThan(0);
    const sender = await page.evaluateHandle(() => window.__noxa.state.shareVideoTransceiver.sender);
    const before = await page.evaluate(s => [s.track.id, window.__pub.replaceTrack, window.__pub.offers.length], sender);
    // Several Takes and a crossfade: pixels change, the published track does not.
    await studio.getByRole("button", { name: "Add scene" }).click();
    await addCamera(page, GREEN);
    await page.keyboard.press("Control+Enter");
    await studio.locator(".studio-transition").selectOption("fade");
    await page.keyboard.press("1");
    await page.keyboard.press("Control+Enter");
    await page.waitForTimeout(600);
    expect(await page.evaluate(s => [s.track.id, window.__pub.replaceTrack, window.__pub.offers.length], sender)).toEqual(before);
    // The share bar labels the Studio, and its Change action reopens the Studio.
    await expect(page.locator(".sharing-source")).toContainText("Studio");
    // Stop: publication ends, every capture/timer/canvas is released.
    const publishedTrack = await page.evaluateHandle(() => window.__noxa.state.shareVideoTransceiver.sender.track);
    await studio.getByRole("button", { name: "Stop", exact: true }).click();
    await expect(studio.locator(".studio-air")).toHaveText("Off air");
    const after = await page.evaluate(t => ({
        sharing: window.__noxa.state.screenSharing, published: t.readyState,
        captures: [...window.__captures.display, ...window.__captures.camera].flatMap(s => s.getTracks()).map(track => track.readyState),
        diagnostics: window.__studio.studioDiagnostics(),
    }), publishedTrack);
    expect(after.sharing).toBe(false);
    expect(after.published).toBe("ended");
    expect(after.captures.every(state => state === "ended")).toBe(true);
    expect(after.diagnostics.captures).toEqual([]); // fresh capture-free session in the open panel
    expect(after.diagnostics.live).toBe(false);
});

test("the Studio refuses to replace an ordinary share and ordinary sharing still works", async ({ page }) => {
    await page.evaluate(async () => {
        await import("/src/modal.js");
        document.getElementById("voice-screen").onclick = () => window.__video.shareToggle();
    });
    await page.locator("#voice-screen").click();
    await page.getByRole("button", { name: "Choose screen or window…", exact: true }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Start sharing", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__noxa.state.screenSharing)).toBe(true);
    expect(await page.evaluate(() => window.__video.studioShareActive())).toBe(false);
    const studio = await open(page);
    await addDisplay(page, RED);
    await studio.getByRole("button", { name: "Go live" }).click();
    await expect(studio.locator(".studio-error")).toContainText("Stop the current screen share");
    expect(await page.evaluate(() => window.__noxa.state.shareStream.getVideoTracks()[0].readyState)).toBe("live");
});

test("panel close ownership: off air stops every capture; live keeps only the program's captures", async ({ page }) => {
    let studio = await open(page);
    await addDisplay(page, RED);
    await studio.getByRole("button", { name: "Close Studio" }).click();
    await expect(studio).toBeHidden();
    expect(await page.evaluate(() => window.__captures.display[0].getVideoTracks()[0].readyState)).toBe("ended");
    expect(await page.evaluate(() => window.__studio.studioActive())).toBe(false);

    studio = await open(page);
    await expect(studio.locator(".studio-ownership")).toHaveText("Closing stops every Studio capture.");
    await expect(studio.locator(".studio-source[data-state='missing']")).toBeVisible(); // restored layout, no auto capture
    expect(await page.evaluate(() => window.__captures.displayCalls)).toBe(1);
    await studio.getByRole("button", { name: "Remove Screen" }).click();
    await addDisplay(page, RED);       // program capture
    await studio.locator(".studio-transition").selectOption("cut");
    await page.keyboard.press("Control+Enter");
    await studio.getByRole("button", { name: "Add scene" }).click();
    await addCamera(page, GREEN);      // draft-only capture
    await studio.getByRole("button", { name: "Go live" }).click();
    await expect(studio.locator(".studio-ownership")).toContainText("Closing keeps the program live");
    await studio.getByRole("button", { name: "Close Studio" }).click();
    const state = await page.evaluate(() => ({
        program: window.__captures.display[1].getVideoTracks()[0].readyState,
        draft: window.__captures.camera[0].getVideoTracks()[0].readyState,
        sharing: window.__noxa.state.screenSharing, diagnostics: window.__studio.studioDiagnostics(),
    }));
    expect(state).toMatchObject({ program: "live", draft: "ended", sharing: true });
    expect(state.diagnostics.live).toBe(true);
    await expectPixel(page, "program", RED);
    // Teardown (channel/server switch, disconnect): disposeStudio releases everything.
    const engine = await page.evaluateHandle(() => window.__studio.studioSessionForTest().engine);
    await page.evaluate(() => window.__studio.disposeStudio());
    const disposed = await page.evaluate(e => ({ program: window.__captures.display[1].getVideoTracks()[0].readyState,
        canvas: [e.program.width, e.preview.width], timer: e.timer, active: window.__studio.studioActive() }), engine);
    expect(disposed).toEqual({ program: "ended", canvas: [0, 0], timer: null, active: false });
});

test("a server/account switch while live ends the publication and every Studio resource", async ({ page }) => {
    const studio = await open(page);
    await addDisplay(page, RED, "system");
    await page.keyboard.press("Control+Enter");
    await studio.getByRole("button", { name: "Go live" }).click();
    await expect(studio.locator(".studio-air")).toHaveText("Live");
    const handles = await page.evaluateHandle(() => {
        const session = window.__studio.studioSessionForTest();
        return { published: window.__noxa.state.shareVideoTransceiver.sender.track, engine: session.engine, audio: session.audio };
    });
    // The modal system closes server-scoped dialogs on a server/account switch.
    await page.evaluate(async () => (await import("/src/modal.js")).closeServerDialogs("server-change"));
    await expect(studio).toBeHidden();
    await expect.poll(() => page.evaluate(() => window.__noxa.state.screenSharing)).toBe(false);
    const after = await page.evaluate(h => ({ published: h.published.readyState, captures: window.__captures.display.flatMap(s => s.getTracks()).map(t => t.readyState),
        timer: h.engine.timer, canvas: h.engine.program.width, audio: h.audio.graph().contextState, active: window.__studio.studioActive() }), handles);
    expect(after.published).toBe("ended");
    expect(after.captures.every(state => state === "ended")).toBe(true);
    expect(after).toMatchObject({ timer: null, canvas: 0, active: false });
    await expect.poll(() => page.evaluate(h => h.audio.graph().contextState, handles)).toBe("closed");
});

// RMS of the PUBLISHED screen-audio track (what the encrypted publication sends).
const publishedRMS = page => page.evaluate(async () => {
    const track = window.__noxa.state.shareAudioSender?.track;
    if (!track) return -1;
    const context = new AudioContext(); const analyser = context.createAnalyser();
    context.createMediaStreamSource(new MediaStream([track])).connect(analyser);
    const data = new Float32Array(analyser.fftSize); let peak = 0;
    for (let i = 0; i < 8; i++) {
        await new Promise(r => setTimeout(r, 50));
        analyser.getFloatTimeDomainData(data);
        peak = Math.max(peak, Math.sqrt(data.reduce((sum, v) => sum + v * v, 0) / data.length));
    }
    await context.close();
    return peak;
});

test("audio follows Take: draft capture audio, recapture audio and the audio switch never reach the publication before Take", async ({ page }) => {
    const studio = await open(page);
    await addDisplay(page, RED, "none");
    await studio.locator(".studio-transition").selectOption("cut");
    await page.keyboard.press("Control+Enter");
    await studio.getByRole("button", { name: "Go live" }).click();
    await expect(studio.locator(".studio-air")).toHaveText("Live");
    expect(await publishedRMS(page)).toBeLessThan(0.005);

    // (a) A new capture WITH audio in a draft-only scene: silent until Take.
    await studio.getByRole("button", { name: "Add scene" }).click();
    await addDisplay(page, BLUE, "system");
    await expectPixel(page, "preview", BLUE);
    await expect(studio.locator(".studio-route-audio")).toBeChecked(); // enabled on the draft source
    expect((await diagnostics(page)).routes).toEqual([]);
    expect(await publishedRMS(page)).toBeLessThan(0.005);
    await page.keyboard.press("Control+Enter");
    await expectPixel(page, "program", BLUE);
    expect((await diagnostics(page)).routes).toEqual(["c2"]);
    expect(await publishedRMS(page)).toBeGreaterThan(0.05);

    // (b) Back to the silent scene (Take): the outgoing scene's audio stops at Take, even during a fade.
    await studio.locator(".studio-transition").selectOption("fade");
    await page.keyboard.press("1");
    await page.keyboard.press("Control+Enter");
    expect((await diagnostics(page)).routes).toEqual([]);
    expect(await publishedRMS(page)).toBeLessThan(0.005);

    // (c) The audio switch on a draft-only source: off in the draft, Take, then
    // switching it on in the draft does not reach the publication until Take.
    await page.keyboard.press("2");
    await studio.locator(".studio-source-select").first().click();
    await studio.locator(".studio-route-audio").uncheck();
    await studio.locator(".studio-transition").selectOption("cut");
    await page.keyboard.press("Control+Enter");
    expect((await diagnostics(page)).routes).toEqual([]);
    await studio.locator(".studio-route-audio").check();
    await page.waitForTimeout(200);
    expect((await diagnostics(page)).routes).toEqual([]);
    expect(await publishedRMS(page)).toBeLessThan(0.005);
    await studio.getByRole("button", { name: "Take", exact: true }).click();
    expect((await diagnostics(page)).routes).toEqual(["c2"]);
    expect(await publishedRMS(page)).toBeGreaterThan(0.05);

    // (d) Recapture with audio in the draft while live: the program keeps the
    // old capture's audio until Take; then exactly the new capture is routed.
    await page.evaluate(() => { const t = window.__captures.display[1].getVideoTracks()[0]; t.stop(); t.dispatchEvent(new Event("ended")); });
    await expect(studio.locator(".studio-source[data-state='ended']")).toBeVisible();
    await page.evaluate(() => window.__captures.colors.push([20, 200, 40]));
    await studio.locator(".studio-capture-audio").selectOption("system");
    await studio.getByRole("button", { name: "Recapture Screen", exact: true }).click();
    await expectPixel(page, "preview", GREEN);
    expect((await diagnostics(page)).routes).not.toContain("c3");
    const beforeTake = await publishedRMS(page);
    await page.keyboard.press("Control+Enter");
    await expect.poll(async () => (await diagnostics(page)).routes).toEqual(["c3"]);
    expect(await publishedRMS(page)).toBeGreaterThan(0.05);
    expect(beforeTake).toBeLessThan(0.005);
});

test("audio: a capture shown in two scenes is routed once; nothing monitors to speakers; mic and camera stay out", async ({ page }) => {
    const studio = await open(page);
    await addDisplay(page, RED, "system");
    await studio.getByRole("button", { name: "Add scene" }).click();
    await studio.locator(".studio-reuse").selectOption("c1"); // same capture, second scene
    await addDisplay(page, BLUE, "system");                   // second capture with audio, switched off
    await studio.locator(".studio-source-select").first().click();
    await studio.locator(".studio-route-audio").uncheck();
    await studio.locator(".studio-transition").selectOption("cut");
    await page.keyboard.press("Control+Enter");
    expect((await diagnostics(page)).routes).toEqual(["c1"]);
    await page.keyboard.press("1");
    await studio.locator(".studio-transition").selectOption("fade");
    await page.keyboard.press("Control+Enter");
    await page.waitForTimeout(400);
    expect((await diagnostics(page)).routes).toEqual(["c1"]);
    const session = await page.evaluate(() => {
        const s = window.__studio.studioSessionForTest();
        return { tracks: s.registry.audioTracks().map(item => item.id), speakers: window.__speakerConnections,
            micInGraph: window.__noxa.state.localStream.getAudioTracks().length };
    });
    expect(session.tracks).toEqual(["c1", "c2"]);
    expect(session.speakers).toBe(0);
    expect(session.micInGraph).toBe(0);
    const level = await page.evaluate(async () => {
        const track = window.__studio.studioSessionForTest().audio.track;
        const context = new AudioContext(); const analyser = context.createAnalyser();
        context.createMediaStreamSource(new MediaStream([track])).connect(analyser);
        const data = new Float32Array(analyser.fftSize); let peak = 0;
        for (let i = 0; i < 20 && peak < 0.01; i++) { await new Promise(r => setTimeout(r, 50)); analyser.getFloatTimeDomainData(data); peak = Math.max(peak, ...data.map(Math.abs)); }
        await context.close();
        return peak;
    });
    expect(level).toBeGreaterThan(0.01);
    await addCamera(page, GREEN);
    expect(await page.evaluate(() => window.__captures.cameraConstraints.audio)).toBe(false);
});

test("Ctrl+Enter in a property field takes the value just typed; the preview shows it too", async ({ page }) => {
    const studio = await open(page);
    await addDisplay(page, RED, "none");
    await studio.locator(".studio-transition").selectOption("cut");
    await page.keyboard.press("Control+Enter");
    await expectPixel(page, "program", RED, 0.9, 0.5);
    await studio.locator(".studio-source-select").first().click();
    const width = studio.getByLabel("Width (%)");
    await width.fill("50");
    await page.keyboard.press("Control+Enter"); // still focused in the field
    await expectPixel(page, "program", [0, 0, 0], 0.9, 0.5);
    await expectPixel(page, "preview", [0, 0, 0], 0.9, 0.5);
    await expectPixel(page, "program", RED, 0.25, 0.5);
});

test("images: EXIF-rotated phone JPEGs decode; a raced prune never deletes a just-added image", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const { decodeImage, createAssetStore } = await import("/src/studio-assets.js");
        const canvas = document.createElement("canvas"); canvas.width = 64; canvas.height = 32;
        canvas.getContext("2d").fillRect(0, 0, 64, 32);
        const jpeg = new Uint8Array(await (await new Promise(r => canvas.toBlob(r, "image/jpeg", 0.9))).arrayBuffer());
        // APP1 Exif, big-endian TIFF, one IFD entry: Orientation (0x0112) = 6.
        const tiff = [0x4d, 0x4d, 0x00, 0x2a, 0x00, 0x00, 0x00, 0x08, 0x00, 0x01, 0x01, 0x12, 0x00, 0x03, 0x00, 0x00, 0x00, 0x01, 0x00, 0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00];
        const payload = [0x45, 0x78, 0x69, 0x66, 0x00, 0x00, ...tiff];
        const app1 = [0xff, 0xe1, ((payload.length + 2) >> 8) & 255, (payload.length + 2) & 255, ...payload];
        const rotated = new Uint8Array(jpeg.length + app1.length);
        rotated.set(jpeg.subarray(0, 2)); rotated.set(app1, 2); rotated.set(jpeg.subarray(2), 2 + app1.length);
        const decoded = await decodeImage(rotated).then(d => `ok ${d.width}x${d.height}`, e => e.code);
        // Prune race: keep is empty (the source is not committed yet).
        const name = "noxa-studio-prune-test";
        const store = createAssetStore({ name });
        const added = await store.add(jpeg);
        await store.prune(() => new Set());
        store.dispose();
        const reload = createAssetStore({ name });
        await reload.load([added.id]);
        const survived = !!reload.bitmap(added.id);
        // Once seen referenced and then dropped, it is pruned.
        await reload.prune(() => new Set([added.id]));
        await reload.prune(() => new Set());
        reload.dispose();
        const after = createAssetStore({ name });
        await after.load([added.id]);
        const removed = !after.bitmap(added.id);
        after.dispose();
        return { decoded, survived, removed };
    });
    expect(result).toEqual({ decoded: "ok 32x64", survived: true, removed: true }); // upright (rotated by EXIF)
});

test("images: PNG renders from local bytes, SVG and oversize files are refused", async ({ page }) => {
    const studio = await open(page);
    const png = await page.evaluate(async () => {
        const canvas = new OffscreenCanvas(64, 36); const ctx = canvas.getContext("2d"); ctx.fillStyle = "rgb(20,40,230)"; ctx.fillRect(0, 0, 64, 36);
        return [...new Uint8Array(await (await canvas.convertToBlob({ type: "image/png" })).arrayBuffer())];
    });
    await studio.locator(".studio-image-file").setInputFiles({ name: "logo.png", mimeType: "image/png", buffer: Buffer.from(png) });
    await expectPixel(page, "preview", BLUE);
    await expect(studio.locator(".studio-source-name-label")).toHaveText("logo.png");
    await studio.locator(".studio-image-file").setInputFiles({ name: "evil.png", mimeType: "image/png", buffer: Buffer.from('<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>') });
    await expect(studio.locator(".studio-error")).toHaveText("Only PNG and JPEG images can be used.");
    await studio.locator(".studio-image-file").setInputFiles({ name: "huge.jpg", mimeType: "image/jpeg", buffer: Buffer.alloc(8 * 1024 * 1024 + 1) });
    await expect(studio.locator(".studio-error")).toContainText("too large");
    // The image survives a reload (local asset store); captures do not.
    await page.evaluate(() => window.__studio.disposeStudio());
    await open(page);
    await expectPixel(page, "preview", BLUE);
    expect(await page.evaluate(() => window.__captures.displayCalls + window.__captures.cameraCalls)).toBe(0);
    const stored = await page.evaluate(() => localStorage.getItem("noxa.studio.v1"));
    expect(stored).not.toContain("capture");
});

test("keyboard, accessibility and compact layout", async ({ page }) => {
    await page.setViewportSize({ width: 820, height: 760 });
    const studio = await open(page);
    await expect(studio.getByRole("button", { name: "Take", exact: true })).toBeFocused();
    await expect(studio.getByRole("img", { name: "Live program output" })).toBeVisible();
    await expect(studio.getByRole("img", { name: /Draft preview of scene/ })).toBeVisible();
    await expect(studio.getByRole("meter", { name: "Studio audio level" })).toBeAttached();
    for (let i = 0; i < 3; i++) await studio.getByRole("button", { name: "Add scene" }).click();
    await studio.getByRole("button", { name: "Take", exact: true }).focus();
    await page.keyboard.press("2");
    await expect(studio.getByRole("button", { name: "Scene 2 (key 2)" })).toHaveAttribute("aria-pressed", "true");
    await page.keyboard.press("Control+Enter");
    await expect(studio.getByRole("button", { name: "Scene 2 (key 2), on air" })).toBeVisible();
    // Typing digits in a text field does not switch scenes.
    await studio.locator(".studio-scene-name").fill("Intro 4");
    await studio.locator(".studio-scene-name").press("Enter");
    await expect(studio.getByRole("button", { name: "Intro 4 (key 2), on air" })).toHaveAttribute("aria-pressed", "true");
    // Layer order from the keyboard.
    await studio.getByRole("button", { name: "Text", exact: true }).click();
    await studio.getByRole("button", { name: "Text", exact: true }).click();
    await studio.locator(".studio-source-name").fill("Lower third");
    await studio.locator(".studio-source-name").press("Tab");
    await expect(studio.locator(".studio-source-name-label").first()).toHaveText("Lower third");
    await studio.locator(".studio-source-select").first().focus();
    await page.keyboard.press("Alt+ArrowDown");
    await expect(studio.locator(".studio-source-name-label").last()).toHaveText("Lower third");
    await expect(studio.locator(".studio-source-select").last()).toBeFocused();
    // All interactive controls are named; the compact panel fits without horizontal scroll.
    const unnamed = await studio.evaluate(root => [...root.querySelectorAll("button, input, select, textarea")]
        .filter(el => !el.hidden && el.type !== "file" && !(el.getAttribute("aria-label") || el.textContent.trim() || el.labels?.[0]?.textContent.trim())).map(el => el.className));
    expect(unnamed).toEqual([]);
    const overflow = await studio.locator(".dlg.studio").evaluate(el => el.scrollWidth - el.clientWidth);
    expect(overflow).toBeLessThanOrEqual(1);
    await page.keyboard.press("Escape");
    await expect(studio).toBeHidden();
    await expect(page.locator("#voice-studio")).toBeFocused();
});

test("a click right after editing a field is not lost (the blur commit does not rebuild the clicked list)", async ({ page }) => {
    const studio = await open(page);
    await studio.getByRole("button", { name: "Add scene" }).click();
    await studio.getByRole("button", { name: "Text", exact: true }).click();
    await studio.getByRole("button", { name: "Text", exact: true }).click();
    await studio.locator(".studio-transition").selectOption("fade");
    await studio.locator(".studio-duration").fill("900");          // commits on blur, i.e. on the next mousedown
    await studio.getByRole("button", { name: "Scene 1 (key 1)" }).click();
    await expect(studio.getByRole("button", { name: "Scene 1 (key 1)" })).toHaveAttribute("aria-pressed", "true");
    expect(await page.evaluate(() => window.__studio.studioSessionForTest().studio.transition.ms)).toBe(900);
    await page.keyboard.press("2");
    await studio.locator(".studio-source-select").first().click();
    await studio.getByLabel("Left (%)").fill("10");
    await studio.locator(".studio-source-select").last().click();     // a different source, right after the edit
    await expect(studio.locator(".studio-source-select").last()).toHaveAttribute("aria-pressed", "true");
});

test("the program clock runs in a worker: renders continue when page timers are throttled away", async ({ page }) => {
    // Simulate background throttling: page intervals never fire from here on.
    await page.evaluate(() => { window.__realSetInterval = window.setInterval; window.setInterval = () => 0; });
    const studio = await open(page);
    await addDisplay(page, RED);
    await studio.locator(".studio-transition").selectOption("cut");
    await page.keyboard.press("Control+Enter");
    await studio.getByRole("button", { name: "Add scene" }).click();
    await addDisplay(page, BLUE);
    await studio.locator(".studio-transition").selectOption("fade");
    await page.keyboard.press("Control+Enter");
    // Only ticks can complete a fade: with page timers dead, the worker clock does.
    await expectPixel(page, "program", BLUE);
    await expect.poll(async () => (await diagnostics(page)).transitioning, { timeout: 3000 }).toBe(false);
    // Dispose terminates the clock worker with the session.
    await page.evaluate(() => window.__studio.disposeStudio());
    expect(await page.evaluate(() => window.__studio.studioActive())).toBe(false);
});

test("a failed Go live keeps the Studio session (captures, draft, program) and shows the error; a retry works", async ({ page }) => {
    const studio = await open(page);
    await addDisplay(page, RED);
    await studio.locator(".studio-transition").selectOption("cut");
    await page.keyboard.press("Control+Enter");
    await page.evaluate(() => {
        const control = window.go.main.App.VideoStreamControlForTab;
        window.__restoreControl = () => { window.go.main.App.VideoStreamControlForTab = control; };
        window.go.main.App.VideoStreamControlForTab = async (tab, msg) => {
            if (msg.action === "publish" && msg.active) throw new Error("publish refused");
            return control(tab, msg);
        };
    });
    await studio.getByRole("button", { name: "Go live" }).click();
    await expect(studio.locator(".studio-error")).toContainText("Could not go live");
    const after = await page.evaluate(() => ({ d: window.__studio.studioDiagnostics(), sharing: window.__noxa.state.screenSharing,
        capture: window.__captures.display[0].getVideoTracks()[0].readyState }));
    expect(after.sharing).toBe(false);
    expect(after.capture).toBe("live");
    expect(after.d).toMatchObject({ live: false, program: "sc1", captures: [{ id: "c1", refs: 2, state: "live" }] });
    await expect(studio.locator(".studio-air")).toHaveText("Off air");
    await expectPixel(page, "program", RED);
    // Retry after the server accepts again.
    await page.evaluate(() => window.__restoreControl());
    await studio.getByRole("button", { name: "Go live" }).click();
    await expect(studio.locator(".studio-air")).toHaveText("Live");
    expect(await page.evaluate(() => window.__video.studioShareActive())).toBe(true);
});

for (const failure of ["async", "sync"]) {
    test(`the program clock falls back to a page timer when the clock worker fails (${failure})`, async ({ page }) => {
        await page.evaluate(mode => {
            window.__workerFailures = 0;
            window.Worker = class extends EventTarget {
                constructor() {
                    super();
                    if (mode === "sync") { window.__workerFailures++; throw new Error("worker blocked"); }
                    setTimeout(() => { window.__workerFailures++; this.onerror?.(new Event("error")); }, 20);
                }
                postMessage() {}
                terminate() { this.terminated = true; }
            };
        }, failure);
        const studio = await open(page);
        await addDisplay(page, RED);
        await studio.locator(".studio-transition").selectOption("cut");
        await page.keyboard.press("Control+Enter");
        await studio.getByRole("button", { name: "Add scene" }).click();
        await addDisplay(page, BLUE);
        await studio.locator(".studio-transition").selectOption("fade");
        await page.keyboard.press("Control+Enter");
        await expectPixel(page, "program", BLUE);
        await expect.poll(async () => (await diagnostics(page)).transitioning, { timeout: 3000 }).toBe(false);
        expect(await page.evaluate(() => window.__workerFailures)).toBeGreaterThan(0);
    });
}

test("an image added and removed in the same session is pruned from local storage at once", async ({ page }) => {
    const studio = await open(page);
    const png = await page.evaluate(async () => {
        const canvas = new OffscreenCanvas(16, 9); canvas.getContext("2d").fillRect(0, 0, 16, 9);
        return [...new Uint8Array(await (await canvas.convertToBlob({ type: "image/png" })).arrayBuffer())];
    });
    const keys = () => page.evaluate(() => new Promise(resolve => {
        const request = indexedDB.open("noxa-studio", 1);
        request.onupgradeneeded = () => request.result.createObjectStore("assets");
        request.onsuccess = () => { const db = request.result; const r = db.transaction("assets").objectStore("assets").getAllKeys(); r.onsuccess = () => { db.close(); resolve(r.result.length); }; };
    }));
    await studio.locator(".studio-image-file").setInputFiles({ name: "temp.png", mimeType: "image/png", buffer: Buffer.from(png) });
    await expect(studio.locator(".studio-source-name-label")).toHaveText("temp.png");
    await expect.poll(keys).toBe(1);
    await studio.getByRole("button", { name: "Remove temp.png" }).click();
    await expect.poll(keys).toBe(0);
    expect(await page.evaluate(() => window.__studio.studioSessionForTest().assets.loadedCount())).toBe(0);
});

test("a hidden program source's audio is not streamed; visibility applies to audio only at Take", async ({ page }) => {
    const studio = await open(page);
    await addDisplay(page, RED, "system");
    await studio.locator(".studio-transition").selectOption("cut");
    await page.keyboard.press("Control+Enter");
    await studio.getByRole("button", { name: "Go live" }).click();
    await expect(studio.locator(".studio-air")).toHaveText("Live");
    expect((await diagnostics(page)).routes).toEqual(["c1"]);
    expect(await publishedRMS(page)).toBeGreaterThan(0.05);
    // Hide in the draft: still on air (picture and audio) until Take.
    await studio.getByRole("button", { name: "Hide Screen" }).click();
    expect((await diagnostics(page)).routes).toEqual(["c1"]);
    await page.keyboard.press("Control+Enter");
    await expectPixel(page, "program", [0, 0, 0]);
    expect((await diagnostics(page)).routes).toEqual([]);
    expect(await publishedRMS(page)).toBeLessThan(0.005);
    // Show again in the draft: silent until Take.
    await studio.getByRole("button", { name: "Show Screen" }).click();
    expect((await diagnostics(page)).routes).toEqual([]);
    await page.keyboard.press("Control+Enter");
    expect((await diagnostics(page)).routes).toEqual(["c1"]);
    expect(await publishedRMS(page)).toBeGreaterThan(0.05);
});
