import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { runInNewContext } from "node:vm";
import { chromium } from "@playwright/test";
import { preview } from "vite";

const frontend = new URL("../", import.meta.url);
const asset = (directory, name) => readFile(new URL(`${directory}/camera/${name}`, frontend));

test("production camera scripts are minified while runtime assets and globals survive", async () => {
    for (const name of ["segment-worker.js", "vision_wasm_nosimd_internal.js"]) {
        const [source, built] = await Promise.all([asset("public", name), asset("dist", name)]);
        assert.ok(built.length < source.length, `${name} must be minified in dist`);
    }
    const loader = await asset("dist", "vision_wasm_nosimd_internal.js");
    const globals = {};
    runInNewContext(loader.toString(), globals);
    assert.equal(typeof globals.ModuleFactory, "function", "the classic loader must export its global factory");
    for (const name of ["LICENSE.txt", "selfie_segmenter.tflite", "vision_wasm_nosimd_internal.wasm"]) {
        assert.deepEqual(await asset("dist", name), await asset("public", name), `${name} must remain unchanged`);
    }
});

test("the production classic camera worker initializes and segments a transferred frame", { timeout: 30_000 }, async t => {
    const server = await preview({ configFile: false, root: fileURLToPath(frontend), preview: { host: "127.0.0.1", port: 0 } });
    t.after(() => new Promise((resolve, reject) => server.httpServer.close(error => error ? reject(error) : resolve())));
    const browser = await chromium.launch();
    t.after(() => browser.close());
    const page = await browser.newPage();
    const origin = `http://127.0.0.1:${server.httpServer.address().port}`;
    const requests = [], failures = [];
    page.context().on("request", request => requests.push(request.url()));
    page.context().on("requestfailed", request => failures.push(request.url()));
    page.on("pageerror", error => failures.push(error.message));
    // An inert built asset supplies the worker's origin without desktop bridge mocks.
    await page.goto(`${origin}/camera/LICENSE.txt`);
    const result = await page.evaluate(async () => {
        const worker = new Worker("./segment-worker.js");
        const request = (message, transfer = []) => new Promise((resolve, reject) => {
            const timer = setTimeout(() => reject(new Error("Built camera worker timed out")), 20_000);
            worker.onerror = event => { clearTimeout(timer); reject(new Error(event.message)); };
            worker.onmessage = ({ data }) => {
                clearTimeout(timer);
                if (data.error) reject(new Error(data.error)); else resolve(data);
            };
            worker.postMessage(message, transfer);
        });
        try {
            const ready = await request({ type: "init" });
            const canvas = new OffscreenCanvas(320, 180);
            const context = canvas.getContext("2d");
            context.fillStyle = "#f00000";
            context.fillRect(0, 0, canvas.width, canvas.height);
            const frame = canvas.transferToImageBitmap();
            const output = await request({ type: "frame", frame, timestamp: 100 }, [frame]);
            const mask = new Float32Array(output.mask);
            const result = {
                ready: ready.ready,
                width: output.width, height: output.height, pixels: mask.length,
                valid: mask.every(value => Number.isFinite(value) && value >= 0 && value <= 1),
                returnedFrame: [output.frame.width, output.frame.height],
                transferred: frame.width === 0,
            };
            output.frame.close();
            return result;
        } finally { worker.terminate(); }
    });
    assert.equal(result.ready, true);
    assert.ok(result.width > 0 && result.height > 0);
    assert.equal(result.pixels, result.width * result.height);
    assert.equal(result.valid, true);
    assert.equal(result.transferred, true);
    assert.deepEqual(result.returnedFrame, [320, 180]);
    assert.deepEqual(failures, []);
    assert.ok(requests.every(url => url.startsWith(`${origin}/`)), "camera inference must stay offline");
    for (const name of ["segment-worker.js", "vision_bundle.js", "vision_wasm_nosimd_internal.js", "vision_wasm_nosimd_internal.wasm", "selfie_segmenter.tflite"]) {
        assert.ok(requests.includes(`${origin}/camera/${name}`), `${name} must load from the production bundle`);
    }
});
