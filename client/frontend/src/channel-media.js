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

export async function startChannelMedia({ tabID, channelID, roster = [], adapter, onError = () => {} }) {
    stopChannelMedia();
    const session = factory ? factory({ tabID, channelID }) : new MediaSession({
        adapter: adapter || desktopMediaAdapter(tabID),
        onStatus: status => { if (current === session || status.state === "off") publish(status); },
        onError: error => { if (current === session) onError(error); },
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

/** The voice panel's encryption status line ("" when voice is off). */
export function mediaStatusText(status = lastStatus) {
    switch (status?.state) {
        case "keying": return t("e2ee.media.keying");
        case "encrypted": return withWarning(status, status.recipients ? t("e2ee.media.encrypted", { names: holderNames(status) }) : t("e2ee.media.alone"));
        case "partial": return withWarning(status, t("e2ee.media.partial", { failed: status.failed?.length || 0 }));
        default: return "";
    }
}

// Who holds my current keys: nickname with the verified account (principal).
function holderNames(status) {
    const names = (status.keyHolders || []).map(h => (h.nickname && h.nickname !== h.principal ? `${h.nickname} (${h.principal})` : h.principal));
    return names.length > 4 ? t("e2ee.media.more", { names: names.slice(0, 4).join(", "), count: names.length - 4 }) : names.join(", ");
}

function withWarning(status, text) {
    const mismatch = status.membershipMismatch || [];
    return mismatch.length ? `${text} · ${t("e2ee.media.mismatch", { names: mismatch.map(m => m.principal).join(", ") })}` : text;
}
