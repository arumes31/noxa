// Desktop glue for encrypted channel media (Task 2). The frame transform and
// the key epochs are the shared MediaSession (media-session.js); this module
// supplies the native bridge (media keys travel as authenticated ratchet
// controls through App.SendMediaKeyForTab / the "media_key" event) and owns
// the one session of the current voice peer.
import { MediaSession, MediaSessionError, MEDIA_E2EE_VERSION, mediaErrorReason } from "./media-session.js";
import { t } from "./i18n.js";

export { MEDIA_E2EE_VERSION, MediaSessionError, mediaErrorReason };

const SECURE_WAIT_MS = 15000;
let current = null;
let listeners = new Set();
let lastStatus = { state: "off" };

// The secure session starts right after sign-in; a voice join waits briefly
// for it instead of failing on the first try.
export function desktopMediaAdapter(tabID, { app = () => window.go?.main?.App, wait = SECURE_WAIT_MS } = {}) {
    return {
        connection: tabID, // one tab is one server connection, kept across its reconnects
        async status() {
            const deadline = Date.now() + wait;
            for (;;) {
                const status = await app().MediaE2EEStatusForTab(tabID);
                const retry = status?.supported && (status.reason === "secure_unavailable" || status.reason === "no_device");
                if (!retry || Date.now() >= deadline) {
                    return { supported: !!status?.supported, principal: status?.principal || "", device: status?.device || "", clientID: status?.client_id || "", reason: status?.reason || "" };
                }
                await new Promise(resolve => setTimeout(resolve, 250));
            }
        },
        async send({ logicalID, principal, device, body }) {
            const result = await app().SendMediaKeyForTab(tabID, { logical_id: logicalID, principal, device, kind: body.kind, scope: body.scope,
                epoch: body.epoch, expires_at: body.expires_at, data: body.data });
            return { ok: result?.state === "accepted", code: result?.code || result?.state || "unavailable" };
        },
    };
}

// Starts the session for a voice peer about to be created. Throws a
// MediaSessionError with an explicit reason; the caller must not create a
// peer then (there is no plaintext mode).
let factory = null;
// Replaces session construction (UI tests whose peers are stand-in
// objects without encoded transforms). Production never sets it.
export function useChannelMediaFactory(fn) { factory = fn || null; }

// Voice key state for the client log, written when it changes: the state,
// counts and failure codes only (never keys, account or device IDs).
let loggedStatus = "";
function logStatus(tabID, status, ignored = {}) {
    const failed = {};
    for (const f of status.failed || []) failed[f.reason] = (failed[f.reason] || 0) + 1;
    const diag = { state: String(status.state || ""), recipients: status.recipients | 0, delivered: status.delivered | 0, waiting: status.waiting | 0, publishers: status.publishers | 0, failed, ignored: { ...ignored } };
    // Counts change with every rekey: only a new state or failure code is news.
    const key = JSON.stringify([diag.state, Object.keys(failed).sort(), Object.keys(ignored).sort()]);
    if (key === loggedStatus) return;
    loggedStatus = key;
    try { void window.go?.main?.App?.LogMediaKeyStatusForTab?.(tabID, diag)?.catch?.(() => {}); } catch { /* logging never breaks voice */ }
}

export async function startChannelMedia({ tabID, channelID, roster = [], adapter, hardwareVideoRequired = true, onError = () => {} }) {
    stopChannelMedia();
    const session = factory ? factory({ tabID, channelID }) : new MediaSession({
        adapter: adapter || desktopMediaAdapter(tabID),
        owner: tabID, // native capture mirrors this session's screen keys into this tab
        hardwareVideoRequired,
        onHardwareUnavailable: error => window.__noxa?.toast?.(t(error?.message === "hardware_unverified" ? "e2ee.reason.hardware_unverified" : "e2ee.reason.hardware_encoder"), "warn"),
        onStatus: status => { if (current === session || status.state === "off") { publish(status); logStatus(tabID, status, session.engine?.counters?.ignored); } },
        onError: error => { if (current === session) onError(error); },
        // Encoded-streams receivers ask the SFU for a keyframe after their
        // first authenticated frame (best effort; the server checks the
        // requester receives that publication).
        onKeyframeNeeded: ({ publisher, slot }) => {
            if (current !== session) return;
            void window.go?.main?.App?.VideoStreamControlForTab?.(tabID, { action: "keyframe", publisher_id: publisher, slot, generation: "0", revision: "0", session: "0", active: true })?.catch?.(() => {});
        },
    });
    current = session;
    session.setRoster(roster);
    try {
        await session.start(channelID);
    } catch (error) {
        if (current === session) current = null;
        session.stop();
        throw error;
    }
    if (current !== session) { session.stop(); throw new MediaSessionError("closed"); }
    session.tabID = tabID;
    return session;
}


export function channelMedia() { return current?.active ? current : null; }

// Installs an already started session object (harnesses and UI tests that
// drive negotiation with stand-in peers). Production uses startChannelMedia.
export function useChannelMediaSession(session) {
    if (current && current !== session) current.stop();
    current = session || null;
}

export function stopChannelMedia() {
    const session = current;
    current = null;
    session?.stop();
    publish({ state: "off" });
}

// A delivered media-key control from the native bridge.
export function receiveMediaKey(event) {
    if (!current || !event) return { ok: false, reason: "inactive" };
    return current.receive({ sender: event.sender, senderDevice: event.sender_device, kind: event.kind, scope: event.scope,
        epoch: event.epoch, expiresAt: event.expires_at, data: event.data });
}

function publish(status) {
    lastStatus = status;
    for (const fn of listeners) { try { fn(status); } catch { /* status listeners never break media */ } }
}
export function onChannelMediaStatus(fn) { listeners.add(fn); fn(lastStatus); return () => listeners.delete(fn); }
export function channelMediaStatus() { return lastStatus; }

/** Localized text for an encrypted-media failure, or the error's own message. */
export function mediaErrorText(error) {
    const reason = mediaErrorReason(error);
    return reason ? t(`e2ee.reason.${reason}`) : String(error?.message || error || "");
}
