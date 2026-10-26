// The server serves this bundle under /app/ (internal/web), from web/app.
import { fileURLToPath } from "node:url";
import { readFileSync } from "node:fs";

export default {
    base: "/app/",
    plugins: [{
        name: "ratchet-dependency-notices",
        generateBundle() {
            const notices = ["curves", "hashes"].map(name => {
                const base = new URL(`./node_modules/@noble/${name}/`, import.meta.url);
                const pkg = JSON.parse(readFileSync(new URL("package.json", base), "utf8"));
                return `@noble/${name} ${pkg.version}\n\n${readFileSync(new URL("LICENSE", base), "utf8")}`;
            }).join("\n\n");
            this.emitFile({ type: "asset", fileName: "olm-directory-licenses.txt", source: notices });
            this.emitFile({ type: "asset", fileName: "ratchet-licenses.txt", source: readFileSync(new URL("../internal/e2ee/ratchet/THIRD_PARTY_LICENSES.txt", import.meta.url), "utf8") });
        },
    }],
    build: {
        outDir: "../web/app", emptyOutDir: true, assetsInlineLimit: 0, target: "es2022",
        // Package the reviewed core before the live DM migration imports it.
        // Export preservation keeps this standalone entry usable by follow-on work.
        rollupOptions: {
            preserveEntrySignatures: "strict",
            input: {
                app: fileURLToPath(new URL("./index.html", import.meta.url)),
                directory: fileURLToPath(new URL("./src/core/olm-directory.js", import.meta.url)),
                ratchet: fileURLToPath(new URL("./src/core/ratchet-core.js", import.meta.url)),
            },
        },
    },
    server: { port: 5174 },
};
