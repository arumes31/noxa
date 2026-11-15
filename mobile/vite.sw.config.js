// Builds the service worker as one classic script at /app/sw.js. It bundles
// the core modules it needs to flush the outbox while the app is closed.
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";

// The app build (run first) writes index.html with the hashed asset names, so
// its hash changes exactly when the cached shell does.
const build = createHash("sha256").update(readFileSync(new URL("../web/app/index.html", import.meta.url))).digest("hex").slice(0, 12);

export default {
    define: { __SHELL_BUILD__: JSON.stringify(build) },
    base: "/app/",
    publicDir: false,
    build: {
        outDir: "../web/app",
        emptyOutDir: false,
        target: "es2022",
        lib: { entry: "src/sw.js", formats: ["iife"], name: "noxaServiceWorker", fileName: () => "sw.js" },
    },
};
