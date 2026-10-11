// Controlled scene adapted from the native test's motion-source.html. Canvas
// capture is a separate workload, not evidence about native display capture.
export function desktopPhase(elapsedMS) {
    if (!Number.isFinite(elapsedMS) || elapsedMS < 0) throw new Error('Invalid desktop scene time');
    const seconds = elapsedMS / 1000, within = seconds % 60;
    return { name: ['static-text', 'scrolling-text', 'motion'][Math.floor(within / 20)],
        index: Math.floor(within / 20), cycle: Math.floor(seconds / 60), elapsed_ms: elapsedMS,
        phase_elapsed_ms: (within % 20) * 1000 };
}

export function drawDesktopScene(g, elapsedMS, width, height) {
    const phase = desktopPhase(elapsedMS), t = elapsedMS / 1000 % 60, motion = phase.index !== 0;
    g.save(); g.scale(width / 1920, height / 1080);
    g.fillStyle = '#101827'; g.fillRect(0, 0, 1920, 1080);
    g.fillStyle = '#fff'; g.font = 'bold 35px monospace';
    g.fillText(`noXa 1080p capture • ${['STATIC TEXT', 'SCROLLING TEXT', 'HIGH MOTION'][phase.index]}`, 44, 62);
    g.font = '26px monospace';
    g.fillText(`Cycle ${phase.cycle}   phase ${t.toFixed(3)} s`, 44, 110);
    g.fillText('Controlled desktop scene · synthetic canvas capture · target 60 fps', 44, 152);
    g.save(); g.beginPath(); g.rect(40, 190, 920, 835); g.clip();
    g.fillStyle = '#edf2f7'; g.fillRect(40, 190, 920, 835); g.fillStyle = '#102030';
    const offset = phase.index === 1 ? (t * 100) % 72 : 0;
    for (let y = 0; y < 24; y++) {
        g.font = (y % 3 === 0 ? 'bold 24px' : '18px') + ' monospace';
        g.fillText('Readable text 0123456789 ABCDEFGHIJKLMNOPQRSTUVWXYZ', 60, 225 + y * 36 - offset);
    }
    g.restore(); g.save(); g.beginPath(); g.rect(990, 190, 890, 835); g.clip();
    for (let y = 0; y < 14; y++) for (let x = 0; x < 16; x++) {
        const shift = motion ? t * 220 : 0;
        g.fillStyle = `hsl(${(x * 29 + y * 17 + (motion ? t * 50 : 0)) % 360} 75% ${35 + (x + y) % 3 * 16}%)`;
        g.fillRect(990 + ((x * 84 + shift) % 1344) - 250, 190 + y * 70, 80, 66);
    }
    for (let i = 0; i < 10; i++) {
        const a = (motion ? t * 1.3 : 0) + i * .628;
        g.beginPath(); g.arc(1435 + Math.cos(a) * 300, 605 + Math.sin(a) * 300, 20 + i * 2, 0, Math.PI * 2);
        g.fillStyle = i % 2 ? '#fff' : '#000'; g.fill();
    }
    g.restore(); g.strokeStyle = '#fff'; g.lineWidth = 2; g.strokeRect(1, 1, 1918, 1078);
    g.fillStyle = '#fff'; g.font = '18px monospace';
    g.fillText('Synthetic repeatable scene. One WebRTC encoder/upload. Not native display capture.', 44, 1055);
    g.restore();
    return phase;
}
