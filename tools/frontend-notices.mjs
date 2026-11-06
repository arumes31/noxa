// Third-party notices for libraries bundled into the desktop frontend outside
// the media worker (which has tools/media-e2ee-notices.mjs): highlight.js
// (BSD-3-Clause), loaded lazily for code-block highlighting. The build emits
// frontend-licenses.txt next to its output.
import { readFileSync } from "node:fs";
import { join } from "node:path";

const PACKAGES = [["highlight.js", "LICENSE"]];

export function frontendNotices(nodeModules) {
    return PACKAGES.map(([name, file]) => {
        const dir = join(nodeModules, name);
        const pkg = JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
        return `${name} ${pkg.version} (${pkg.license})\n\n${readFileSync(join(dir, file), "utf8").trim()}\n`;
    }).join("\n\n");
}

export function frontendNoticePlugin(nodeModules) {
    return {
        name: "frontend-notices",
        generateBundle() { this.emitFile({ type: "asset", fileName: "frontend-licenses.txt", source: frontendNotices(nodeModules) }); },
    };
}
