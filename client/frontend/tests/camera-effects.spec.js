import { expect, test } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {};
        window.runtime = { EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
        const settings = { language: "en", camera_fps: 30, camera_background: "none", camera_background_scene: "slate", onboarding_done: true, alpha_dismissed: "test", bookmarks: [], chat_max_lines: 200 };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            if (method === "GetSettings") return structuredClone(settings);
            if (method === "SaveSettings") { Object.assign(settings, args[0]); window.__cameraSaved = args[0]; return ""; }
            if (["ListTabs", "GetPermissions"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (["Connected", "IsGuest"].includes(method)) return false;
            if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
            return "";
        }; } }) } };
        Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: {
            enumerateDevices: async () => [{ kind: "videoinput", deviceId: "usb-camera", label: "USB camera" }],
            getUserMedia: async constraints => {
                window.__cameraConstraint = constraints;
                const canvas = document.createElement("canvas"); canvas.width = 320; canvas.height = 180;
                const ctx = canvas.getContext("2d"); ctx.fillStyle = "#f00000"; ctx.fillRect(0, 0, 320, 180);
                const stream = canvas.captureStream(15);
                window.__rawCamera = stream.getVideoTracks()[0];
                return stream;
            },
        } });
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.openSettings);
});

test("camera selection and background preferences persist without enabling capture", async ({ page }) => {
    await page.evaluate(() => window.__noxa.openSettings("camera"));
    await page.getByLabel("Camera device", { exact: true }).selectOption("usb-camera");
    await page.getByLabel("Background effect", { exact: true }).selectOption("replace");
    await page.getByLabel("Replacement background", { exact: true }).selectOption("warm");
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    expect(await page.evaluate(() => window.__cameraSaved.camera_device_id)).toBe("usb-camera");
    expect(await page.evaluate(() => window.__cameraSaved.camera_background_scene)).toBe("warm");
    expect(await page.evaluate(() => window.__rawCamera)).toBeUndefined();
    await page.getByRole("button", { name: "Test camera", exact: true }).click();
    await expect(page.getByLabel("Camera test preview")).toBeVisible({ timeout: 30000 });
    expect(await page.evaluate(() => window.__cameraConstraint.video.deviceId)).toEqual({ exact: "usb-camera" });
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(await page.evaluate(() => window.__rawCamera.readyState)).toBe("ended");
});

test("offline segmentation replaces background pixels and stops source when output is stopped", async ({ page }) => {
    const external = [];
    page.on("request", request => { if (!request.url().startsWith("http://127.0.0.1:12364") && !request.url().startsWith("data:")) external.push(request.url()); });
    const result = await page.evaluate(async () => {
        const { captureCamera } = await import("/src/camera-capture.js");
        const capture = await captureCamera({ camera_background: "replace", camera_background_scene: "slate" });
        const video = document.createElement("video"); video.muted = true; video.srcObject = capture.stream;
        await video.play();
        await new Promise(resolve => setTimeout(resolve, 600));
        const canvas = document.createElement("canvas"); canvas.width = 320; canvas.height = 180;
        canvas.getContext("2d").drawImage(video, 0, 0);
        const pixel = Array.from(canvas.getContext("2d").getImageData(2, 2, 1, 1).data);
        capture.stream.getVideoTracks()[0].stop();
        video.srcObject = null;
        return { pixel, source: window.__rawCamera.readyState };
    });
    expect(result.pixel[0]).toBeLessThan(100);
    expect(result.pixel[3]).toBe(255);
    expect(result.source).toBe("ended");
    expect(external).toEqual([]);
});

test("effect failure releases the raw camera instead of silently exposing it", async ({ page }) => {
    await page.route("**/camera/selfie_segmenter.tflite", route => route.abort());
    const error = await page.evaluate(async () => {
        const { captureCamera } = await import("/src/camera-capture.js");
        try { await captureCamera({ camera_background: "blur" }); return "unexpected success"; }
        catch (error) { return error.message; }
    });
    expect(error).not.toBe("unexpected success");
    expect(await page.evaluate(() => window.__rawCamera?.readyState)).not.toBe("live");
});

