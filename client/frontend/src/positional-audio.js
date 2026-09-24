function validPosition(value) {
    return value && typeof value.context === "string" && value.context.length > 0 && value.context.length <= 128 &&
        [value.x, value.y, value.z].every(number => Number.isFinite(number) && Math.abs(number) <= 1e6);
}

// Inserts spatial processing after each speaker's gain/mute and before the
// existing master/deafen chain. Missing/stale game data uses the original path.
export class SpatialVoice {
    constructor(ctx) {
        this.ctx = ctx;
        this.tracks = new Map();
        this.positions = new Map();
        this.listener = null;
    }
    attach(trackID, source, destination, peerID = trackID) {
        this.detach(trackID);
        source.connect(destination);
        this.tracks.set(trackID, { source, destination, peerID: String(peerID), panner: null, spatial: false });
    }
    peer(trackID, peerID) {
        const track = this.tracks.get(trackID);
        if (track) track.peerID = String(peerID);
    }
    detach(trackID) {
        const track = this.tracks.get(trackID);
        if (!track) return;
        track.source.disconnect();
        track.panner?.disconnect();
        this.tracks.delete(trackID);
    }
    local(position, now = Date.now()) {
        const vector = values => Array.isArray(values) && values.length === 3 && values.every(Number.isFinite);
        this.listener = validPosition(position) && vector(position.forward) && vector(position.up) ? { ...position, at: now } : null;
    }
    remote(peerID, position, now = Date.now()) {
        if (validPosition(position)) this.positions.set(String(peerID), { ...position, at: now });
    }
    reset() {
        this.listener = null;
        this.positions.clear();
        this.update(false);
    }
    retainPeers(peerIDs) {
        const allowed = new Set(peerIDs.map(String));
        for (const id of this.positions.keys()) if (!allowed.has(id)) this.positions.delete(id);
    }
    update(enabled, now = Date.now()) {
        const listener = this.listener;
        const active = enabled && listener && now - listener.at < 3000;
        const param = (target, key, value) => target[key]?.setTargetAtTime(value, this.ctx.currentTime, 0.04);
        if (active) {
            for (const [index, axis] of ["X", "Y", "Z"].entries()) {
                param(this.ctx.listener, "position" + axis, listener[axis.toLowerCase()]);
                param(this.ctx.listener, "forward" + axis, listener.forward[index]);
                param(this.ctx.listener, "up" + axis, listener.up[index]);
            }
        }
        for (const [peerID, position] of this.positions) if (now - position.at >= 3000) this.positions.delete(peerID);
        for (const track of this.tracks.values()) {
            const position = this.positions.get(track.peerID);
            const spatial = !!(active && position && position.context === listener.context);
            if (spatial && !track.panner) {
                track.panner = this.ctx.createPanner();
                track.panner.panningModel = "HRTF";
                track.panner.distanceModel = "inverse";
                track.panner.refDistance = 1;
                track.panner.maxDistance = 100;
                track.panner.rolloffFactor = 0.25;
                track.panner.connect(track.destination);
            }
            if (spatial !== track.spatial) {
                track.source.disconnect();
                track.source.connect(spatial ? track.panner : track.destination);
                track.spatial = spatial;
            }
            if (spatial) for (const axis of ["X", "Y", "Z"]) param(track.panner, "position" + axis, position[axis.toLowerCase()]);
        }
    }
}
