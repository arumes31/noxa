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