test("closing settings cancels an effect still loading and releases hardware immediately", async ({ page }) => {
    let release;
    const gate = new Promise(resolve => { release = resolve; });
    let requested;
    const ready = new Promise(resolve => { requested = resolve; });
    await page.route("**/camera/selfie_segmenter.tflite", async route => {
        requested(); await gate; await route.abort();
    });
    await page.evaluate(() => window.__noxa.openSettings("camera"));
    await page.getByLabel("Background effect", { exact: true }).selectOption("blur");
    await page.getByRole("button", { name: "Test camera", exact: true }).click();
    try {
        await ready;
        await page.getByRole("button", { name: "Cancel", exact: true }).click();
        expect(await page.evaluate(() => window.__rawCamera.readyState)).toBe("ended");
    } finally { release(); }
});

test("canceling the first bitmap never creates an orphan processed track", async ({ page }) => {
    await page.evaluate(async () => {
        const originalBitmap = window.createImageBitmap;
        window.createImageBitmap = async (...args) => {
            const bitmap = await originalBitmap(...args);
            return new Promise(resolve => { window.__releaseBitmap = () => resolve(bitmap); });
        };
        const originalCapture = HTMLCanvasElement.prototype.captureStream;
        window.__canvasCaptures = 0;
        HTMLCanvasElement.prototype.captureStream = function (...args) { window.__canvasCaptures++; return originalCapture.apply(this, args); };
        window.__cameraAbort = new AbortController();
        const { captureCamera } = await import("/src/camera-capture.js");
        window.__cameraPending = captureCamera({ camera_background: "blur" }, undefined, { signal: window.__cameraAbort.signal }).then(
            capture => { capture.stop(); return "unexpected success"; }, error => error.name,
        );
    });
    await page.waitForFunction(() => typeof window.__releaseBitmap === "function");
    const result = await page.evaluate(async () => {
        window.__cameraAbort.abort(); window.__releaseBitmap();
        return { error: await window.__cameraPending, captures: window.__canvasCaptures, source: window.__rawCamera.readyState };
    });
    expect(result).toEqual({ error: "AbortError", captures: 1, source: "ended" });
});

test("custom camera image, blur strength and local mirroring are saved and previewed", async ({page}) => {
    await page.evaluate(()=>window.__noxa.openSettings('camera'));
    const encoded = await page.evaluate(()=>{const canvas=document.createElement('canvas');canvas.width=32;canvas.height=24;canvas.getContext('2d').fillRect(0,0,32,24);return canvas.toDataURL('image/png').split(',')[1];});
    await page.getByLabel('Choose background image',{exact:true}).setInputFiles({name:'background.png',mimeType:'image/png',buffer:Buffer.from(encoded,'base64')});
    await expect(page.getByLabel('Replacement background',{exact:true})).toHaveValue('custom');
    await page.getByLabel('Blur strength',{exact:true}).fill('22');
    await page.getByLabel('Mirror my preview only',{exact:true}).uncheck();
    await page.getByLabel('Mirror my preview only',{exact:true}).check();
    await page.getByRole('button',{name:'Test camera',exact:true}).click();
    const preview=page.getByLabel('Camera test preview');await expect(preview).toBeVisible();
    expect(await preview.evaluate(video=>video.style.transform)).toBe('scaleX(-1)');
    await page.locator('#set-apply').click();
    await expect(page.locator('.settings-save-status')).toHaveText('Changes applied');
    const saved=await page.evaluate(()=>window.__noxa.state.settings);
    expect(saved.camera_background_image).toMatch(/^data:image\/jpeg;base64,/);expect(saved.camera_blur_strength).toBe(22);expect(saved.camera_mirror_preview).toBe(true);
});
