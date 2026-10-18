// logo-motion.js — the noXa mark (ring, X, waveform bars) drawn from packets
// for the connect splash and the post-update intro. Time-based: a painter runs
// requestAnimationFrame only while its overlay is open and stops on dispose;
// reduced motion paints the finished mark once and nothing else.

const LOGO = 200;

const jitter = (index, salt) => {
    const value = Math.sin(index * 127.1 + salt * 311.7) * 43758.5453;
    return value - Math.floor(value);
};

// logoGeometry returns the mark in a 200×200 box. `order` is a 0..1 position
// along the drawing path (ring clockwise from the top, then the X strokes,
// then the bars outer to inner) used by the "trace" choreography.
export function logoGeometry() {
    const packets = [];
    for (let i = 0; i < 60; i++) {
        const angle = i / 60 * Math.PI * 2 - Math.PI / 2;
        packets.push({ part: "ring", x: 100 + Math.cos(angle) * 92, y: 100 + Math.sin(angle) * 92, size: 7, angle, order: i / 60 });
    }
    [[54, 52, 146, 148], [146, 52, 54, 148]].forEach(([x1, y1, x2, y2], stroke) => {
        const dx = x2 - x1, dy = y2 - y1, length = Math.hypot(dx, dy), nx = -dy / length, ny = dx / length;
        for (let j = 0; j <= 15; j++) {
            const t = j / 15;
            for (const offset of [-6.5, 0, 6.5]) {
                packets.push({ part: "x", x: x1 + dx * t + nx * offset, y: y1 + dy * t + ny * offset, size: 7, order: (stroke + t) / 2 });
            }
        }
    });
    [7, 13, 21, 29, 19, 11].forEach((height, bar) => {
        for (const side of [-1, 1]) {
            const x = 100 + side * (80 - bar * 5.4);
            for (let y = -height; y <= height + 0.1; y += 5) packets.push({ part: "bar", bar, x, y: 100 + y, size: 3.6, order: bar / 6 });
        }
    });
    packets.forEach((packet, index) => { packet.r1 = jitter(index, 1); packet.r2 = jitter(index, 2); });
    return packets;
}

const easeOut = t => 1 - (1 - t) ** 3;
const clamp = t => Math.max(0, Math.min(1, t));

// Choreographies return, for a packet at time t (seconds since start), its
// offset from home, alpha and scale. Both settle to the finished mark.
export const choreographies = {
    // C04: a comet draws the ring, then the X, then the voice bars.
    trace: {
        settle: 2.0,
        place(packet, t) {
            const start = packet.part === "ring" ? packet.order * 0.9 : packet.part === "x" ? 0.95 + packet.order * 0.6 : 1.6 + packet.order * 0.3;
            const p = easeOut(clamp((t - start) / 0.35));
            const angle = packet.angle ?? Math.atan2(packet.y - 100, packet.x - 100);
            return { dx: -Math.sin(angle) * 14 * (1 - p), dy: Math.cos(angle) * 14 * (1 - p), alpha: p, scale: 1 };
        },
    },
    // C20: packets converge from every direction.
    converge: {
        settle: 1.3,
        place(packet, t) {
            const start = packet.r2 * 0.5;
            const p = easeOut(clamp((t - start) / 0.75));
            const angle = packet.r1 * Math.PI * 2, distance = (320 + packet.r2 * 80) * (1 - p);
            return { dx: Math.cos(angle) * distance, dy: Math.sin(angle) * distance, alpha: clamp(p * 2), scale: 1 };
        },
    },
};

// The bars keep moving like a level meter while a connection is pending:
// one level per bar column, so every packet of a bar moves together.
const meter = (packet, t) => {
    if (packet.part !== "bar") return 1;
    const column = packet.bar * 2 + (packet.x < 100 ? 0 : 1);
    return 0.5 + 0.5 * Math.abs(Math.sin(t * (2.1 + jitter(column, 5) * 1.7) + jitter(column, 6) * 6));
};

export const prefersReducedMotion = () => !!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches
    || document.documentElement.dataset.reduceMotion === "1";

