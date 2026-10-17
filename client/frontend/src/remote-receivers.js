// Ownership of remote receiver tracks by negotiated publisher slot.
//
// A receiver MediaStreamTrack's browser id is fixed from the a=msid present
// when its transceiver was created ("alice|cam"). When the server reuses that
// idle media section (same MID) for another publisher, the id stays "alice|cam"
// although the section now carries "bob|cam"; after alice rejoins on a new MID,
// two live receivers share one id. Browser ids therefore never identify a
// publisher. remote-media.js resolves the publisher track ID ("<client>|<slot>")
// from the CURRENT remote description on every reconcile; registries created
// here key state by that negotiated ID and remember the exact receiver track
// object, so:
//   - a reused receiver re-binds: binding its track object to a new key
//     releases the key it previously held (returned to the caller);
//   - a release only succeeds for the track object that currently owns the
//     key, so a stale or duplicate-id receiver can never remove another
//     publisher's state.
export function createReceiverRegistry() {
    const entries = new Map(); // negotiated track ID -> { track, value }
    return {
        get size() { return entries.size; },
        has(key) { return entries.has(key); },
        get(key) { return entries.get(key)?.value; },
        track(key) { return entries.get(key)?.track ?? null; },
        owns(key, track) { return !!track && entries.get(key)?.track === track; },
        keyOf(track) {
            for (const [key, entry] of entries) if (entry.track === track) return key;
            return null;
        },
        // bind makes track the receiver of key and returns the other keys the
        // same receiver track object held before (a reused MID).
        bind(key, track, value) {
            const released = [];
            for (const [other, entry] of entries) {
                if (other !== key && entry.track === track) { entries.delete(other); released.push(other); }
            }
            entries.set(key, { track, value });
            return released;
        },
        // release removes key only while track still owns it.
        release(key, track) {
            if (!track || entries.get(key)?.track !== track) return false;
            entries.delete(key);
            return true;
        },
        keys() { return entries.keys(); },
        clear() { entries.clear(); },
    };
}

// attributeReceiver records who publishes on a receiver in users (main.js
// state.trackUsers), keyed by the negotiated publisher track ID. A reused
// receiver drops the attribution it carried for its previous publisher.
export function attributeReceiver(users, owners, track, trackID, clientID, publisher) {
    for (const previous of owners.bind(trackID, track)) users.delete(previous);
    users.set(trackID, {
        client_id: clientID, unique_id: publisher?.unique_id || "", nickname: publisher?.nickname || "", track_id: trackID,
    });
}

// releaseReceiver drops the attribution only while track still owns trackID.
export function releaseReceiver(users, owners, track, trackID) {
    if (!owners.release(trackID, track)) return false;
    users.delete(trackID);
    return true;
}
