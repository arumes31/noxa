// Local channel-join intents.
//
// A server-reported self move is not evidence that this user asked to join a
// channel: a malicious or buggy server can emit one at any time. Deafen is a
// local privacy control, so it may only be cleared by a join the user started
// here. Explicit join actions record an intent; the user_moved handler
// consumes a matching, unexpired intent before it clears deafen.

const INTENT_TTL_MS = 30_000;
const intents = new Map(); // tabID -> { channelID, at }

/** Records that the user explicitly asked to join channelID on tabID. 0 asks for the server's default (home) channel. */
export function noteLocalChannelJoin(tabID, channelID, now = Date.now()) {
    if (!tabID) return null;
    const intent = { channelID: Number(channelID) || 0, at: now };
    intents.set(tabID, intent);
    return intent;
}

/**
 * Forgets a recorded intent, e.g. after the join request failed. With an
 * intent token only that intent is removed, so an older failed join cannot
 * delete the intent of a newer join on the same tab.
 */
export function forgetLocalChannelJoin(tabID, intent = null) {
    if (!intent || intents.get(tabID) === intent) intents.delete(tabID);
}

/**
 * A self move into channel 0 (home/no channel) completes a default-channel
 * request: retire it so it cannot authorize a later server-initiated move.
 */
export function settleLocalChannelJoinAt(tabID, channelID) {
    if ((Number(channelID) || 0) === 0 && intents.get(tabID)?.channelID === 0) intents.delete(tabID);
}

/**
 * Consumes the intent for tabID when it authorizes a self move into
 * channelID. A default-channel request (0) matches whichever channel the
 * server places the user in.
 */
export function consumeLocalChannelJoin(tabID, channelID, now = Date.now()) {
    const intent = intents.get(tabID);
    if (!intent) return false;
    if (now - intent.at > INTENT_TTL_MS) {
        intents.delete(tabID);
        return false;
    }
    const target = Number(channelID) || 0;
    if (intent.channelID !== 0 && intent.channelID !== target) return false;
    intents.delete(tabID);
    return true;
}

/** Records the intent, then performs the native join. */
export async function joinChannelLocally(tabID, channelID) {
    const intent = noteLocalChannelJoin(tabID, channelID);
    try {
        const error = await window.go.main.App.JoinChannelForTab(tabID, channelID);
        if (error) forgetLocalChannelJoin(tabID, intent);
        return error;
    } catch (error) {
        forgetLocalChannelJoin(tabID, intent);
        throw error;
    }
}
