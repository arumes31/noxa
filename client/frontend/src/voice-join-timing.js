// Timing marks of the latest voice join, in ms since it started. They show
// where a slow join spends its time (microphone, key exchange, transport)
// instead of guessing. Read with window.__noxa.voiceJoinTiming().
let run = null;

export function beginVoiceJoin(now = performance.now()) { run = { at: now, marks: {}, reported: false }; }

/** Records the first occurrence of a mark; later ones are ignored. */
export function markVoiceJoin(name, now = performance.now()) {
    if (run && !(name in run.marks)) run.marks[name] = Math.round(now - run.at);
}

/** Marks derived from the key engine's status (channel-media status events). */
export function markVoiceJoinKeys(status, now = performance.now()) {
    if (!run || !status || status.state === "off") return;
    // Key marks end 30 s after the transport connected: a later one belongs
    // to somebody else joining, not to this join.
    if (now - run.at - run.marks.transport_connected > 30_000) return;
    // A join that connected with nobody else there is finished: keys for a
    // participant who arrives later are not part of it.
    const peers = Math.max(status.recipients || 0, (status.participants || []).length);
    if (!("transport_connected" in run.marks) || run.peers === undefined) run.peers = peers;
    else if (!run.peers) return;
    if (status.epoch) markVoiceJoin("own_key_active", now);
    if (status.delivered > 0) markVoiceJoin("own_key_first_delivered", now);
    if (status.recipients > 0 && status.delivered >= status.recipients) markVoiceJoin("own_key_all_delivered", now);
    const others = status.participants || [];
    if (others.some(p => p.received)) markVoiceJoin("first_key_received", now);
    if (others.length > 0 && others.every(p => p.received)) markVoiceJoin("all_keys_received", now);
}

export function voiceJoinTiming() { return run ? { ...run.marks } : null; }

/**
 * "name ms · name ms" in time order, once per join and only when the join is
 * finished (transport connected) and took at least slowMS; otherwise "".
 */
export function slowVoiceJoinSummary(slowMS = 3000) {
    if (!run || run.reported || !("transport_connected" in run.marks)) return "";
    const marks = Object.entries(run.marks).sort((a, b) => a[1] - b[1]);
    if (marks.at(-1)[1] < slowMS) return "";
    run.reported = true;
    return marks.map(([name, ms]) => `${name} ${ms}`).join(" · ");
}
