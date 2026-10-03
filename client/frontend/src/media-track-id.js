// (70/73) Track-identity contract with the router: a publisher's media arrives
// as one track per SLOT, so camera and screen share are separate tiles and
// shared system audio is a separate source from the microphone.
//   microphone ("mic")            track id "<clientID>"
//                                  msid stream "noxa-<clientID>"
//   other slots ("cam", "screenaudio", "screen")
//                                  track id "<clientID>|<slot>"
//                                  msid stream "noxa-<clientID>|<slot>"
// The separator is "|" because an msid id is an RFC 4566 token and "/" is not
// a token character. Microphones keep the bare publisher ID, so parsing
// yields slot "" for them and a router that labels nothing still resolves.
export const SLOT_SCREEN = "screen";
export const SLOT_SCREEN_AUDIO = "screenaudio"; // main.js routes this slot's audio
const SLOT_SEP = "|";

// parseTrackID splits a media track id into publisher id and slot.
export function parseTrackID(id) {
    const s = String(id ?? "");
    const i = s.indexOf(SLOT_SEP);
    return i < 0 ? { clientID: s, slot: "" } : { clientID: s.slice(0, i), slot: s.slice(i + 1) };
}

// A receiver track keeps its original browser ID when an SDP media section is
// reused. The current remote MSID owns publisher and slot attribution.
export function remoteTrackIDs(sdp = "") {
    const result = new Map();
    let section = null;
    const finish = () => {
        if (section?.mid != null) result.set(section.mid, section.active ? section.trackID : "");
    };
    for (const line of String(sdp).split(/\r?\n/)) {
        if (line.startsWith("m=")) {
            finish();
            const [kind, port] = line.slice(2).split(/\s+/);
            section = { active: ["audio", "video"].includes(kind) && port !== "0", trackID: "" };
        } else if (section) {
            if (line.startsWith("a=mid:")) section.mid = line.slice(6);
            else if (line.startsWith("a=msid:")) section.trackID = line.slice(7).trim().split(/\s+/)[1] || "";
            else if (line === "a=inactive" || line === "a=recvonly") section.active = false;
        }
    }
    finish();
    return result;
}

export function remoteTrackID(pc, track, transceiver) {
    const ids = remoteTrackIDs(pc.remoteDescription?.sdp);
    const binding = transceiver || pc.getTransceivers?.().find(item => item.receiver.track === track);
    return ids.size ? ids.get(binding?.mid) || "" : track.id;
}
