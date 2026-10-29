// studio-audio.js — the Studio's shared-audio graph, separate from pictures.
//
// The microphone is never part of this graph: it stays the existing voice
// track with its own mute/push-to-talk rules. sync() routes exactly the set
// of capture IDs it is given, once per capture (however many sources show
// it). The caller (studio-ui.js programAudio) derives that set from the live
// PROGRAM only and calls sync at Take or when a capture ends, never on draft
// edits, so draft audio cannot reach the publication. Monitoring is a level meter
// (AnalyserNode): nothing is connected to the local speakers, so the preview
// cannot create a feedback loop.

export function createStudioAudio({ AudioContextClass = globalThis.AudioContext } = {}) {
    const context = new AudioContextClass();
    const destination = context.createMediaStreamDestination();
    const mix = context.createGain();
    const meter = context.createAnalyser();
    meter.fftSize = 256;
    mix.connect(destination);
    mix.connect(meter);
    const routes = new Map(); // capture id -> { track, source, gain }
    const levels = new Float32Array(meter.fftSize);
    let disposed = false;

    function unroute(id) {
        const route = routes.get(id);
        if (!route) return;
        routes.delete(id);
        route.source.disconnect();
        route.gain.disconnect();
    }

    return {
        // The single stable audio track handed to the encrypted publication.
        track: destination.stream.getAudioTracks()[0],
        // sync routes exactly the capture IDs in `enabled` that have a live
        // track in `available` (the program-derived set from studio-ui.js).
        sync(available, enabled) {
            if (disposed) return;
            const wanted = new Map();
            for (const { id, track } of available) {
                if (enabled.has(id) && track?.readyState === "live" && !wanted.has(id)) wanted.set(id, track);
            }
            for (const [id, route] of routes) if (wanted.get(id) !== route.track) unroute(id);
            for (const [id, track] of wanted) {
                if (routes.has(id)) continue;
                const source = context.createMediaStreamSource(new MediaStream([track]));
                const gain = context.createGain();
                source.connect(gain);
                gain.connect(mix);
                routes.set(id, { track, source, gain });
            }
            if (routes.size && context.state === "suspended") context.resume().catch(() => {});
        },
        routed() { return [...routes.keys()]; },
        level() {
            if (disposed || !routes.size) return 0;
            meter.getFloatTimeDomainData(levels);
            let sum = 0;
            for (const value of levels) sum += value * value;
            return Math.min(1, Math.sqrt(sum / levels.length) * 4);
        },
        // Graph facts for diagnostics (nothing here connects to context.destination).
        graph() {
            return { routes: routes.size, contextState: context.state };
        },
        dispose() {
            if (disposed) return;
            disposed = true;
            for (const id of [...routes.keys()]) unroute(id);
            mix.disconnect();
            meter.disconnect();
            destination.stream.getTracks().forEach(track => track.stop());
            context.close().catch(() => {});
        },
    };
}