// createLogoMotion paints into a canvas. Options: choreography name, `busy`
// (after settling, bars keep metering and a comet keeps orbiting), and a
// clock for tests.
export function createLogoMotion(canvas, { choreography = "trace", busy = false, now = () => performance.now() } = {}) {
    const packets = logoGeometry();
    const plan = choreographies[choreography] || choreographies.trace;
    const reduced = prefersReducedMotion();
    let started = now(), frame = 0, disposed = false, idle = !busy;
    const paint = time => {
        if (disposed || !canvas.isConnected) return false;
        const width = canvas.clientWidth, height = canvas.clientHeight;
        if (!width || !height) return true;
        const dpr = Math.min(window.devicePixelRatio || 1, 2);
        if (canvas.width !== Math.round(width * dpr)) canvas.width = Math.round(width * dpr);
        if (canvas.height !== Math.round(height * dpr)) canvas.height = Math.round(height * dpr);
        const ctx = canvas.getContext("2d");
        if (!ctx) return false;
        const style = getComputedStyle(canvas);
        const color = name => style.getPropertyValue(name).trim();
        const ring = color("--logo-ring") || "#1fb0c4", hi = color("--logo-hi") || "#5fd7e6";
        const lo = color("--logo-lo") || "#16a3b6", barColor = color("--logo-bar") || "#4fd0e0";
        const comet = color("--logo-comet") || "#e3f7ff";
        const scale = Math.min(width, height) / LOGO;
        ctx.setTransform(1, 0, 0, 1, 0, 0);
        ctx.clearRect(0, 0, canvas.width, canvas.height);
        ctx.setTransform(dpr * scale, 0, 0, dpr * scale, dpr * (width - LOGO * scale) / 2, dpr * (height - LOGO * scale) / 2);
        const gradient = ctx.createLinearGradient(0, 40, 0, 160);
        gradient.addColorStop(0, hi);
        gradient.addColorStop(1, lo);
        const t = reduced ? plan.settle + 10 : time;
        for (const packet of packets) {
            const place = reduced ? { dx: 0, dy: 0, alpha: 1 } : plan.place(packet, t);
            if (place.alpha <= 0) continue;
            ctx.globalAlpha = place.alpha;
            ctx.fillStyle = packet.part === "ring" ? ring : packet.part === "bar" ? barColor : gradient;
            const x = packet.x + place.dx, y = packet.y + place.dy;
            if (packet.part === "bar") {
                const level = busy && !reduced && t > plan.settle ? meter(packet, t) : 1;
                const yy = 100 + (y - 100) * level;
                ctx.fillRect(x - packet.size / 2, yy - packet.size / 2, packet.size, packet.size);
            } else {
                ctx.beginPath();
                ctx.arc(x, y, packet.size / 2, 0, Math.PI * 2);
                ctx.fill();
            }
        }
        // The comet leads the ring while it is drawn, then keeps orbiting
        // slowly for as long as the connection is pending.
        if (!reduced && plan === choreographies.trace && (t < 0.95 || busy)) {
            const turn = t < 0.95 ? t / 0.9 : 1 + (t - 0.95) / 2.4;
            const angle = turn * Math.PI * 2 - Math.PI / 2;
            ctx.globalAlpha = 1;
            ctx.shadowColor = hi;
            ctx.shadowBlur = 14;
            ctx.fillStyle = comet;
            ctx.beginPath();
            ctx.arc(100 + Math.cos(angle) * 92, 100 + Math.sin(angle) * 92, 6, 0, Math.PI * 2);
            ctx.fill();
            ctx.shadowBlur = 0;
        }
        ctx.globalAlpha = 1;
        return !reduced && (busy || t < plan.settle);
    };
    const step = () => {
        frame = 0;
        if (paint((now() - started) / 1000)) frame = requestAnimationFrame(step);
        else idle = true;
    };
    step();
    return {
        // Called when the connection resolves: the comet stops and the bars rest.
        settle() {
            busy = false;
            if (idle && !disposed) step();
        },
        restart() { started = now(); if (!frame && !disposed) step(); },
        dispose() { disposed = true; cancelAnimationFrame(frame); frame = 0; },
    };
}
