// share-region.js — "Region": share a selected area of a screen or window.
// The user drags a rectangle over the live preview in the share dialog. The
// shared track is the source cropped to that rectangle with VideoFrame
// visibleRect (no pixel copy). Size constraints given to the cropped track
// describe its OUTPUT; they are translated to the source so the cropped area
// fits them, which is the same as cropping first and scaling afterwards.

import { regionFrameLayout, sourceConstraints } from "./share-frame-geometry.js";
import { createCaptureFramePipe } from "./share-frame-pipe.js";
export { MIN_REGION_PX, normalizeRegion, cropRect, regionLargeEnough, sourceConstraints, regionFrameLayout } from "./share-frame-geometry.js";

export const regionCropSupported = () => typeof globalThis.MediaStreamTrackProcessor === "function" &&
    typeof globalThis.MediaStreamTrackGenerator === "function" && typeof globalThis.VideoFrame === "function";

const processedTracks = new WeakSet();
export function displayStreamHasFrameGeometry(stream) {
    const tracks = stream?.getVideoTracks?.() || [];
    return tracks.length === 1 && processedTracks.has(tracks[0]);
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
    let stopped = false, output = {}, deliveredLayout = null, constraintsTask = Promise.resolve();
    // Own both tracks before worker construction/transfer can fail.
    let pipe;
    const stopTrack = track.stop.bind(track);
    track.stop = () => {
        if (stopped) return;
        stopped = true;
        pipe?.close();
        source.stop();
        stopTrack();
    };
    try {
        pipe = createCaptureFramePipe(processor.readable, track.writable, {
            region,
            onLayout: layout => { deliveredLayout = layout; },
            onError: () => {
                if (stopped) return;
                track.stop();
                track.dispatchEvent(new Event("ended"));
            },
        });
    } catch (error) {
        track.stop();
        throw error;
    }
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
    track.getConstraints = () => structuredClone(output);
    track.applyConstraints = (constraints = {}) => {
        const requested = structuredClone(constraints);
        const pending = constraintsTask.catch(() => {}).then(async () => {
            if (stopped) throw new DOMException("capture ended", "AbortError");
            await source.applyConstraints(sourceConstraints(native, region, requested, captureRateMultiplier));
            if (stopped) throw new DOMException("capture ended", "AbortError");
            await pipe.setOutput(requested);
            output = requested;
        });
        constraintsTask = pending;
        return pending;
    };
    return { stream: new MediaStream([track, ...stream.getAudioTracks()]), source };
}
