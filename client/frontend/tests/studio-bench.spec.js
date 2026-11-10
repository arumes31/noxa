import { expect, test } from "./fixtures.js";

// Program renderer benchmark (20): the same scene rendered by the 2D and the
// WebGL path. Opt-in (NOXA_STUDIO_BENCH=1) because timings depend on the
// machine and on whether the browser has a GPU (headless runs often use a
// software rasteriser). "main" is the main-thread time of one program frame;
// "synced" adds a 1-pixel readback, which waits for the frame to finish.
test.skip(!process.env.NOXA_STUDIO_BENCH, "set NOXA_STUDIO_BENCH=1 to run the Studio renderer benchmark");

test("Studio program renderer: 2D vs WebGL frame cost", async ({ page }) => {
    test.setTimeout(120000);
    await page.route("**/studio-bench", route => route.fulfill({ contentType: "text/html", body: "<!doctype html><title>bench</title>" }));
    await page.goto("/studio-bench");
    const result = await page.evaluate(async () => {
        const { StudioEngine } = await import("/src/studio-compositor.js");
        // Four moving 1280x720 video sources from animated canvases.
        const videos = await Promise.all([0, 1, 2, 3].map(async i => {
            const canvas = document.createElement("canvas"); canvas.width = 1280; canvas.height = 720;
            const ctx = canvas.getContext("2d");
            let frame = 0;
            setInterval(() => { ctx.fillStyle = `hsl(${(frame++ * 7 + i * 90) % 360} 70% 50%)`; ctx.fillRect(0, 0, 1280, 720); }, 16);
            const video = document.createElement("video");
            video.muted = true; video.srcObject = canvas.captureStream(60);
            await video.play();
            return video;
        }));
        const registry = { setHolder() {}, drawable: id => videos[Number(id)], dispose() {} };
        const sources = [
            { id: "0", kind: "display", capture: "0", visible: true, x: 0, y: 0, w: 1, h: 1, crop: { l: 0, t: 0, r: 0, b: 0 } },
            { id: "1", kind: "camera", capture: "1", visible: true, x: 0.65, y: 0.65, w: 0.3, h: 0.3, crop: { l: 0.1, t: 0.1, r: 0.1, b: 0.1 } },
            { id: "2", kind: "display", capture: "2", visible: true, x: 0.05, y: 0.05, w: 0.4, h: 0.4, crop: { l: 0, t: 0, r: 0, b: 0 } },
            { id: "3", kind: "display", capture: "3", visible: true, x: 0.5, y: 0.05, w: 0.45, h: 0.45, crop: { l: 0, t: 0, r: 0, b: 0 } },
            { id: "t", kind: "text", visible: true, x: 0.05, y: 0.8, w: 0.6, h: 0.15, text: "Benchmark overlay", size: 48, color: "#ffffff", background: "#000000", crop: { l: 0, t: 0, r: 0, b: 0 } },
        ];
        const scene = Object.freeze({ id: "bench", sources: sources.map(s => Object.freeze(s)) });
        const nextFrame = () => new Promise(resolve => requestAnimationFrame(() => resolve()));
        const measure = async renderer => {
            const engine = new StudioEngine({ registry, width: 1280, height: 720, renderer, setTimer: () => 1, clearTimer() {} });
            engine.take(scene);
            const kind = engine.rendererKind;
            const readback = () => engine.programContext.getImageData(640, 360, 1, 1);
            for (let i = 0; i < 30; i++) { await nextFrame(); engine.renderProgram(performance.now()); readback(); } // warm up
            const main = [], synced = [];
            for (let i = 0; i < 200; i++) {
                // One animation frame between renders: the source canvases
                // repaint every 16 ms, so each sample draws new video frames.
                await nextFrame();
                const t0 = performance.now();
                engine.renderProgram(t0);
                const t1 = performance.now();
                readback();
                const t2 = performance.now();
                main.push(t1 - t0); synced.push(t2 - t0);
            }
            // A run that fell back to another renderer is not that renderer's result.
            const stayed = engine.rendererKind === kind;
            engine.dispose();
            if (!stayed) return { kind, invalid: `switched to ${kind === "webgl" ? "2d" : "webgl"} during the run` };
            const median = list => list.sort((a, b) => a - b)[Math.floor(list.length / 2)];
            const p95 = list => list.sort((a, b) => a - b)[Math.floor(list.length * 0.95)];
            return { kind, mainMedian: median(main), mainP95: p95(main), syncedMedian: median(synced), syncedP95: p95(synced) };
        };
        // Delivered frame rate: the engine runs on its own 30 fps clock for
        // 4 s while a video element plays the published program track.
        const delivered = async renderer => {
            const engine = new StudioEngine({ registry, width: 1280, height: 720, renderer });
            engine.take(scene);
            const kind = engine.rendererKind;
            const video = document.createElement("video");
            video.muted = true; video.srcObject = engine.outputStream();
            await video.play();
            await new Promise(resolve => setTimeout(resolve, 1000)); // settle
            let frames = 0, stop = false;
            const count = () => { if (stop) return; frames++; video.requestVideoFrameCallback(count); };
            video.requestVideoFrameCallback(count);
            const started = performance.now();
            await new Promise(resolve => setTimeout(resolve, 4000));
            stop = true;
            const fps = frames * 1000 / (performance.now() - started);
            video.srcObject.getTracks().forEach(track => track.stop());
            engine.dispose();
            return { kind: engine.gl ? "webgl" : kind, fps: Math.round(fps * 10) / 10 };
        };
        const gl = document.createElement("canvas").getContext("webgl2");
        const info = gl?.getExtension("WEBGL_debug_renderer_info");
        const gpu = info ? gl.getParameter(info.UNMASKED_RENDERER_WEBGL) : gl ? gl.getParameter(gl.RENDERER) : "none";
        return { gpu, twoD: await measure("2d"), webgl: await measure("webgl"),
            delivered2D: await delivered("2d"), deliveredWebGL: await delivered("webgl") };
    });
    console.log(JSON.stringify(result, null, 2));
    expect(result.twoD.kind).toBe("2d");
    expect(result.twoD.invalid).toBeUndefined();
    expect(result.webgl.invalid).toBeUndefined();
    expect(Number.isFinite(result.webgl.mainMedian)).toBe(true);
});
