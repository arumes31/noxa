import { t } from "./i18n.js";

// Decorative, synthetic motion only; no microphone or audio processing.
export function initLoginBackground() {
    const overlay = document.getElementById("login-overlay");
    const canvas = document.getElementById("login-background");
    const toggle = document.getElementById("login-motion");
    const context = canvas.getContext("2d");
    if (!context) { toggle.hidden = true; return; }

    const reducedMotion = matchMedia("(prefers-reduced-motion: reduce)");
    const forcedColors = matchMedia("(forced-colors: active)");
    let paused = reducedMotion.matches;
    let pageActive = true;
    let request = null;
    let lastFrame = null;
    let time = 0;
    let width = 0;
    let height = 0;

    function visible() {
        return pageActive && !document.hidden && !overlay.hidden &&
            !overlay.classList.contains("hidden") && overlay.getAttribute("aria-hidden") !== "true";
    }

    function resize() {
        if (!visible() || forcedColors.matches) return;
        width = overlay.clientWidth;
        height = overlay.clientHeight;
        if (!width || !height) return;
        // Keep the decorative backing store bounded on large/high-DPI displays.
        const ratio = Math.min(window.devicePixelRatio || 1, 1.5, Math.sqrt(2_000_000 / (width * height)));
        const pixelWidth = Math.round(width * ratio);
        const pixelHeight = Math.round(height * ratio);
        if (canvas.width !== pixelWidth || canvas.height !== pixelHeight) {
            canvas.width = pixelWidth;
            canvas.height = pixelHeight;
            context.setTransform(ratio, 0, 0, ratio, 0, 0);
        }
        drawAuroraRain(context, width, height, time);
    }

    function frame(now) {
        request = null;
        if (!visible() || paused || forcedColors.matches) return;
        // 30 painted frames per second is enough for this slow ambient motion.
        if (lastFrame === null || now - lastFrame >= 1000 / 30) {
            if (lastFrame !== null) time += Math.min((now - lastFrame) / 1000, .1) * .7;
            lastFrame = now;
            drawAuroraRain(context, width, height, time);
        }
        request = requestAnimationFrame(frame);
    }

    function sync() {
        if (request !== null) cancelAnimationFrame(request);
        request = null;
        lastFrame = null;
        toggle.dataset.paused = String(paused);
        toggle.querySelector("span").textContent = t(paused ? "login.playMotion" : "login.pauseMotion");
        toggle.setAttribute("aria-label", t(paused ? "login.playAnimation" : "login.pauseAnimation"));
        resize();
        if (visible() && !paused && !forcedColors.matches) request = requestAnimationFrame(frame);
    }

    toggle.addEventListener("click", () => { paused = !paused; sync(); });
    reducedMotion.addEventListener("change", () => { paused = reducedMotion.matches; sync(); });
    forcedColors.addEventListener("change", sync);
    document.addEventListener("visibilitychange", sync);
    window.addEventListener("noxa-language-changed", sync);
    window.addEventListener("resize", resize);
    window.addEventListener("pagehide", () => { pageActive = false; sync(); });
    window.addEventListener("pageshow", () => { pageActive = true; sync(); });
    new ResizeObserver(resize).observe(overlay);
    new MutationObserver(sync).observe(overlay, { attributes: true, attributeFilter: ["class", "hidden", "aria-hidden"] });
    sync();
}

function random(seed) {
    const value = Math.sin(seed * 127.1 + 311.7) * 43758.5453;
    return value - Math.floor(value);
}

function drawAuroraRain(context, width, height, time) {
    if (!width || !height) return;
    const color = (alpha, hue) => `hsla(${hue + time * 7}, 85%, 65%, ${alpha})`;
    context.clearRect(0, 0, width, height);
    context.save();
    context.globalCompositeOperation = "screen";

    for (let band = 0; band < 4; band++) {
        for (let strand = 0; strand < 18; strand++) {
            context.beginPath();
            for (let point = 0; point <= 100; point++) {
                const u = point / 100;
                const x = u * width;
                const y = height * .5 + Math.sin(u * 8 + time * .25 + band * .65) * height * .138 +
                    Math.sin(u * 17 - time * .18) * 15.4 + (band - 1.5) * 33 +
                    (strand - 9) * 3 * Math.sin(u * 6 + band + time * .1);
                if (point) context.lineTo(x, y);
                else context.moveTo(x, y);
            }
            context.strokeStyle = color(.065, 150 + band * 45 + strand);
            context.lineWidth = 1;
            context.stroke();
        }
    }

    const columns = Math.min(128, Math.max(1, Math.round(width / 15)));
    for (let layer = 0; layer < 2; layer++) {
        const depth = (layer + 1) / 2;
        for (let column = 0; column < columns; column++) {
            const seed = column + layer * 171;
            const velocity = (14 + random(seed + 7) * 28) * (1 + layer * .22);
            const phase = (random(seed) * height + time * velocity) % (height + 230) - 115;
            const x = (column + .5) / columns * width + (layer % 2) * 4;
            const pulse = .7 + .3 * Math.sin(column * .27 + time * .75 + layer);
            for (let segment = 0; segment < 14; segment++) {
                const y = phase + segment * (7 + depth * 6);
                const alpha = Math.pow(segment / 13, 1.65) * (.16 + depth * .43) * .7 * pulse;
                const length = 3 + depth * 4 + Math.sin(column * .29 + time * .5 + segment * .12) * 2;
                context.fillStyle = color(alpha, 155 + column / columns * 150 + layer * 35);
                context.fillRect(x, y, 1.4 + depth * .7, length);
                if (segment === 13 && depth > .6) {
                    context.fillStyle = color(alpha * .65, 175 + column / columns * 120);
                    context.fillRect(x - 1, y - 1, 3 + depth * 2, length + 2);
                }
            }
        }
    }
    context.restore();
}
