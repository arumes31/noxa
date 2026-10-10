import "./speaking-avatar.css";

const SIZE = 58;

export function speakingAvatar(avatar) {
    const wrapper = document.createElement("span");
    wrapper.className = "speaking-avatar";
    const canvas = document.createElement("canvas");
    canvas.setAttribute("aria-hidden", "true");
    wrapper.append(canvas, avatar);
    return wrapper;
}

// One raster per frame is shared by every visible speaker, including hoisted
// duplicates. Full tree rerenders replace the targets, never start extra loops.
export function createSpeakingAvatars(root) {
    const motion = window.matchMedia("(prefers-reduced-motion: reduce)");
    const source = document.createElement("canvas");
    const context = source.getContext("2d");
    const targets = new Map();
    let request = 0, lastPaint = 0;
    const reducedMotion = () => motion.matches || document.documentElement.dataset.reduceMotion === "1";
    const observer = new IntersectionObserver(entries => {
        for (const entry of entries) {
            const target = targets.get(entry.target);
            if (target) target.visible = entry.isIntersecting;
        }
        restart();
    }, { root });

    function paint(now) {
        request = 0;
        if (!root.isConnected || document.hidden || !context) return;
        const visible = [...targets].filter(([canvas, target]) => canvas.isConnected && target.visible);
        if (!visible.length) return;
        if (reducedMotion() || now - lastPaint >= 1000 / 30) {
            lastPaint = now;
            const ratio = Math.min(window.devicePixelRatio || 1, 2);
            const pixels = Math.round(SIZE * ratio);
            if (source.width !== pixels) source.width = source.height = pixels;
            const accent = getComputedStyle(root).getPropertyValue("--accent").trim() || "#4ad8ed";
            drawPlasma(context, reducedMotion() ? 0.9 : now / 1000 * 1.3, pixels / SIZE, accent);
            for (const [canvas, target] of visible) {
                if (canvas.width !== pixels) canvas.width = canvas.height = pixels;
                target.context.clearRect(0, 0, pixels, pixels);
                target.context.drawImage(source, 0, 0);
            }
        }
        if (!reducedMotion()) request = requestAnimationFrame(paint);
    }

    function restart() {
        cancelAnimationFrame(request);
        request = 0;
        lastPaint = 0;
        if (!document.hidden && targets.size) paint(performance.now());
    }

    document.addEventListener("visibilitychange", restart);
    motion.addEventListener("change", restart);
    const preferences = new MutationObserver(restart);
    preferences.observe(document.documentElement, { attributes: true, attributeFilter: ["data-reduce-motion", "data-theme"] });
    return {
        refresh() {
            observer.disconnect();
            targets.clear();
            for (const canvas of root.querySelectorAll(".speaking-avatar canvas")) {
                const context = canvas.getContext("2d");
                if (!context) continue;
                targets.set(canvas, { context, visible: false });
                observer.observe(canvas);
            }
            restart();
        },
        dispose() {
            cancelAnimationFrame(request);
            observer.disconnect();
            targets.clear();
            document.removeEventListener("visibilitychange", restart);
            motion.removeEventListener("change", restart);
            preferences.disconnect();
        },
    };
}

function drawPlasma(context, time, ratio, accent) {
    context.setTransform(ratio, 0, 0, ratio, 0, 0);
    context.clearRect(0, 0, SIZE, SIZE);
    context.strokeStyle = context.shadowColor = accent;
    context.lineCap = context.lineJoin = "round";
    const pulse = 0.5 + 0.32 * Math.sin(6.3 * time) + 0.18 * Math.sin(10.1 * time + 0.7);
    const wave = angle => 16.2 + 1.1 * Math.sin(9 * angle + time * 5) +
        0.65 * Math.sin(13 * angle - time * 6.4) + 0.45 * Math.sin(5 * angle + time * 2.3) +
        0.7 * Math.cos(angle) ** 2 * pulse;
    function path(start, length, steps, radius, width, alpha, blur) {
        context.beginPath();
        for (let step = 0; step <= steps; step++) {
            const angle = start + step / steps * length;
            const distance = radius(angle);
            const x = SIZE / 2 + Math.cos(angle) * distance;
            const y = SIZE / 2 + Math.sin(angle) * distance;
            if (step === 0) context.moveTo(x, y);
            else context.lineTo(x, y);
        }
        context.lineWidth = width;
        context.globalAlpha = alpha;
        context.shadowBlur = blur * ratio;
        context.stroke();
    }
    path(0, Math.PI * 2, 144, wave, 2.3, 0.28, 4.1);
    path(0, Math.PI * 2, 144, wave, 0.95, 0.95, 2.1);
    for (let side = 0; side < 2; side++) for (let echo = 0; echo < 2; echo++) {
        const sway = (1 + Math.sin(time * 4.3 + side * Math.PI + echo * 0.9)) / 2;
        const radius = angle => 18.5 + echo * 1.8 + 2.1 * sway +
            0.45 * Math.sin(9 * angle + time * 5 + echo) + 0.2 * Math.sin(13 * angle - time * 6.4);
        path(side * Math.PI - 0.67, 1.34, 54, radius, echo ? 0.8 : 1.3, echo ? 0.32 : 0.92, echo ? 2 : 2.8);
    }
}
