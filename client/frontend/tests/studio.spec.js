import { expect, test } from "./fixtures.js";

// Real Chromium, real Studio modules. Studio capture is native: sources come
// from noXa's picker and Go composes and sends the program, so the Go bridge
// is mocked (no capture hardware is used or claimed). The voice peer is the
// same stand-in the media-slots specs use.

const BLUE = [20, 40, 230];

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
            myClientID: "self", myUniqueID: "uid-a", clients: [{ client_id: "peer", channel_id: 1 }], mediaLimits: { video_max_width: 1280, video_max_height: 720, video_max_bitrate: 0 } },
        $: id => document.getElementById(id), sysMsg: message => window.__pub.controls.push(["sys", message]), toast: message => window.__pub.controls.push(["toast", message]) };
        window.go = { main: { App: {
            ChannelOfferMockForTab: async (tab, _sdp, slots) => { window.__pub.offers.push(slots); return "answer"; },
            VideoStreamControlForTab: async (tab, msg) => { window.__pub.controls.push(["control", msg.action, msg.slot, msg.active]); return { ...msg, generation: "1", streams: [] }; },
            SetVideoQualityForTab: async () => "",
            AcquireDisplayCaptureLease: async () => "lease", RefreshDisplayCaptureLease: async () => {}, ReleaseDisplayCaptureLease: async () => {},
        } } };
        window.go.main.App.WebRTCOfferSecureForTab = (tab, sdp, slots) => window.go.main.App.ChannelOfferMockForTab(tab, sdp, slots);
        const channelMedia = await import("/src/channel-media.js");
        const standIn = () => ({ active: true, media: { authenticated: () => true, onAuthenticated() {} }, identity: { channelID: 1 }, async start() { return this; }, stop() { this.active = false; }, setRoster() {}, protectPeer: p => p,
            peerConfig: c => c, bindSenders: async () => {}, syncReceivers: async () => {}, rescope() {}, publicationsChanged() {}, receive: () => ({ ok: false }), status: () => ({ state: "encrypted" }) });
        channelMedia.useChannelMediaFactory(standIn);
        channelMedia.useChannelMediaSession(standIn());
        (await import("/src/modal.js")).initModalSystem();
        window.__events = {};
        window.runtime = { EventsOn: (name, callback) => { (window.__events[name] ||= []).push(callback); return () => {}; } };
        window.__studio = await import("/src/studio-ui.js");
        window.__video = await import("/src/video.js");
        // The Studio button and startup sweep live in the eager launcher; the
        // panel module above is the same instance the button loads on demand.
        (await import("/src/studio-launcher.js")).initStudio();
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
    const stored = await page.evaluate(() => localStorage.getItem("noxa.studio.v1:uid-a"));
    expect(stored).not.toContain("capture");
});

// P3-5: the layout and images belong to one identity: another identity on
// this device starts empty, identity deletion wipes them, and the unscoped
// layout of older builds is dropped rather than handed to whoever opens next.
test("the Studio layout and images are scoped per identity and wiped on identity deletion", async ({ page }) => {
    await page.evaluate(() => localStorage.setItem("noxa.studio.v1", JSON.stringify({ legacy: true })));
    let studio = await open(page);
    await studio.getByRole("button", { name: "Add scene" }).click();
    const png = await page.evaluate(async () => {
        const canvas = new OffscreenCanvas(16, 9); canvas.getContext("2d").fillRect(0, 0, 16, 9);
        return [...new Uint8Array(await (await canvas.convertToBlob({ type: "image/png" })).arrayBuffer())];
    });
    await studio.locator(".studio-image-file").setInputFiles({ name: "mine.png", mimeType: "image/png", buffer: Buffer.from(png) });
    await expect(studio.locator(".studio-source-name-label")).toHaveText("mine.png");
    const databases = () => page.evaluate(async () => (await indexedDB.databases()).map(d => d.name).filter(n => n.startsWith("noxa-studio")).sort());
    await expect.poll(databases).toEqual(["noxa-studio:uid-a"]);
    expect(await page.evaluate(() => localStorage.getItem("noxa.studio.v1"))).toBe(null);
    expect(await page.evaluate(() => localStorage.getItem("noxa.studio.v1:uid-a"))).toContain("mine.png");
    // Another identity sees nothing of it.
    await page.evaluate(() => { window.__studio.disposeStudio(); window.__noxa.state.myUniqueID = "uid-b"; });
    studio = await open(page);
    await expect(studio.locator(".studio-source-name-label")).toHaveCount(0);
    expect(await page.evaluate(() => localStorage.getItem("noxa.studio.v1:uid-b") ?? "")).not.toContain("mine.png");
    // Deleting identity A wipes its layout and images; B keeps its own.
    await page.evaluate(() => { window.__studio.disposeStudio(); window.__noxa.state.myUniqueID = "uid-a"; });
    await open(page);
    await page.evaluate(async () => { (await import("/src/security-settings.js")).forgetIdentityStudio("uid-a"); });
    await expect.poll(() => page.evaluate(() => !!window.__studio.studioSessionForTest())).toBe(false);
    await expect.poll(databases).not.toContain("noxa-studio:uid-a");
    expect(await page.evaluate(() => localStorage.getItem("noxa.studio.v1:uid-a"))).toBe(null);
});

