// ice-ladder.js — ICE restart ladder for the voice peer, matching the desktop:
// restart at once when the transport failed (500 ms when it is only
// "disconnected", which often recovers by itself), then after 1, 2, 5 and
// 15 seconds. A connected state resets the ladder; a network change restarts
// a waiting ladder from its first step. An ICE restart keeps the peer and its
// media keys, so no rekey is needed.

export const ICE_FIRST_RESTART_MS = Object.freeze({ failed: 0, disconnected: 500 });
export const ICE_BACKOFF_MS = Object.freeze([1000, 2000, 5000, 15000]);

/** "failed", "disconnected", "connected" or "" for a peer's transport state. */
export function iceTransportState(pc) {
    if (!pc) return "";
    const states = [pc.connectionState, pc.iceConnectionState];
    if (states.includes("closed")) return "";
    if (states.includes("failed")) return "failed";
    if (states.includes("disconnected")) return "disconnected";
    if (pc.connectionState === "connected" || ["connected", "completed"].includes(pc.iceConnectionState)) return "connected";
    return "";
}

/**
 * restart() performs one ICE restart; state() returns iceTransportState of the
 * current peer. onExhausted fires once when every step has been tried.
 */
export function createIceRestartLadder({ restart, state, onExhausted = () => {}, timers = globalThis }) {
    let attempts = 0, timer = null, stopped = false, exhausted = false;
    // restarting: a restart is in flight; firstFor: the state a pending first
    // step was scheduled for.
    let restarting = false, firstFor = "";

    function clear() {
        if (timer !== null) timers.clearTimeout(timer);
        timer = null;
    }

    function schedule() {
        if (stopped || restarting) return;
        const current = state();
        // A failure while the "disconnected" grace step waits restarts now.
        if (timer !== null && current === "failed" && attempts === 1 && firstFor === "disconnected") {
            clear();
            attempts = 0;
        }
        if (timer !== null) return;
        if (current === "connected") { reset(); return; }
        if (current !== "failed" && current !== "disconnected") return;
        if (attempts > ICE_BACKOFF_MS.length) {
            if (!exhausted) { exhausted = true; onExhausted(); }
            return;
        }
        const delay = attempts === 0 ? ICE_FIRST_RESTART_MS[current] : ICE_BACKOFF_MS[attempts - 1];
        firstFor = attempts === 0 ? current : "";
        attempts++;
        const mine = timers.setTimeout(async () => {
            if (timer !== mine) return;
            timer = null;
            if (stopped) return;
            const now = state();
            if (now === "connected") { reset(); return; }
            if (now !== "failed" && now !== "disconnected") return;
            restarting = true;
            try { await restart(); } catch { /* the next step retries */ } finally { restarting = false; }
            // A persistent failure may not fire another state change.
            schedule();
        }, delay);
        timer = mine;
    }

    function reset() {
        clear();
        attempts = 0;
        exhausted = false;
    }

    return {
        /** Call on every connection/ICE state change of the peer. */
        update() {
            if (stopped) return;
            if (state() === "connected") reset();
            else schedule();
        },
        /** A network change: restart a waiting ladder from the first step. */
        nudge() {
            if (stopped) return;
            clear();
            attempts = 0;
            exhausted = false;
            schedule();
        },
        stop() { stopped = true; clear(); },
    };
}
