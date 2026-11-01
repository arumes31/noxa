// share-region.js — "Region": share a selected area of a screen or window.
// The user drags a rectangle over the live preview in the share dialog. The
// shared track is the source cropped to that rectangle with VideoFrame
// visibleRect (no pixel copy). Size constraints given to the cropped track
// describe its OUTPUT; they are translated to the source so the cropped area
// fits them, which is the same as cropping first and scaling afterwards.

export const MIN_REGION_PX = 64;

export const regionCropSupported = () => typeof globalThis.MediaStreamTrackProcessor === "function" &&
    typeof globalThis.MediaStreamTrackGenerator === "function" && typeof globalThis.VideoFrame === "function";

const processedTracks = new WeakSet();
export function displayStreamHasFrameGeometry(stream) {
    const tracks = stream?.getVideoTracks?.() || [];
    return tracks.length === 1 && processedTracks.has(tracks[0]);
}

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

// cropDisplayStream returns a stream whose video track is `stream`'s video
// cropped to `region`; its audio tracks are passed through. Stopping the
// cropped track stops the source; the source ending ends the cropped track.
export function cropDisplayStream(stream, region, { captureRateMultiplier = 1 } = {}) {
    const source = stream.getVideoTracks()[0];
    const initial = source.getSettings();
    const native = { width: initial.width || 1920, height: initial.height || 1080 };
    const processor = new MediaStreamTrackProcessor({ track: source, maxBufferSize: 1 });
    const track = new MediaStreamTrackGenerator({ kind: "video" });
    processedTracks.add(track);
    const abort = new AbortController();
    let stopped = false, output = {}, deliveredLayout = null;
    const transform = new TransformStream({
        transform(frame, controller) {
            let wrapped = null, transferred = false;
            try {
                const layout = regionFrameLayout(frame, region, output);
                const visible = frame.visibleRect || { x: 0, y: 0, width: frame.codedWidth, height: frame.codedHeight };
                if (Object.keys(layout.visibleRect).every(key => layout.visibleRect[key] === visible[key]) &&
                    layout.displayWidth === frame.displayWidth && layout.displayHeight === frame.displayHeight) {
                    controller.enqueue(frame);
                    transferred = true;
                } else {
                    // VideoFrame's copy constructor wraps the same GPU resource;
                    // no canvas draw, pixel readback or software codec is needed.
                    wrapped = new VideoFrame(frame, layout);
                    controller.enqueue(wrapped);
                    wrapped = null;
                }
                deliveredLayout = layout;
            } finally {
                if (!transferred) frame.close();
                wrapped?.close();
            }
        },
    });
    processor.readable.pipeThrough(transform, { signal: abort.signal })
        .pipeTo(track.writable, { signal: abort.signal }).catch(() => {
            // A failed frame transform must end publication and release capture,
            // rather than leaving a live source behind a frozen generated track.
            if (stopped) return;
            track.stop();
            track.dispatchEvent(new Event("ended"));
        });
    const stopTrack = track.stop.bind(track);
    track.stop = () => {
        if (stopped) return;
        stopped = true;
        abort.abort();
        source.stop();
        stopTrack();
    };
    source.addEventListener("ended", () => {
        if (stopped) return;
        track.stop();
        track.dispatchEvent(new Event("ended"));
    }, { once: true });
    track.getSettings = () => {
        const settings = source.getSettings();
        const width = settings.width || native.width, height = settings.height || native.height;
        // Settings describe delivered frames, including GPU captures whose
        // actual geometry differs from the native track's reported size. During
        // a pause/constraint change keep the last frame until a new one arrives.
        const layout = deliveredLayout || regionFrameLayout({ codedWidth: width, codedHeight: height,
            displayWidth: width, displayHeight: height }, region, output);
        return { ...settings, width: layout.displayWidth, height: layout.displayHeight };
    };
    track.getConstraints = () => ({ ...output });
    track.applyConstraints = async (constraints = {}) => {
        await source.applyConstraints(sourceConstraints(native, region, constraints, captureRateMultiplier));
        output = { ...constraints };
    };
    return { stream: new MediaStream([track, ...stream.getAudioTracks()]), source };
}
