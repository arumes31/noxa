// Mobile keyboards can resize only the visual viewport, not CSS viewport units.
export function installViewport(win = window, doc = document) {
    const viewport = win.visualViewport;
    let frame = 0;
    const update = () => {
        frame = 0;
        // Preserve the user's pinch-to-zoom and panning.
        if (viewport && Math.abs(viewport.scale - 1) > 0.01) return;
        const height = viewport?.height || win.innerHeight;
        if (!Number.isFinite(height) || height <= 0) return;
        doc.documentElement.style.setProperty("--app-height", `${height}px`);
        doc.documentElement.style.setProperty("--app-top", `${Math.max(0, viewport?.offsetTop || 0)}px`);
        doc.documentElement.toggleAttribute("data-keyboard", win.innerHeight - height > 100);
    };
    const schedule = () => { if (!frame) frame = win.requestAnimationFrame(update); };
    viewport?.addEventListener("resize", schedule);
    viewport?.addEventListener("scroll", schedule);
    win.addEventListener("resize", schedule);
    win.addEventListener("pageshow", schedule);
    update();
    return () => {
        if (frame) win.cancelAnimationFrame(frame);
        viewport?.removeEventListener("resize", schedule);
        viewport?.removeEventListener("scroll", schedule);
        win.removeEventListener("resize", schedule);
        win.removeEventListener("pageshow", schedule);
    };
}
