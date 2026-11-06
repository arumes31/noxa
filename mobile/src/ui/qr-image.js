// qr-image.js — renders text as an SVG QR code built from DOM nodes (205),
// so no markup string or data URL is needed under the app's CSP. Encoding
// uses the bundled qrcode-generator (MIT).
import qrcode from "qrcode-generator";

const SVG = "http://www.w3.org/2000/svg";
const QUIET_ZONE = 4; // modules of white border the spec requires

/** The dark modules of a QR code for text, as rows of booleans. */
export function qrModules(text, level = "M") {
    const code = qrcode(0, level);
    code.addData(text, "Byte");
    code.make();
    const size = code.getModuleCount();
    return Array.from({ length: size }, (_, row) => Array.from({ length: size }, (_, col) => code.isDark(row, col)));
}

/** An <svg role="img"> showing text as a QR code, labelled with label. */
export function qrSvg(text, label, doc = document) {
    const modules = qrModules(text);
    const size = modules.length + QUIET_ZONE * 2;
    const svg = doc.createElementNS(SVG, "svg");
    svg.setAttribute("viewBox", `0 0 ${size} ${size}`);
    svg.setAttribute("class", "qr-image");
    svg.setAttribute("role", "img");
    svg.setAttribute("aria-label", label);
    svg.setAttribute("shape-rendering", "crispEdges");
    const background = doc.createElementNS(SVG, "rect");
    background.setAttribute("width", String(size));
    background.setAttribute("height", String(size));
    background.setAttribute("fill", "#fff");
    let d = "";
    modules.forEach((row, y) => row.forEach((dark, x) => {
        if (dark) d += `M${x + QUIET_ZONE} ${y + QUIET_ZONE}h1v1h-1z`;
    }));
    const path = doc.createElementNS(SVG, "path");
    path.setAttribute("d", d);
    path.setAttribute("fill", "#000");
    svg.append(background, path);
    return svg;
}
