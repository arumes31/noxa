// Third-party notices for libraries bundled into the desktop frontend and
// the mobile web app outside the media worker (which has
// tools/media-e2ee-notices.mjs). The desktop bundles highlight.js
// (BSD-3-Clause, code-block highlighting) and jsQR (Apache-2.0, safety-code
// scanning); the mobile app bundles jsQR and qrcode-generator (MIT, safety
// codes). Each build emits frontend-licenses.txt next to its output.
import { readFileSync } from "node:fs";
import { join } from "node:path";

const MIT_TEXT = `Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.`;

// qrcode-generator states "Copyright (c) 2009 Kazuhiko Arase ... Licensed
// under the MIT license" in the header of qrcode.js and ships no LICENSE file.
function qrcodeGeneratorLicense(dir) {
    const header = readFileSync(join(dir, "qrcode.js"), "utf8").split("\n").slice(0, 16)
        .filter(line => line.startsWith("//")).map(line => line.replace(/^\/\/ ?/, "").trimEnd())
        .filter(line => !/^-+$/.test(line)).join("\n").trim();
    if (!/Copyright \(c\) 2009 Kazuhiko Arase/.test(header) || !/MIT license/.test(header)) {
        throw new Error("qrcode-generator license header changed; review the notice");
    }
    return `${header}\n\n${MIT_TEXT}`;
}

// [package, license file] or [package, function(dir) returning the text].
export const DESKTOP_PACKAGES = Object.freeze([["highlight.js", "LICENSE"], ["jsqr", "LICENSE"]]);
export const MOBILE_PACKAGES = Object.freeze([["jsqr", "LICENSE"], ["qrcode-generator", qrcodeGeneratorLicense]]);

export function frontendNotices(nodeModules, packages = DESKTOP_PACKAGES) {
    return packages.map(([name, file]) => {
        const dir = join(nodeModules, name);
        const pkg = JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
        const text = typeof file === "function" ? file(dir) : readFileSync(join(dir, file), "utf8");
        return `${name} ${pkg.version} (${pkg.license})\n\n${text.trim()}\n`;
    }).join("\n\n");
}

export function frontendNoticePlugin(nodeModules, packages = DESKTOP_PACKAGES) {
    return {
        name: "frontend-notices",
        generateBundle() { this.emitFile({ type: "asset", fileName: "frontend-licenses.txt", source: frontendNotices(nodeModules, packages) }); },
    };
}
