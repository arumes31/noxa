// update-logo.js — the update splash: the noXa mark assembled from packets in
// proportion to the bytes actually downloaded ("voice first": the waveform
// bars rise first, then the ring and the X grow out of them).
//
// The arrangement is a pure function of the measured ratio. Between two
// progress events the painter eases the shown ratio toward the measured one
// with a short requestAnimationFrame tween that stops as soon as it arrives;
// there is no idle animation loop, and reduced motion paints the measured
// ratio directly.

const LOGO = 200;
const BAR_SHARE = 0.18;
const PACKET_WINDOW = 0.1;
const TWEEN_MS = 420;

// Deterministic jitter so the order is stable between paints and runs.
const jitter = (index, salt) => {
    const value = Math.sin(index * 127.1 + salt * 311.7) * 43758.5453;
    return value - Math.floor(value);
};

// logoPackets returns the mark in a 200×200 box: the ring, the X (two strokes,
// three packets wide) and the waveform bars on both sides. Each packet carries
// its target, its origin and the slice of download progress that moves it.
export function logoPackets() {
    const packets = [];
    for (let i = 0; i < 60; i++) {
        const angle = i / 60 * Math.PI * 2 - Math.PI / 2;
        packets.push({ part: "ring", x: 100 + Math.cos(angle) * 92, y: 100 + Math.sin(angle) * 92, size: 7 });
    }
    for (const [x1, y1, x2, y2] of [[54, 52, 146, 148], [146, 52, 54, 148]]) {
        const dx = x2 - x1, dy = y2 - y1, length = Math.hypot(dx, dy), nx = -dy / length, ny = dx / length;
        for (let j = 0; j <= 15; j++) {
            const t = j / 15;
            for (const offset of [-6.5, 0, 6.5]) {
                packets.push({ part: "x", x: x1 + dx * t + nx * offset, y: y1 + dy * t + ny * offset, size: 7 });
            }
        }
    }
    const heights = [7, 13, 21, 29, 19, 11];
    heights.forEach((height, bar) => {
        for (const side of [-1, 1]) {
            const x = 100 + side * (80 - bar * 5.4);
            for (let y = -height; y <= height + 0.1; y += 5) {
                packets.push({ part: "bar", bar, x, y: 100 + y, size: 3.6 });
            }
        }
    });
    // Bars occupy the first share of the download, outer to inner; the ring
    // and the X follow in a stable shuffled order, each growing out of the
    // nearest bar group.
    const body = packets.filter(packet => packet.part !== "bar");
    const order = body.map((packet, index) => ({ packet, key: jitter(index, 1) })).sort((a, b) => a.key - b.key);
    order.forEach(({ packet }, rank) => {
        packet.start = BAR_SHARE + rank / Math.max(1, order.length - 1) * (1 - BAR_SHARE - PACKET_WINDOW);
        packet.ox = packet.x < 100 ? 40 : 160;
        packet.oy = 100;
    });
    for (const packet of packets) {
        if (packet.part !== "bar") continue;
        packet.start = packet.bar / heights.length * (BAR_SHARE - PACKET_WINDOW * 0.6);
        packet.ox = packet.x;
        packet.oy = 100;
    }
    return packets;
}

const easeOut = t => 1 - (1 - t) ** 3;

// packetState places one packet for a download ratio in [0, 1]: progress 0
// is still at its origin (not drawn), 1 is home. Unknown progress (null)
// draws nothing but the ghost.
export function packetState(packet, ratio) {
    if (ratio === null || !Number.isFinite(ratio)) return { progress: 0, x: packet.ox, y: packet.oy, alpha: 0 };
    const span = packet.part === "bar" ? PACKET_WINDOW * 0.6 : PACKET_WINDOW;
    // The final byte always lands every packet, whatever float rounding says.
    const linear = ratio >= 1 ? 1 : Math.max(0, Math.min(1, (ratio - packet.start) / span));
    const progress = easeOut(linear);
    return {
        progress: linear,
        x: packet.ox + (packet.x - packet.ox) * progress,
        y: packet.oy + (packet.y - packet.oy) * progress,
        alpha: Math.min(1, linear * 3),
    };
}

