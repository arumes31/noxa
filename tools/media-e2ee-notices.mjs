// Third-party notices for the encrypted-media worker (Task 2): the pinned
// LiveKit worker bundle (Apache-2.0) and the libraries that survive in the
// BUILT worker (loglevel MIT, events MIT, tslib 0BSD, @bufbuild/protobuf
// Apache-2.0 with Google protobuf code under BSD-3-Clause). Both the desktop
// frontend and the PWA emit media-e2ee-licenses.txt next to their build
// output; tools/verify-media-worker-build.mjs checks it against the worker.
import { readFileSync, existsSync } from "node:fs";
import { join } from "node:path";

const PACKAGES = [["livekit-client", ["LICENSE"]], ["loglevel", ["LICENSE-MIT", "LICENSE"]], ["events", ["LICENSE"]], ["tslib", ["LICENSE.txt"]]];

function bufbuild(nodeModules) {
    const dir = join(nodeModules, "@bufbuild", "protobuf");
    const pkg = JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
    // The package ships no LICENSE file; its headers carry the terms.
    const apache = readFileSync(join(nodeModules, "livekit-client", "LICENSE"), "utf8").trim();
    const header = readFileSync(join(dir, "dist", "cjs", "google", "varint.js"), "utf8");
    const google = header.split("\n").filter(line => line.startsWith("//")).map(line => line.replace(/^\/\/ ?/, "")).join("\n").trim();
    return `@bufbuild/protobuf ${pkg.version} (${pkg.license})\n\nCopyright 2021-2024 Buf Technologies, Inc.\nLicensed under the Apache License, Version 2.0:\n\n${apache}\n\nIncludes code derived from Google Protocol Buffers:\n\n${google}\n`;
}

export function mediaE2EENotices(nodeModules) {
    return [...PACKAGES.map(([name, files]) => {
        const dir = join(nodeModules, name);
        const pkg = JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
        const file = files.map(f => join(dir, f)).find(existsSync);
        if (!file) throw new Error(`missing license for ${name}`);
        return `${name} ${pkg.version} (${pkg.license})\n\n${readFileSync(file, "utf8").trim()}\n`;
    }), bufbuild(nodeModules)].join("\n\n");
}

export function mediaE2EENoticePlugin(nodeModules) {
    return {
        name: "media-e2ee-notices",
        generateBundle() { this.emitFile({ type: "asset", fileName: "media-e2ee-licenses.txt", source: mediaE2EENotices(nodeModules) }); },
    };
}
