// invite.js — channel invitation links (internal/netproto/channel_invite.go).
// The desktop shares noxa://<host>/invite/<channel>#<token>; the web form is
// https://<host>/app/invite/<channel>#<token> (or /invite/<channel>). The token
// stays in the fragment, which browsers never send to the server. An
// invitation only locates a channel: the server still checks access and the
// channel password when joining.
import { Msg } from "./frame.js";

const TOKEN = /^[A-Za-z0-9_-]{43}$/;
const PATH = /^\/(?:app\/)?invite\/([1-9][0-9]{0,15})\/?$/;
const PENDING = "noxa.pendingInvite";

/**
 * Parses an invitation link. Returns {host, hostname, channelID, token, web}
 * or null. web is true for the https form (host is then a web host).
 */
export function parseInviteLink(text) {
    let url;
    try { url = new URL(String(text || "").trim()); } catch { return null; }
    const web = url.protocol === "https:" || url.protocol === "http:";
    if (!web && url.protocol !== "noxa:") return null;
    if (!url.hostname || url.username || url.password || url.search) return null;
    // noxa: URLs are not special, so their path is not normalized.
    const match = PATH.exec(url.pathname);
    if (!match || (!web && url.pathname.startsWith("/app/"))) return null;
    const channelID = Number(match[1]);
    const token = url.hash.slice(1);
    if (!Number.isSafeInteger(channelID) || !TOKEN.test(token)) return null;
    return { host: url.host, hostname: url.hostname, channelID, token, web };
}

/** The invitation carried by the page's own URL (path and fragment), or null. */
export function inviteFromLocation(location = globalThis.location) {
    if (!location || !PATH.test(location.pathname)) return null;
    const invite = parseInviteLink(`${location.origin}${location.pathname}${location.hash}`);
    return invite ? { ...invite, sameServer: true } : null;
}

/**
 * Whether an invitation is for the server this app runs on: an https link
 * to this origin, or a noxa:// link whose host name is this page's host.
 */
export function isForThisServer(invite, location = globalThis.location) {
    if (!invite) return false;
    if (invite.sameServer) return true;
    if (invite.web) return invite.host === location.host;
    return invite.hostname === location.hostname;
}

/** The web address that opens an invitation in the app of its server. */
export function webInviteURL(invite) {
    const host = invite.web ? invite.host : invite.hostname;
    return `https://${host}/app/invite/${invite.channelID}#${invite.token}`;
}

/** Keeps an invitation across sign-in (this tab only). */
export function setPendingInvite(invite, store = globalThis.sessionStorage) {
    try {
        if (invite) store?.setItem(PENDING, JSON.stringify({ channelID: invite.channelID, token: invite.token }));
        else store?.removeItem(PENDING);
    } catch { /* storage blocked */ }
}

export function pendingInvite(store = globalThis.sessionStorage) {
    try {
        const invite = JSON.parse(store?.getItem(PENDING) || "null");
        return invite && Number.isSafeInteger(invite.channelID) && invite.channelID > 0 && TOKEN.test(invite.token) ? invite : null;
    } catch {
        return null;
    }
}

/**
 * Checks the invitation with the server. Resolves with the result
 * ({channel_id, access, active}) or rejects with the server's refusal.
 */
export async function resolveInvite(conn, { channelID, token }) {
    const result = await conn.request(Msg.ChannelInviteRequest, { action: "resolve", channel_id: channelID, token },
        Msg.ChannelInviteResult, { accept: r => r.action === "resolve" && r.channel_id === channelID });
    if (!result.active) throw new Error("invitation inactive");
    return result;
}
