// Builds the service worker as one classic script at /app/sw.js. It bundles
// the core modules it needs to flush the outbox while the app is closed.
export default {
    base: "/app/",
    publicDir: false,
    build: {
        outDir: "../web/app",
        emptyOutDir: false,
        target: "es2022",
        lib: { entry: "src/sw.js", formats: ["iife"], name: "noxaServiceWorker", fileName: () => "sw.js" },
    },
};
