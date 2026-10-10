// The same five mist bands, aurora curtains and three inner strands are used by
// the native renderer. This is display-only activity, never an audio-level meter.
export function mistAuroraPreview(sample) {
    const rows = [...sample.querySelectorAll(".overlay-sample-person")].map(person => {
        const canvas = document.createElement("canvas"); canvas.className = "overlay-mist-canvas";
        canvas.setAttribute("aria-hidden", "true"); person.prepend(canvas);
        return { person, canvas, ctx: canvas.getContext("2d"), mask: document.createElement("canvas") };
    });
    let enabled = false, animate = true, frame = 0, started = 0;
    const measure = row => {
        const body = row.person.getBoundingClientRect(), name = row.person.querySelector(".overlay-sample-name").getBoundingClientRect();
        if (!body.width) return;
        const dpr = Math.min(window.devicePixelRatio || 1, 2);
        const scale = Number(sample.style.getPropertyValue("--overlay-scale")) || 1;
        row.w = body.width + 36*scale; row.h = body.height + 144*scale; row.dpr = dpr; row.body = body.height; row.scale = scale;
        row.text = { x: name.x - body.x + 18*scale, y: name.y - body.y + 72*scale, w: name.width, h: name.height };
        row.canvas.width = row.mask.width = Math.ceil(row.w*dpr); row.canvas.height = row.mask.height = Math.ceil(row.h*dpr);
        row.canvas.style.width = `${row.w}px`; row.canvas.style.height = `${row.h}px`;
        const ctx = row.mask.getContext("2d"), pixels = ctx.createImageData(row.mask.width, row.mask.height);
        const smooth = value => { const x = Math.max(0, Math.min(1, value)); return x*x*(3-2*x); };
        const text = row.text;
        for (let y = 0; y < row.mask.height; y++) for (let x = 0; x < row.mask.width; x++) {
            const px = x/dpr, py = y/dpr;
            const fx = Math.min(smooth((px-text.x+12*scale)/(8*scale)), smooth((text.x+text.w+12*scale-px)/(8*scale)));
            const fy = Math.min(smooth((py-text.y+12*scale)/(8*scale)), smooth((text.y+text.h+12*scale-py)/(8*scale)));
            const i = (y*row.mask.width+x)*4;
            pixels.data[i+3] = Math.floor(255*(.65-.55*fx*fy));
        }
        ctx.putImageData(pixels, 0, 0);
    };
    const paint = (row, seconds, index) => {
        if (!row.w) measure(row);
        if (!row.w) return;
        const ctx = row.ctx, scale = row.scale, w = row.w/scale, cy = row.h/scale/2, t = seconds + index*2.43;
        ctx.setTransform(row.dpr*scale, 0, 0, row.dpr*scale, 0, 0); ctx.clearRect(0, 0, w, row.h/scale);
        const gradient = ctx.createLinearGradient(0, 0, w, 0);
        gradient.addColorStop(0, "rgba(53,214,233,0)"); gradient.addColorStop(Math.min(.15,18/w), "#35d6e9");
        gradient.addColorStop(Math.max(.85,1-18/w), "#35d6e9"); gradient.addColorStop(1, "rgba(53,214,233,0)");
        ctx.strokeStyle = gradient; ctx.fillStyle = gradient; ctx.lineCap = "round";
        const edge = x => Math.max(0, Math.sin(Math.PI*x/w))**.48;
        const span = x => edge(x)*(row.body/scale/2+21+Math.exp(-(((x-38)/34)**2))*14);
        const wave = (x, phase = 0) => Math.sin(x*.041-t*1.7+phase)*.72+Math.sin(x*.09+t+phase)*.28;
        const line = (curve, alpha, thickness) => {
            ctx.globalAlpha = alpha; ctx.lineWidth = thickness; ctx.beginPath();
            for (let x = 1; x < w; x += 2) { const y = curve(x); if (x === 1) ctx.moveTo(x,y); else ctx.lineTo(x,y); }
            ctx.stroke();
        };
        for (let layer = 0; layer < 5; layer++) {
            const curve = x => cy+span(x)*.7*Math.sin(x*.022-t*.6+layer*1.1);
            ctx.globalAlpha = .055; ctx.beginPath();
            for (let x = 1; x < w; x += 2) { const y = curve(x)-(7+layer)*edge(x); if (x === 1) ctx.moveTo(x,y); else ctx.lineTo(x,y); }
            for (let x = w-1; x > 0; x -= 2) ctx.lineTo(x,curve(x)+(7+layer)*edge(x));
            ctx.closePath(); ctx.fill(); line(curve,.23,1.1);
        }
        const strength = .8*(.55+.45*Math.sin(t*.9)**2);
        for (let x = 2; x < w; x += 3) {
            const spread = span(x)*(.75+strength*.18*wave(x)), shift = wave(x,1)*spread*.2;
            ctx.globalAlpha = .2+.35*(.5+.5*Math.sin(x*.055-t*1.2)); ctx.lineWidth = 1.4;
            ctx.beginPath(); ctx.moveTo(x,cy-spread+shift); ctx.lineTo(x,cy+spread+shift); ctx.stroke();
        }
        for (let strand = 0; strand < 3; strand++) line(x => cy+edge(x)*(2+strand*3)*Math.sin(x*.03+t*.7+strand*1.7),.2,1);
        ctx.globalAlpha = 1; ctx.globalCompositeOperation = "destination-in";
        ctx.setTransform(1,0,0,1,0,0); ctx.drawImage(row.mask,0,0); ctx.globalCompositeOperation = "source-over";
    };
    const tick = now => {
        frame = 0;
        if (!sample.isConnected) { observer.disconnect(); return; }
        if (!enabled) return;
        if (!started) started = now;
        rows.forEach((row,index) => paint(row,animate ? (now-started)/1000 : 0,index));
        if (animate) frame = requestAnimationFrame(tick);
    };
    const observer = new ResizeObserver(() => {
        if (!sample.isConnected) { observer.disconnect(); return; }
        if (!enabled) return;
        rows.forEach(measure);
        if (!frame) frame = requestAnimationFrame(tick);
    });
    return (style, motion) => {
        enabled = style === "mist-aurora"; animate = motion;
        sample.classList.toggle("overlay-mist-aurora", enabled);
        cancelAnimationFrame(frame); frame = 0;
        if (enabled) { rows.forEach(row => { observer.observe(row.person); measure(row); }); frame = requestAnimationFrame(tick); }
        else observer.disconnect();
    };
}