test("keyboard, accessibility and compact layout", async ({ page }) => {
    await page.setViewportSize({ width: 820, height: 760 });
    const studio = await open(page);
    await expect(studio.getByRole("button", { name: "Take", exact: true })).toBeFocused();
    await expect(studio.getByRole("img", { name: "Live program output" })).toBeVisible();
    await expect(studio.getByRole("img", { name: /Draft preview of scene/ })).toBeVisible();
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

test("an image added and removed in the same session is pruned from local storage at once", async ({ page }) => {
    const studio = await open(page);
    const png = await page.evaluate(async () => {
        const canvas = new OffscreenCanvas(16, 9); canvas.getContext("2d").fillRect(0, 0, 16, 9);
        return [...new Uint8Array(await (await canvas.convertToBlob({ type: "image/png" })).arrayBuffer())];
    });
    const keys = () => page.evaluate(() => new Promise(resolve => {
        const request = indexedDB.open("noxa-studio:uid-a", 1);
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

async function installNative(page, status = { available: true }) {
    await page.evaluate(status => {
        window.__native = { starts: [], scenes: [], stops: [] };
        Object.assign(window.go.main.App, {
            NativeScreenShareStatusForTab: async () => ({ monitors: [{ index: 0, width: 1920, height: 1080, primary: true }], windows: [], foreground: "0", ...status }),
            NativeScreenThumbnails: async () => ({}),
            NativeCameras: async () => [],
            StartNativeScreenShareForTab: async (tab, request) => { window.__native.starts.push(request); },
            UpdateNativeScreenShareForTab: async () => {},
            UpdateNativeStudioForTab: async (tab, id, scene) => { window.__native.scenes.push(scene); },
            StopNativeScreenShareForTab: async (tab, id) => { window.__native.stops.push(id); },
        });
    }, status);
}

test("Studio captures through noXa's picker and goes live natively; a Go failure ends it with the reason", async ({ page }) => {
    await installNative(page);
    const studio = await open(page);
    await studio.getByRole("button", { name: "Screen or window", exact: true }).click();
    const pick = page.getByRole("dialog", { name: "Choose a source" });
    await pick.getByRole("button", { name: "Add", exact: true }).click();
    await expect(studio.locator(".studio-source")).toHaveCount(1);
    await studio.getByRole("button", { name: "Take", exact: true }).click();
    await studio.getByRole("button", { name: "Go live", exact: true }).click();
    await expect(studio.locator(".studio-air")).toHaveText("Live");
    const request = await page.evaluate(() => window.__native.starts[0]);
    expect(request.source.studio).toBe(true);
    expect(request.studio.layers.map(layer => layer.source)).toEqual([{ monitor: 0, window: "0" }]);
    expect(await page.evaluate(() => window.__captures.displayCalls + window.__captures.cameraCalls)).toBe(0);
    await page.evaluate(id => { for (const callback of window.__events.native_share || []) callback({ tab: "studio-tab", share: id, state: "failed", reason: "capture" }); }, request.id);
    await expect(studio.locator(".studio-air")).not.toHaveText("Live");
    expect(await page.evaluate(() => window.__pub.controls.filter(([kind]) => kind === "toast").map(([, text]) => text)))
        .toContain("Screen sharing stopped: screen capture failed again after a restart.");
    expect(await page.evaluate(() => window.__native.stops)).toEqual([request.id]);
});

test("without native capture the Studio explains why and cannot capture or go live", async ({ page }) => {
    await installNative(page, { available: false, reason: "server_outdated" });
    const studio = await open(page);
    await expect(studio.locator(".studio-error")).toContainText("too old for screen sharing");
    await expect(studio.getByRole("button", { name: "Screen or window", exact: true })).toBeDisabled();
    await expect(studio.getByRole("button", { name: "Go live", exact: true })).toBeDisabled();
});