const reducedMotion = () => window.matchMedia?.("(prefers-reduced-motion: reduce)").matches
    || document.documentElement.dataset.reduceMotion === "1";

export function createUpdateLogo(canvas) {
    const packets = logoPackets();
    let target = null, shown = null, from = null, startedAt = 0, frame = 0, disposed = false;
    const paint = () => {
        if (disposed || document.hidden || !canvas.isConnected || !canvas.getClientRects().length) return;
        const width = canvas.clientWidth, height = canvas.clientHeight;
        if (!width || !height) return;
        const dpr = Math.min(window.devicePixelRatio || 1, 2);
        canvas.width = Math.round(width * dpr);
        canvas.height = Math.round(height * dpr);
        const ctx = canvas.getContext("2d");
        if (!ctx) return;
        const style = getComputedStyle(canvas);
        const ghost = style.getPropertyValue("--upd-ghost").trim() || "rgba(159,227,255,.12)";
        const ringColor = style.getPropertyValue("--upd-logo-ring").trim() || "#1fb0c4";
        const xTop = style.getPropertyValue("--upd-logo-hi").trim() || "#5fd7e6";
        const xBottom = style.getPropertyValue("--upd-logo-lo").trim() || "#16a3b6";
        const barColor = style.getPropertyValue("--upd-logo-bar").trim() || "#4fd0e0";
        const scale = Math.min(width, height) / LOGO * 0.94;
        ctx.setTransform(dpr * scale, 0, 0, dpr * scale, dpr * (width - LOGO * scale) / 2, dpr * (height - LOGO * scale) / 2);
        const gradient = ctx.createLinearGradient(0, 40, 0, 160);
        gradient.addColorStop(0, xTop);
        gradient.addColorStop(1, xBottom);
        const fillFor = packet => packet.part === "ring" ? ringColor : packet.part === "bar" ? barColor : gradient;
        const dot = (packet, x, y) => {
            const r = packet.size / 2;
            if (packet.part === "bar") ctx.fillRect(x - r, y - r, packet.size, packet.size);
            else { ctx.beginPath(); ctx.arc(x, y, r, 0, Math.PI * 2); ctx.fill(); }
        };
        ctx.fillStyle = ghost;
        for (const packet of packets) dot(packet, packet.x, packet.y);
        for (const packet of packets) {
            const state = packetState(packet, shown);
            if (!state.alpha) continue;
            ctx.globalAlpha = state.alpha;
            ctx.fillStyle = fillFor(packet);
            dot(packet, state.x, state.y);
        }
        ctx.globalAlpha = 1;
    };
    const step = now => {
        frame = 0;
        if (disposed) return;
        const t = Math.min(1, (now - startedAt) / TWEEN_MS);
        shown = from + (target - from) * easeOut(t);
        paint();
        if (t < 1) frame = requestAnimationFrame(step);
        else shown = target;
    };
    const settle = () => {
        cancelAnimationFrame(frame);
        frame = 0;
        if (target === null || shown === null || target <= shown || reducedMotion() || document.hidden) {
            shown = target;
            paint();
            return;
        }
        from = shown;
        startedAt = performance.now();
        frame = requestAnimationFrame(step);
    };
    const repaint = () => { if (!frame) paint(); };
    const resize = new ResizeObserver(repaint);
    resize.observe(canvas);
    const theme = new MutationObserver(repaint);
    theme.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme", "data-reduce-motion"] });
    document.addEventListener("visibilitychange", repaint);
    return {
        update(value) {
            target = value === null || !Number.isFinite(value) ? null : Math.max(0, Math.min(1, value));
            settle();
        },
        dispose() {
            disposed = true;
            cancelAnimationFrame(frame);
            resize.disconnect();
            theme.disconnect();
            document.removeEventListener("visibilitychange", repaint);
        },
    };
}
