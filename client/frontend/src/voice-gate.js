import workletURL from "./voice-gate-worklet.js?worker&url";

const gates = new WeakMap();

export function microphoneSendTrack(raw) {
    return gates.get(raw)?.track || raw;
}

export function disposeMicrophoneGate(raw) {
    gates.get(raw)?.dispose();
}

export function updateMicrophoneGate(raw) {
    const gate = gates.get(raw);
    gate?.update();
    return !!gate;
}

export async function replaceMicrophoneGate(previous, next) {
    const gate = gates.get(previous);
    return gate ? createMicrophoneGate(next, gate.readState, gate.onActivity) : next;
}

export async function createMicrophoneGate(raw, readState, onActivity = () => {}) {
    const existing = gates.get(raw);
    if (existing) return existing.track;
    const ctx = new AudioContext({ latencyHint: "interactive" });
    // Raw track enablement remains a UI compatibility signal. Only this
    // local clone feeds the gate; only the processed track reaches WebRTC.
    const capture = raw.clone();
    capture.enabled = true;
    let source, node, destination, disposed = false, failed = false, revision = 0, settingsKey = "", blocked = true;
    const dispose = () => {
        if (disposed) return;
        disposed = true;
        if (gates.get(raw)?.ctx === ctx) gates.delete(raw);
        capture.stop();
        source?.disconnect(); node?.disconnect(); node?.port.close();
        destination?.stream.getTracks().forEach(track => { track.enabled = false; track.stop(); });
        void ctx.close().catch(() => {});
    };
    try {
        await ctx.audioWorklet.addModule(workletURL);
        if (raw.readyState === "ended") throw new DOMException("Microphone ended", "AbortError");
        const channels = Math.min(2, Math.max(1, raw.getSettings().channelCount || 1));
        node = new AudioWorkletNode(ctx, "noxa-microphone-gate", {
            numberOfInputs: 1, numberOfOutputs: 1, outputChannelCount: [channels],
            channelCount: channels, channelCountMode: "explicit",
        });
        source = ctx.createMediaStreamSource(new MediaStream([capture]));
        destination = ctx.createMediaStreamDestination();
        destination.channelCount = channels;
        const track = destination.stream.getAudioTracks()[0];
        track.enabled = false;
        track.contentHint = raw.contentHint || "speech";
        source.connect(node).connect(destination);
        const update = () => {
            if (disposed) return;
            const state = readState();
            const mode = state.settings?.activation_mode || "ptt";
            blocked = !!(failed || state.muted || state.deafened || (mode === "ptt" && !state.pttActive));
            // Hard mute is synchronous. Re-enabling waits for the audio thread
            // to acknowledge the configuration and clear any private pre-roll.
            if (blocked) track.enabled = false;
            const config = { mode, blocked, threshold: (state.settings?.vad_threshold ?? 50) * 0.002 };
            const key = JSON.stringify(config);
            if (key === settingsKey) return;
            settingsKey = key;
            track.enabled = false;
            node.port.postMessage({ ...config, revision: ++revision });
        };
        node.port.onmessage = ({ data }) => {
            if (disposed || data.revision !== revision) return;
            if (data.ready) track.enabled = !blocked;
            if (typeof data.active === "boolean") onActivity(data.active, data.level, raw);
        };
        node.onprocessorerror = () => { failed = true; track.enabled = false; onActivity(false, 0, raw); };
        gates.set(raw, { track, ctx, readState, onActivity, update, dispose });
        // The caller activates only after installing this capture as the
        // current owner. A pending replaceTrack must remain silent.
        await ctx.resume();
        return track;
    } catch (error) { dispose(); throw error; }
}
