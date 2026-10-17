// Eighty visual blocks divide the measured payload; they are not network packets.
export function measuredUpdateProgress(percent, details) {
    const bytes = details?.downloaded_bytes;
    const total = details?.total_bytes;
    const downloaded = Number.isSafeInteger(bytes) && bytes >= 0 ? bytes : null;
    const size = Number.isSafeInteger(total) && total > 0 ? total : null;
    const ratio = downloaded !== null && size !== null ? Math.min(1, downloaded / size)
        : Number.isFinite(percent) && percent >= 0 ? Math.min(1, percent / 100) : null;
    return { downloaded, total: size, ratio, blocks: ratio === null ? null : Math.floor(ratio * 80) };
}

// Paint only on progress/visibility/size/theme changes. No animation loop or timers.
export function createUpdateCore(canvas) {
    let progress = null, disposed = false;
    const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
    const paint = () => {
        if (disposed || document.hidden || !canvas.isConnected || !canvas.getClientRects().length) return;
        const width = canvas.clientWidth, height = canvas.clientHeight;
        if (!width || !height) return;
        const ratio = Math.min(window.devicePixelRatio || 1, 1.5);
        canvas.width = Math.round(width * ratio);
        canvas.height = Math.round(height * ratio);
        const ctx = canvas.getContext("2d");
        if (!ctx) return;
        ctx.scale(ratio, ratio);
        const style = getComputedStyle(canvas);
        const track = style.getPropertyValue("--upd-track").trim();
        const accent = style.getPropertyValue("--upd-accent").trim();
        const hot = style.getPropertyValue("--upd-hot").trim();
        const turn = reducedMotion.matches || document.documentElement.dataset.reduceMotion === "1" ? 0 : (progress ?? 0) * Math.PI * .6;
        const radius = Math.min(width, height) * .37;
        for (let i = 0; i < 80; i++) {
            const theta = i * 2.399963 + turn, distance = Math.sqrt((i + .5) / 80) * radius;
            const x = width / 2 + Math.cos(theta) * distance, y = height / 2 + Math.sin(theta) * distance;
            ctx.beginPath(); ctx.arc(x, y, 3.5, 0, Math.PI * 2);
            ctx.fillStyle = track; ctx.fill();
            const filled = Math.max(0, Math.min(1, (progress ?? 0) * 80 - i));
            if (!filled) continue;
            ctx.save(); ctx.clip();
            ctx.fillStyle = filled === 1 ? accent : hot;
            ctx.fillRect(x - 3.5, y + 3.5 - 7 * filled, 7, 7);
            ctx.restore();
        }
    };
    reducedMotion.addEventListener("change", paint);
    const resize = new ResizeObserver(paint);
    resize.observe(canvas);
    const theme = new MutationObserver(paint);
    theme.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme", "data-reduce-motion"] });
    document.addEventListener("visibilitychange", paint);
    return {
        update(value) { progress = value; paint(); },
        dispose() { disposed = true; reducedMotion.removeEventListener("change", paint); resize.disconnect(); theme.disconnect(); document.removeEventListener("visibilitychange", paint); },
    };
}
