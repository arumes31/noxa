// The server serves this bundle under /app/ (internal/web), from web/app.
export default {
    base: "/app/",
    build: { outDir: "../web/app", emptyOutDir: true, assetsInlineLimit: 0, target: "es2022" },
    server: { port: 5174 },
};
