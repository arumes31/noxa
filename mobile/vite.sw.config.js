// Builds the service worker as one classic script at /app/sw.js. It bundles
// the core modules it needs to flush the outbox while the app is closed.
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// The app build (run first, into the same outDir — the Dockerfile overrides it
// with --outDir) writes index.html with the hashed asset names, so its hash
// changes exactly when the cached shell does.
function shellBuild() {
    let outDir;
    return {
        name: "noxa-shell-build",
        configResolved(config) { outDir = config.build.outDir; },
        renderChunk(code) {
            const build = createHash("sha256").update(readFileSync(join(outDir, "index.html"))).digest("hex").slice(0, 12);
            return { code: code.replaceAll("__SHELL_BUILD__", JSON.stringify(build)), map: null };
        },
    };
}

export default {
    plugins: [shellBuild()],
    base: "/app/",
    publicDir: false,
    build: {
        outDir: "../web/app",
        emptyOutDir: false,
        target: "es2022",
        lib: { entry: "src/sw.js", formats: ["iife"], name: "noxaServiceWorker", fileName: () => "sw.js" },
    },
};
