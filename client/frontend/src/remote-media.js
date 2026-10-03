import { remoteTrackID, remoteTrackIDs } from "./media-track-id.js";

const sessions = new WeakMap();

export function reconcileRemoteMedia(pc) { sessions.get(pc)?.(); }

// Reconcile track events and completed SDP exchanges. Browser receiver identity
// can survive publisher changes without creating a new MediaStreamTrack.
export function startRemoteMedia(pc, current, onTrack, onRemoved) {
    const bindings = new Map();
    let active = true;
    const remove = (receiver, binding) => {
        if (bindings.get(receiver) !== binding) return;
        bindings.delete(receiver);
        binding.track.removeEventListener("ended", binding.ended);
        onRemoved(binding.track, binding.id);
    };
    const bind = (receiver, track, id) => {
        if (!active || !current()) return;
        if (track.readyState === "ended") id = "";
        const previous = bindings.get(receiver);
        if (previous?.id === id && previous.track === track) return;
        if (previous) remove(receiver, previous);
        if (!id) return;
        const binding = { id, track, ended: () => remove(receiver, binding) };
        bindings.set(receiver, binding);
        track.addEventListener("ended", binding.ended);
        onTrack(track, id);
    };
    const reconcile = () => {
        if (!active || !current()) return;
        const ids = remoteTrackIDs(pc.remoteDescription?.sdp);
        if (!ids.size) return;
        const targets = new Map();
        for (const transceiver of pc.getTransceivers()) {
            const { receiver } = transceiver;
            const id = receiver.track.readyState === "ended" ? "" : ids.get(transceiver.mid) || "";
            targets.set(receiver, { track: receiver.track, id });
        }
        // Publisher-keyed playback controls must release every old owner before
        // any new receiver takes its place, including swaps between two MIDs.
        for (const [receiver, binding] of bindings) {
            const target = targets.get(receiver);
            if (!target || target.id !== binding.id || target.track !== binding.track) remove(receiver, binding);
        }
        for (const [receiver, { track, id }] of targets) bind(receiver, track, id);
    };
    pc.ontrack = event => {
        if (remoteTrackIDs(pc.remoteDescription?.sdp).size && pc.getTransceivers) { reconcile(); return; }
        const transceiver = event.transceiver || pc.getTransceivers?.().find(item => item.receiver.track === event.track);
        bind(event.receiver || transceiver?.receiver || event.track, event.track, remoteTrackID(pc, event.track, transceiver));
    };
    sessions.set(pc, reconcile);
    return () => {
        active = false;
        if (sessions.get(pc) === reconcile) { sessions.delete(pc); pc.ontrack = null; }
        for (const [receiver, binding] of bindings) remove(receiver, binding);
    };
}
