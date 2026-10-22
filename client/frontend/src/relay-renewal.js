// Credential delivery, ICE negotiation and selected-path migration are separate
// steps. Keep the existing peer/tracks alive while a replacement is prepared.
export function maintainRelay(pc, { app, tabID, current, restart, onFailure = () => {} }) {
    let stopped = false, running = false, generation = 0, attempts = 0, retryAt = 0, oldRelay = null, needsRestart = false;
    const live = () => !stopped && current() && pc.signalingState !== "closed";
    const selected = async () => {
        const stats = await pc.getStats();
        const transport = [...stats.values()].find(item => item.type === "transport" && item.selectedCandidatePairId);
        const pair = transport && stats.get(transport.selectedCandidatePairId);
        const local = pair && stats.get(pair.localCandidateId);
        if (!local) return undefined;
        return local.candidateType === "relay" ? `${local.address || local.ip}:${local.port}` : null;
    };
    async function tick() {
        if (!live()) { stop(); return; }
        if (running || !app.GetRelayLeaseForTab || Date.now() < retryAt) return;
        running = true;
        try {
            const lease = await app.GetRelayLeaseForTab(tabID);
            if (!live() || !lease?.generation) return;
            if (lease.generation < generation) return;
            if (lease.generation !== generation) {
                const iceServers = await app.GetICEServersForTab(tabID);
                if (!live()) return;
                oldRelay = pc.remoteDescription ? await selected() : null;
                if (!live()) return;
                pc.setConfiguration({ ...pc.getConfiguration(), iceServers });
                generation = lease.generation; attempts = 0; retryAt = 0;
                needsRestart = !!pc.remoteDescription;
                if (needsRestart) { await restart(); needsRestart = false; }
                // A setConfiguration call does not prove migration. For an
                // existing relay path, wait for a different selected endpoint.
            } else if ((oldRelay || needsRestart) && pc.remoteDescription) {
                const nowRelay = await selected();
                if (!live()) return;
                if (!needsRestart && nowRelay !== undefined && nowRelay !== oldRelay && ["connected", "completed"].includes(pc.iceConnectionState)) {
                    oldRelay = null; attempts = 0;
                } else if (attempts < 4) {
                    attempts++; retryAt = Date.now() + [1000, 5000, 15000, 30000][attempts - 1];
                    await restart(); needsRestart = false;
                } else onFailure();
            }
        } catch {
            // Native cache and server clock enforce the finite credential
            // deadline. Recheck after wake/network recovery without stale writes.
            retryAt = Date.now() + 15000;
            if (generation > 0) onFailure();
        } finally { running = false; }
    }
    const timer = setInterval(() => { void tick(); }, 15000);
    const wake = () => { retryAt = 0; void tick(); };
    const changed = () => { if (!live()) stop(); };
    pc.addEventListener("connectionstatechange", changed);
    window.addEventListener("online", wake);
    document.addEventListener("visibilitychange", wake);
    function stop() {
        if (stopped) return;
        stopped = true; clearInterval(timer);
        pc.removeEventListener("connectionstatechange", changed);
        window.removeEventListener("online", wake);
        document.removeEventListener("visibilitychange", wake);
    }
    void tick();
    return stop;
}
