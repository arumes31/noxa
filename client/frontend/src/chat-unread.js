import "./chat-unread.css";

export function appendUnreadBadge(tab, count, arrivedAt, mention = false) {
    tab.classList.add("has-unread");
    if (Number.isFinite(arrivedAt)) {
        tab.classList.add("unread-arrival");
        // Preserve the arrival/idle phase when another chat rebuilds the bar.
        tab.style.setProperty("--pm-unread-elapsed", `-${Math.max(0, Date.now() - arrivedAt)}ms`);
    }
    const canvas = document.createElement("canvas");
    canvas.className = "pm-unread-plasma";
    canvas.setAttribute("aria-hidden", "true");
    const badge = document.createElement("span");
    badge.className = "pm-unread" + (mention ? " mention" : "");
    badge.textContent = count;
    for (const className of ["pm-unread-halo", "pm-unread-echo"]) {
        const ring = document.createElement("span");
        ring.className = className;
        ring.setAttribute("aria-hidden", "true");
        badge.appendChild(ring);
    }
    tab.append(canvas, badge);
}

// One loop for the tab bar. Opening, closing or switching servers replaces the
// targets; hidden tabs and reduced motion do not keep a painting loop alive.
export function createUnreadTabEffects(root) {
    const motion = window.matchMedia("(prefers-reduced-motion: reduce)");
    const targets = new Map();
    let request = 0, lastPaint = 0;
    const reduced = () => motion.matches || document.documentElement.dataset.reduceMotion === "1";
    const observer = new IntersectionObserver(entries => {
        for (const entry of entries) {
            const target = targets.get(entry.target);
            if (target) target.visible = entry.isIntersecting;
        }
        restart();
    });

    function paint(now) {
        request = 0;
        if (!root.isConnected || document.hidden) return;
        const visible = [...targets].filter(([tab, target]) => tab.isConnected && target.visible);
        if (!visible.length) return;
        const staticFrame = reduced();
        if (staticFrame || now - lastPaint >= 1000 / 30) {
            lastPaint = now;
            const accent = getComputedStyle(root).getPropertyValue("--accent").trim();
            for (const [tab, target] of visible) {
                const elapsed = target.elapsed + now - target.started;
                const fast = target.arrival && elapsed < 2400;
                const phase = staticFrame ? 0.29 : fast ? elapsed / 1200 : (elapsed - (target.arrival ? 2400 : 0)) / 4200;
                drawPlasma(tab, target.canvas, target.context, phase, accent, fast);
            }
        }
        if (!staticFrame) request = requestAnimationFrame(paint);
    }

    function restart() {
        cancelAnimationFrame(request);
        request = 0;
        lastPaint = 0;
        if (!document.hidden && targets.size) paint(performance.now());
    }

    const preferences = new MutationObserver(restart);
    preferences.observe(document.documentElement, { attributes: true, attributeFilter: ["data-reduce-motion", "data-theme"] });
    document.addEventListener("visibilitychange", restart);
    motion.addEventListener("change", restart);
    window.addEventListener("resize", restart);
    return {
        refresh() {
            observer.disconnect();
            targets.clear();
            for (const tab of root.querySelectorAll(".pm-tab.has-unread")) {
                const canvas = tab.querySelector(".pm-unread-plasma");
                const context = canvas?.getContext("2d");
                if (!context) continue;
                targets.set(tab, {
                    canvas, context, visible: false, started: performance.now(),
                    arrival: tab.classList.contains("unread-arrival"),
                    elapsed: -parseFloat(tab.style.getPropertyValue("--pm-unread-elapsed")) || 0,
                });
                observer.observe(tab);
            }
            restart();
        },
        dispose() {
            cancelAnimationFrame(request);
            observer.disconnect();
            preferences.disconnect();
            targets.clear();
            document.removeEventListener("visibilitychange", restart);
            motion.removeEventListener("change", restart);
            window.removeEventListener("resize", restart);
        },
    };
}

function drawPlasma(tab, canvas, context, time, accent, arriving) {
    // CSS canvas dimensions exclude the tab border. Measure both rectangles in
    // the same space instead of mixing offsetWidth with a fixed translation.
    const bounds = canvas.getBoundingClientRect(), tabBounds = tab.getBoundingClientRect();
    const style = getComputedStyle(canvas);
    const width = parseFloat(style.width), height = parseFloat(style.height);
    if (!width || !height || !bounds.width || !bounds.height) return;
    const scaleX = bounds.width / width, scaleY = bounds.height / height;
    const ratio = Math.min(window.devicePixelRatio || 1, 2);
    const pixelsX = Math.round(bounds.width * ratio), pixelsY = Math.round(bounds.height * ratio);
    if (canvas.width !== pixelsX || canvas.height !== pixelsY) { canvas.width = pixelsX; canvas.height = pixelsY; }
    context.setTransform(pixelsX / width, 0, 0, pixelsY / height, 0, 0);
    context.clearRect(0, 0, width, height);
    context.translate((tabBounds.left - bounds.left) / scaleX, (tabBounds.top - bounds.top) / scaleY);
    const w = tabBounds.width / scaleX, h = tabBounds.height / scaleY, r = Math.min(7, h / 2);
    const tau = Math.PI * 2, perimeter = 2 * (w + h - 4 * r) + tau * r;
    const pulse = Math.exp(-(((time % 1 - 0.29) / 0.17) ** 2));
    const point = fraction => {
        let distance = fraction % 1 * perimeter;
        const segment = (length, position) => { if (distance <= length) return position(distance); distance -= length; return null; };
        return segment(w - 2 * r, s => [r + s, 0]) ||
            segment(Math.PI * r / 2, s => [w - r + r * Math.cos(s / r - Math.PI / 2), r + r * Math.sin(s / r - Math.PI / 2)]) ||
            segment(h - 2 * r, s => [w, r + s]) ||
            segment(Math.PI * r / 2, s => [w - r + r * Math.cos(s / r), h - r + r * Math.sin(s / r)]) ||
            segment(w - 2 * r, s => [w - r - s, h]) ||
            segment(Math.PI * r / 2, s => [r + r * Math.cos(s / r + Math.PI / 2), h - r + r * Math.sin(s / r + Math.PI / 2)]) ||
            segment(h - 2 * r, s => [0, h - r - s]) ||
            [r + r * Math.cos(distance / r + Math.PI), r + r * Math.sin(distance / r + Math.PI)];
    };
    context.strokeStyle = context.shadowColor = accent;
    context.lineCap = context.lineJoin = "round";
    for (let layer = 0; layer < 2; layer++) {
        context.beginPath();
        for (let step = 0; step <= 160; step++) {
            const f = step / 160, [x, y] = point(f);
            const ripple = (Math.sin(f * tau * 7 + time * tau * 2 + layer) + Math.sin(f * tau * 13 - time * tau * 1.5)) *
                (0.45 + layer * 0.25) * (0.65 + pulse * 0.65);
            const px = x + (x - w / 2) / (w / 2) * ripple, py = y + (y - h / 2) / (h / 2) * ripple;
            if (step) context.lineTo(px, py); else context.moveTo(px, py);
        }
        context.globalAlpha = (layer ? 0.15 : 0.42) + pulse * (layer ? 0.3 : 0.53);
        context.lineWidth = layer ? 1 : 1.5 + pulse * 0.35;
        context.shadowBlur = (6 + pulse * 10) * (arriving ? 1.25 : 1);
        context.stroke();
    }
}
