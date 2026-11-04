// Geometry only: shared by the capture worker and its owning page.
export const MIN_REGION_PX = 64;
const unit = value => Math.min(1, Math.max(0, Number(value) || 0));
const even = value => Math.max(0, Math.floor(value / 2) * 2);

// normalizeRegion turns two drag points (0..1 of the preview) into a rectangle.
export function normalizeRegion(a, b) {
    const x1 = unit(Math.min(a.x, b.x)), y1 = unit(Math.min(a.y, b.y));
    const x2 = unit(Math.max(a.x, b.x)), y2 = unit(Math.max(a.y, b.y));
    return { x: x1, y: y1, width: x2 - x1, height: y2 - y1 };
}

// cropRect is the region in pixels of a frame, aligned to even values so 4:2:0
// frames can be cropped without resampling.
export function cropRect(frameWidth, frameHeight, region) {
    const x = Math.min(even(region.x * frameWidth), even(frameWidth - 2));
    const y = Math.min(even(region.y * frameHeight), even(frameHeight - 2));
    const width = Math.max(2, Math.min(even(region.width * frameWidth), even(frameWidth - x)));
    const height = Math.max(2, Math.min(even(region.height * frameHeight), even(frameHeight - y)));
    return { x, y, width, height };
}

export function regionLargeEnough(region, sourceWidth, sourceHeight) {
    if (!region || !(sourceWidth > 0) || !(sourceHeight > 0)) return false;
    return region.width * sourceWidth >= MIN_REGION_PX && region.height * sourceHeight >= MIN_REGION_PX;
}

const bound = value => {
    if (typeof value === "number") return value > 0 ? value : Infinity;
    const limit = value?.max ?? value?.exact ?? value?.ideal;
    return limit > 0 ? limit : Infinity;
};

// sourceConstraints scales the source so the cropped part fits the output box
// described by `output` (screenShareConstraints of the chosen quality).
export function sourceConstraints(native, region, output = {}, captureRateMultiplier = 1) {
    const cropWidth = Math.max(1, region.width * native.width), cropHeight = Math.max(1, region.height * native.height);
    const scale = Math.min(1, bound(output.width) / cropWidth, bound(output.height) / cropHeight);
    const width = Math.max(2, Math.round(native.width * scale)), height = Math.max(2, Math.round(native.height * scale));
    // Chromium budgets desktop capture time separately from the RTP frame cap.
    // Sampling headroom avoids coupling a 30 fps upload to a capture loop whose
    // scheduling overhead otherwise undershoots 30. Extra sampling stops at 60;
    // explicitly requested higher rates retain their existing behavior.
    const fps = bound(output.frameRate);
    const rate = captureRateMultiplier === 2 && Number.isFinite(fps) && fps < 60 ? Math.min(60, fps * 2) : null;
    const frameRate = rate ? { ideal: rate, max: rate } : output.frameRate;
    return { width: { ideal: width, max: width }, height: { ideal: height, max: height },
        ...(frameRate ? { frameRate } : {}) };
}

// Texture-backed capture may bypass the browser's CPU-size adapter. Align
// both the visible resource and its display size before it reaches H264;
// getSettings()/applyConstraints alone cannot prove the next frame is even.
export function regionFrameLayout(frame, region, output = {}) {
    const visible = frame.visibleRect || { x: 0, y: 0, width: frame.codedWidth, height: frame.codedHeight };
    if (![frame.codedWidth, frame.codedHeight, visible.x, visible.y, visible.width, visible.height, frame.displayWidth, frame.displayHeight].every(Number.isSafeInteger) ||
        visible.x < 0 || visible.y < 0 || visible.width < 2 || visible.height < 2 ||
        frame.displayWidth < 2 || frame.displayHeight < 2) throw new Error("invalid capture frame geometry");
    if (visible.x + visible.width > frame.codedWidth || visible.y + visible.height > frame.codedHeight ||
        bound(output.width) < 2 || bound(output.height) < 2) throw new Error("invalid capture frame bounds");
    const rect = cropRect(visible.width, visible.height, region);
    const displayWidth = frame.displayWidth * rect.width / visible.width;
    const displayHeight = frame.displayHeight * rect.height / visible.height;
    const scale = Math.min(1, bound(output.width) / displayWidth, bound(output.height) / displayHeight);
    const aligned = (value, maximum) => Math.max(2, Math.min(even(maximum), Math.round(value / 2) * 2));
    return { visibleRect: { x: visible.x + rect.x, y: visible.y + rect.y, width: rect.width, height: rect.height },
        displayWidth: aligned(displayWidth * scale, Math.min(frame.displayWidth, bound(output.width))),
        displayHeight: aligned(displayHeight * scale, Math.min(frame.displayHeight, bound(output.height))) };
}
