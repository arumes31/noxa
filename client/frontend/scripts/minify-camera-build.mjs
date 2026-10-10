import { readdir, readFile, writeFile } from "node:fs/promises";
import { minify } from "vite";

// Vite copies public assets unchanged. Minify only the generated copies so the
// pinned MediaPipe source files and their provenance checks stay intact.
const directory = new URL("../dist/camera/", import.meta.url);
for (const name of (await readdir(directory)).filter(name => name.endsWith(".js")).sort()) {
    const path = new URL(name, directory);
    const source = await readFile(path, "utf8");
    const result = await minify(name, source, {
        // importScripts shares globals across classic scripts (ModuleFactory).
        module: false,
        mangle: { toplevel: false },
        compress: false,
        codegen: { legalComments: "inline" },
    });
    if (result.errors.length) throw new Error(`Cannot minify ${name}: ${JSON.stringify(result.errors)}`);
    // The upstream bundle is already minified; do not expand it by reprinting.
    const code = Buffer.byteLength(result.code) < Buffer.byteLength(source) ? result.code : source;
    await writeFile(path, code);
    console.log(`camera/${name}: ${Buffer.byteLength(source)} -> ${Buffer.byteLength(code)} bytes`);
}
