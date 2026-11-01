// Readiness is scoped to the local voice channel and the active key epoch.
// "ready" means local keys are installed and our control was admitted;
// it is not an acknowledgement that the remote listener has played audio.
export function memberMediaState({ self = false, connected, failed = false, status, clientID }) {
    if (failed) return "unavailable";
    if (!connected) return "connecting";
    if (status?.state === "paused") return "paused";
    if (self) return status?.epoch > 0 ? "ready" : "keying";
    const peer = status?.participants?.find(p => p.clientID === String(clientID));
    if (peer?.failed) return "retrying";
    return peer?.sent && peer?.received ? "ready" : "keying";
}
