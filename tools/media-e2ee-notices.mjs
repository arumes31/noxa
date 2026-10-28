// Third-party notices for the encrypted-media worker (Task 2): the pinned
// LiveKit worker bundle (Apache-2.0) and the libraries it bundles (loglevel
// MIT, events MIT, tslib 0BSD). Both the desktop frontend and the PWA emit
// media-e2ee-licenses.txt next to their build output.
import { readFileSync, existsSync } from "node:fs";
import { join } from "node:path";

const PACKAGES = [["livekit-client", ["LICENSE"]], ["loglevel", ["LICENSE-MIT", "LICENSE"]], ["events", ["LICENSE"]], ["tslib", ["LICENSE.txt"]]];

export function mediaE2EENotices(nodeModules) {
    return PACKAGES.map(([name, files]) => {
        const dir = join(nodeModules, name);
        const pkg = JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
        const file = files.map(f => join(dir, f)).find(existsSync);
        if (!file) throw new Error(`missing license for ${name}`);
        return `${name} ${pkg.version} (${pkg.license})\n\n${readFileSync(file, "utf8").trim()}\n`;
    }).join("\n\n");
}

export function mediaE2EENoticePlugin(nodeModules) {
    return {
        name: "media-e2ee-notices",
        generateBundle() { this.emitFile({ type: "asset", fileName: "media-e2ee-licenses.txt", source: mediaE2EENotices(nodeModules) }); },
    };
}
