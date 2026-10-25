// call-connection-state.js — pure model behind the three-step call status
// (item 141): "connecting" (signaling or transport still coming up),
// "connected" (the peer transport is up but no audio flows yet) and "media"
// (audio tracks are actually sending or receiving). Voice channels and
// private calls feed it from RTCPeerConnection and track events; the UI
// renders the derived phase with text and an icon, never color alone.

export const CALL_PHASE_CONNECTING = "connecting";
export const CALL_PHASE_CONNECTED = "connected";
export const CALL_PHASE_MEDIA = "media";
export const CALL_PHASES = Object.freeze([CALL_PHASE_CONNECTING, CALL_PHASE_CONNECTED, CALL_PHASE_MEDIA]);

const TRANSPORT_STATES = Object.freeze(["new", "connecting", "connected", "failed", "closed"]);

// normalizeTransportState maps RTCPeerConnectionState and RTCIceConnectionState
// values onto the small set the model understands. "disconnected" and
// "checking" are still connecting from the user's point of view: nothing can
// flow until the transport reports connected again.
export function normalizeTransportState(value) {
    switch (value) {
    case "connected":
    case "completed":
        return "connected";
    case "failed":
        return "failed";
    case "closed":
        return "closed";
    case "new":
        return "new";
    default:
        return "connecting";
    }
}

export function initialCallConnection() {
    return { transport: "new", sending: false, receiving: false };
}

// reduceCallConnection applies one event and returns the next state. Events:
//   { type: "transport", state }            — pc.connectionState / iceConnectionState
//   { type: "media", direction, active }    — "send" | "receive" audio flow changed
//   { type: "reset" }                       — back to a fresh connection
// Unknown events return the same object so callers can compare by identity.
export function reduceCallConnection(state = initialCallConnection(), event = {}) {
    switch (event.type) {
    case "transport": {
        const transport = normalizeTransportState(event.state);
        if (transport === state.transport) return state;
        // Media cannot flow over a transport that is not up; drop stale flags
        // so a reconnect starts again at "connecting" instead of "media".
        if (transport !== "connected") return { transport, sending: false, receiving: false };
        return { ...state, transport };
    }
    case "media": {
        const active = !!event.active;
        if (event.direction === "send") return state.sending === active ? state : { ...state, sending: active };
        if (event.direction === "receive") return state.receiving === active ? state : { ...state, receiving: active };
        return state;
    }
    case "reset":
        return initialCallConnection();
    default:
        return state;
    }
}

export function callConnectionPhase(state) {
    if (!state || state.transport !== "connected") return CALL_PHASE_CONNECTING;
    return state.sending || state.receiving ? CALL_PHASE_MEDIA : CALL_PHASE_CONNECTED;
}

// aggregateCallConnections combines the per-peer states of a group call. The
// transport counts as up only when every peer is connected; media flows when
// any peer sends or receives. An empty list is still connecting.
export function aggregateCallConnections(states) {
    const list = Array.from(states || []).filter(Boolean);
    if (list.length === 0) return initialCallConnection();
    const allConnected = list.every(state => state.transport === "connected");
    let transport = "connected";
    if (!allConnected) {
        transport = list.some(state => TRANSPORT_STATES.includes(state.transport) && state.transport !== "connected" && state.transport !== "new") ? "connecting" : "new";
        if (list.every(state => state.transport === "failed")) transport = "failed";
    }
    return {
        transport,
        sending: allConnected && list.some(state => state.sending),
        receiving: allConnected && list.some(state => state.receiving),
    };
}

// describeCallConnection turns a state into what the UI shows: the phase, a
// translation key, a direction detail key (media only) and a shape-coded icon
// name, so the three steps stay distinguishable without color.
export function describeCallConnection(state) {
    const phase = callConnectionPhase(state);
    const icon = { [CALL_PHASE_CONNECTING]: "refresh", [CALL_PHASE_CONNECTED]: "phone", [CALL_PHASE_MEDIA]: "activity" }[phase];
    let detailKey = null;
    if (phase === CALL_PHASE_MEDIA) {
        if (state.sending && state.receiving) detailKey = "call.link.media.both";
        else detailKey = state.sending ? "call.link.media.sending" : "call.link.media.receiving";
    }
    return {
        phase,
        icon,
        key: `call.link.${phase}`,
        detailKey,
        sending: !!(state && state.sending),
        receiving: !!(state && state.receiving),
    };
}

// callConnectionAnnouncement returns the translation key to announce when the
// phase changed, or null when nothing worth saying happened. Direction-only
// changes inside "media" stay silent to avoid chatter while people talk.
export function callConnectionAnnouncement(previousPhase, nextPhase) {
    if (!nextPhase || previousPhase === nextPhase) return null;
    return `call.link.announce.${nextPhase}`;
}

// trackIsFlowing reports whether a MediaStreamTrack currently carries media:
// live, enabled and not muted. A remote track is muted until RTP arrives, so
// that is the browser's own "packets are flowing" signal for the receive
// side; a local sender track is gated by `enabled` (mute, deafen, PTT idle),
// which the browser never mirrors into `muted`.
export function trackIsFlowing(track) {
    return !!track && track.readyState === "live" && track.muted !== true && track.enabled !== false;
}
