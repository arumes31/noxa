import { OVERLAY_MOTION_SPEED, OVERLAY_COMB_AMPLITUDE, OVERLAY_CONTOUR_GAP, overlayCombActivity, overlayGlyphContour, overlaySpeakerPhase } from './gaming-overlay-animation-core.js';

// Breathing light halo + glyph-hugging comb. This is decorative activity, never
// an audio meter; native and settings previews share the selected 2.4x / 8px preset.
export function lightHaloPreview(sample) {
    const rows = [...sample.querySelectorAll('.overlay-sample-person')].map(person => {
        const canvas = document.createElement('canvas'); canvas.className = 'overlay-animation-canvas';
        canvas.setAttribute('aria-hidden', 'true'); person.prepend(canvas);
        return { person, canvas, ctx: canvas.getContext('2d'), mask: document.createElement('canvas') };
    });
    let animate = true, frame = 0, started = null;
    const measure = row => {
        const body = row.person.getBoundingClientRect(), name = row.person.querySelector('.overlay-sample-name');
        const bounds = name.getBoundingClientRect();
        if (!body.width) return;
        const scale = Number(sample.style.getPropertyValue('--overlay-scale')) || 1;
        const dpr = Math.min(Math.max(window.devicePixelRatio || 1, 2), 3);
        row.w = body.width + 36 * scale; row.h = body.height + 144 * scale; row.scale = scale; row.dpr = dpr;
        row.cx = 38; row.cy = 72 + body.height / scale / 2;
        row.nameX = (bounds.x - body.x) / scale + 18;
        row.nameY = (bounds.y - body.y) / scale + 72;
        row.phase = overlaySpeakerPhase(row.person.dataset.overlayId || '', name.textContent);
        row.canvas.width = Math.ceil(row.w * dpr); row.canvas.height = Math.ceil(row.h * dpr);
        row.canvas.style.width = `${row.w}px`; row.canvas.style.height = `${row.h}px`;
        row.mask.width = Math.max(1, Math.ceil(bounds.width / scale) + 4);
        row.mask.height = Math.max(1, Math.ceil(bounds.height / scale) + 4);
        const mask = row.mask.getContext('2d', { willReadFrequently: true });
        const style = getComputedStyle(name);
        mask.font = `${style.fontWeight} ${parseFloat(style.fontSize) / scale}px ${style.fontFamily}`;
        const metrics = mask.measureText(name.textContent);
        const ascent = metrics.fontBoundingBoxAscent ?? metrics.actualBoundingBoxAscent;
        const descent = metrics.fontBoundingBoxDescent ?? metrics.actualBoundingBoxDescent;
        const baseline = (bounds.height / scale - ascent - descent) / 2 + ascent;
        mask.textBaseline = 'alphabetic'; mask.fillStyle = '#fff'; mask.fillText(name.textContent, 0, baseline);
        const pixels = mask.getImageData(0, 0, row.mask.width, row.mask.height).data;
        const alpha = new Uint8Array(row.mask.width * row.mask.height);
        for (let i = 0; i < alpha.length; i++) alpha[i] = pixels[i * 4 + 3];
        row.contour = overlayGlyphContour(alpha, row.mask.width, row.mask.height);
    };
    const paint = (row, seconds) => {
        if (!row.w) measure(row);
        if (!row.w) return;
        const ctx = row.ctx, scale = row.scale, t = seconds * OVERLAY_MOTION_SPEED + row.phase;
        const color = row.person.style.getPropertyValue('--overlay-color') || '#35d6e9';
        ctx.setTransform(row.dpr * scale, 0, 0, row.dpr * scale, 0, 0);
        ctx.clearRect(0, 0, row.w / scale, row.h / scale);
        const pulse = .5 + .5 * Math.sin(t * 4), radius = (23 + 3 * pulse) * 1.25;
        ctx.globalAlpha = .12 + .08 * pulse; ctx.fillStyle = color;
        ctx.beginPath(); ctx.arc(row.cx, row.cy, 23.75, 0, Math.PI * 2); ctx.fill(); ctx.globalAlpha = 1;
        const gradient = ctx.createRadialGradient(row.cx, row.cy, 20, row.cx, row.cy, radius + 3.75);
        gradient.addColorStop(0, color + '00'); gradient.addColorStop(.45, color + 'a0'); gradient.addColorStop(1, color + '00');
        ctx.fillStyle = gradient; ctx.beginPath(); ctx.arc(row.cx, row.cy, radius + 3.75, 0, Math.PI * 2); ctx.fill();
        ctx.beginPath(); ctx.arc(row.cx, row.cy, radius, 0, Math.PI * 2);
        ctx.strokeStyle = '#06111b'; ctx.lineWidth = 4.875; ctx.stroke();
        ctx.strokeStyle = color; ctx.lineWidth = 3.25; ctx.stroke();
        ctx.lineCap = 'butt';
        const edge = (x, y, length) => {
            ctx.globalAlpha = .88; ctx.beginPath(); ctx.moveTo(x, y); ctx.lineTo(x, y + length);
            ctx.strokeStyle = '#06111b'; ctx.lineWidth = 2.9; ctx.stroke();
            ctx.strokeStyle = color; ctx.lineWidth = 1.4; ctx.stroke(); ctx.globalAlpha = 1;
        };
        for (const column of row.contour) {
            const x = row.nameX + column.x, top = row.nameY + column.top, bottom = row.nameY + column.bottom;
            const activity = overlayCombActivity(column.x + 68, seconds, row.phase);
            const length = 1 + OVERLAY_COMB_AMPLITUDE * activity;
            ctx.globalAlpha = .13 + .14 * activity; ctx.fillStyle = color;
            ctx.fillRect(x - .7, top, 1.4, bottom - top); ctx.globalAlpha = 1;
            edge(x, top - OVERLAY_CONTOUR_GAP, -length);
            edge(x, bottom + OVERLAY_CONTOUR_GAP, length);
        }
    };
    const tick = now => {
        frame = 0;
        if (!sample.isConnected) { observer.disconnect(); return; }
        if (started === null) started = now;
        rows.forEach(row => paint(row, animate ? (now - started) / 1000 : 0));
        if (animate) frame = requestAnimationFrame(tick);
    };
    const observer = new ResizeObserver(() => {
        if (!sample.isConnected) { observer.disconnect(); return; }
        rows.forEach(measure);
        if (!frame) frame = requestAnimationFrame(tick);
    });
    document.fonts?.ready.then(() => {
        if (!sample.isConnected) return;
        rows.forEach(measure);
        if (!frame) frame = requestAnimationFrame(tick);
    });
    return motion => {
        animate = motion;
        cancelAnimationFrame(frame); frame = 0;
        rows.forEach(row => { observer.observe(row.person); measure(row); });
        frame = requestAnimationFrame(tick);
    };
}
