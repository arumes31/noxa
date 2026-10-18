// share-region.js — "Region": share a selected area of a screen or window.
// The user drags a rectangle over the live preview in the share dialog. The
// shared track is the source cropped to that rectangle with VideoFrame
// visibleRect (no pixel copy). Size constraints given to the cropped track
// describe its OUTPUT; they are translated to the source so the cropped area
// fits them, which is the same as cropping first and scaling afterwards.

export const MIN_REGION_PX = 64;

export const regionCropSupported = () => typeof globalThis.MediaStreamTrackProcessor === "function" &&
    typeof globalThis.MediaStreamTrackGenerator === "function" && typeof globalThis.VideoFrame === "function";

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
export function sourceConstraints(native, region, output = {}) {
    const cropWidth = Math.max(1, region.width * native.width), cropHeight = Math.max(1, region.height * native.height);
    const scale = Math.min(1, bound(output.width) / cropWidth, bound(output.height) / cropHeight);
    const width = Math.max(2, Math.round(native.width * scale)), height = Math.max(2, Math.round(native.height * scale));
    return { width: { ideal: width, max: width }, height: { ideal: height, max: height },
        ...(output.frameRate ? { frameRate: output.frameRate } : {}) };
}

// cropDisplayStream returns a stream whose video track is `stream`'s video
// cropped to `region`; its audio tracks are passed through. Stopping the
// cropped track stops the source; the source ending ends the cropped track.
export function cropDisplayStream(stream, region) {
    const source = stream.getVideoTracks()[0];
    const initial = source.getSettings();
    const native = { width: initial.width || 1920, height: initial.height || 1080 };
    const processor = new MediaStreamTrackProcessor({ track: source });
    const track = new MediaStreamTrackGenerator({ kind: "video" });
    const abort = new AbortController();
    let stopped = false, output = {};
    const transform = new TransformStream({
        transform(frame, controller) {
            try {
                const visible = frame.visibleRect || { x: 0, y: 0, width: frame.codedWidth, height: frame.codedHeight };
                const rect = cropRect(visible.width, visible.height, region);
                controller.enqueue(new VideoFrame(frame, { visibleRect: {
                    x: visible.x + rect.x, y: visible.y + rect.y, width: rect.width, height: rect.height } }));
            } finally { frame.close(); }
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
        const rect = cropRect(settings.width || native.width, settings.height || native.height, region);
        return { ...settings, width: rect.width, height: rect.height };
    };
    track.getConstraints = () => ({ ...output });
    track.applyConstraints = async (constraints = {}) => {
        await source.applyConstraints(sourceConstraints(native, region, constraints));
        output = { ...constraints };
    };
    return { stream: new MediaStream([track, ...stream.getAudioTracks()]), source };
}
