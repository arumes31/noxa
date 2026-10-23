// Private calls expose candidates to another user, unlike server voice rooms.
export function privateCallRTCConfiguration(iceServers = []) {
    const relay = iceServers.filter(server => {
        const urls = Array.isArray(server.urls) ? server.urls : [server.urls];
        return urls.some(url => /^turns?:/i.test(String(url || ""))) && server.username && server.credential;
    });
    if (!relay.length) throw new Error("Private calls require an available TURN relay. Ask the server administrator to configure TURN and try again.");
    return { iceServers: relay, iceTransportPolicy: "relay" };
}
