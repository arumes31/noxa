// admin.js — moderation and administration requests of the control protocol
// (internal/netproto roles*.go, member_*.go, channel_invite.go, webhook.go,
// codec.go). No DOM: the Admin screens and the member sheet call these and
// show the server's error message when a request is refused. The server
// enforces every permission; nothing here decides what is allowed.
import { Msg } from "./frame.js";

/** Ban lengths offered by the member sheet; zero seconds is permanent. */
export const BAN_DURATIONS = Object.freeze([
    { key: "5m", seconds: 5 * 60 },
    { key: "1h", seconds: 60 * 60 },
    { key: "1d", seconds: 24 * 60 * 60 },
    { key: "permanent", seconds: 0 },
]);

/** The expiry a ban of durationSeconds gets when issued at now (ms); 0 = permanent. */
export function banExpiresAt(durationSeconds, now = Date.now()) {
    const seconds = Number(durationSeconds) || 0;
    return seconds > 0 ? now + seconds * 1000 : 0;
}

/**
 * The state of a ban-list entry (expires_at in Unix seconds, 0 = permanent):
 * {permanent, expired, remaining} with remaining in whole seconds.
 */
export function banStatus(entry, now = Date.now()) {
    const expires = Number(entry?.expires_at) || 0;
    if (!expires) return { permanent: true, expired: false, remaining: Infinity };
    const remaining = Math.max(0, Math.ceil(expires - now / 1000));
    return { permanent: false, expired: remaining === 0, remaining };
}

/** Splits seconds into the largest whole unit: {value, unit} with unit minute/hour/day. */
export function durationParts(seconds) {
    const s = Math.max(0, Math.round(Number(seconds) || 0));
    if (s >= 86400) return { value: Math.round(s / 86400), unit: "day" };
    if (s >= 3600) return { value: Math.round(s / 3600), unit: "hour" };
    return { value: Math.max(1, Math.round(s / 60)), unit: "minute" };
}

/** Largest custom emoji, server icon and channel icon the server accepts. */
export const MAX_IMAGE_BYTES = 256 * 1024;
export const EMOJI_TYPES = Object.freeze(["image/png", "image/gif", "image/webp"]);
export const ICON_TYPES = Object.freeze(["image/png", "image/jpeg", "image/gif", "image/webp"]);
/** Emoji shortcodes the server accepts (chatx.go emojiNameRe). */
export const EMOJI_NAME = /^[a-z0-9_-]{1,32}$/;

/** Turns any text into a valid emoji shortcode, or "" when nothing is left. */
export function emojiName(text) {
    return String(text || "").toLowerCase().replace(/\.[a-z0-9]+$/, "").replace(/[^a-z0-9_-]+/g, "_").replace(/^_+|_+$/g, "").slice(0, 32);
}

/** Base64 of bytes, chunked so large images do not overflow the call stack. */
export function bytesToBase64(bytes) {
    let binary = "";
    for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
    return btoa(binary);
}

/** The invitation link the app opens (#/invite/<channel>/<token>). */
export function inviteLink(origin, channelID, token) {
    return `${String(origin).replace(/\/+$/, "")}/app/#/invite/${channelID}/${token}`;
}

/** Parses an invite route argument "<channel>/<token>"; null when malformed. */
export function parseInvite(arg) {
    const match = /^([1-9][0-9]{0,15})\/([A-Za-z0-9_-]{43})$/.exec(String(arg || ""));
    return match ? { channelID: Number(match[1]), token: match[2] } : null;
}

/** Splits a stored comma list into entries, and joins entries back. */
export function splitList(value) {
    return String(value || "").split(/[,\n]/).map(s => s.trim()).filter(Boolean);
}
export function joinList(entries) {
    return [...new Set(entries.map(s => String(s).trim()).filter(Boolean))].join(",");
}

/** Parses an audit detail: structured JSON {text, before, after, channel_ids} or plain text. */
export function auditDetail(entry) {
    const raw = entry?.detail || "";
    if (raw.startsWith("{")) {
        try {
            const parsed = JSON.parse(raw);
            if (parsed && typeof parsed === "object") return { text: parsed.text || "", before: parsed.before, after: parsed.after, channels: parsed.channel_ids || [], revision: parsed.revision || 0 };
        } catch { /* plain text that happens to start with a brace */ }
    }
    return { text: raw, before: undefined, after: undefined, channels: [], revision: 0 };
}

/** Matches an audit entry against a filter {actor, action, target, from, to} (dates in ms). */
export function auditMatches(entry, filter = {}, names = new Map()) {
    const has = (value, needle) => !needle || String(value || "").toLowerCase().includes(needle.toLowerCase());
    const actor = `${entry.actor || ""} ${names.get(entry.actor) || ""}`;
    const target = `${entry.target || ""} ${names.get(entry.target) || ""}`;
    const at = (entry.created_at || 0) * 1000;
    return has(actor, filter.actor) && has(entry.action, filter.action) && has(target, filter.target) &&
        (!filter.from || at >= filter.from) && (!filter.to || at < filter.to);
}

export class Admin {
    constructor(session) {
        this.session = session;
        this.conn = session.conn;
    }

    #request(type, payload, reply, accept) {
        return this.conn.request(type, payload, reply, accept ? { accept, timeout: 15000 } : { timeout: 15000 });
    }

    // --- Member moderation -------------------------------------------------------

    /** Moves another session into a channel. */
    move(clientID, channelID) {
        return this.#request(Msg.MoveClient, { ack_requested: true, client_id: clientID, channel_id: channelID }, Msg.ClientMoved,
            r => r.client_id === clientID);
    }

    /** Removes a session from its voice channel (it stays signed in). */
    disconnect(client, reason = "") {
        return this.#request(Msg.KickClient, { ack_requested: true, client_id: client.client_id, expected_channel_id: client.channel_id, reason },
            Msg.ClientRemoved, r => r.client_id === client.client_id && !r.from_server);
    }

    /** Signs one session out of the server. */
    kick(clientID, reason = "") {
        return this.#request(Msg.KickClient, { ack_requested: true, client_id: clientID, from_server: true, reason },
            Msg.ClientRemoved, r => r.client_id === clientID && r.from_server && !r.ban);
    }

    /** Bans the account of a session; zero seconds is permanent. */
    ban(clientID, reason = "", durationSeconds = 0) {
        return this.#request(Msg.KickClient, { ack_requested: true, client_id: clientID, from_server: true, ban: true, reason, duration_seconds: durationSeconds || undefined },
            Msg.ClientRemoved, r => r.client_id === clientID && r.ban);
    }

    /** Signs every visible session of an account out; resolves with the count. */
    async kickSessions(uniqueID, reason = "") {
        const sessions = this.session.clients().filter(c => c.unique_id === uniqueID && c.client_id !== this.session.clientID);
        for (const client of sessions) await this.kick(client.client_id, reason);
        return sessions.length;
    }

    /** Server mute/deafen; only the given flags change. */
    voice(client, { muted, deafened } = {}) {
        return this.#request(Msg.MemberVoiceSet, { client_id: client.client_id, channel_id: client.channel_id, muted, deafened },
            Msg.MemberVoiceState, r => r.client_id === client.client_id);
    }

    /**
     * Files a complaint. The server sends no acknowledgement, so a Ping
     * follows it: frames are handled in order, and the Pong arrives only after
     * the complaint was stored or refused with an error.
     */
    complain(uniqueID, reason) {
        const pending = this.#request(Msg.Complaint, { target_unique_id: uniqueID, reason }, Msg.Pong);
        try { this.conn.send(Msg.Ping, {}); } catch { /* the request rejects on its own */ }
        return pending;
    }

    // --- Bans and complaints -----------------------------------------------------

    async bans() {
        return (await this.#request(Msg.BanList, {}, Msg.BanListResponse)).bans || [];
    }

    liftBan(banID) {
        return this.#request(Msg.RoleBanRemove, { ban_id: banID }, Msg.RoleBanRemoved, r => r.ban_id === banID);
    }

    async complaints() {
        return (await this.#request(Msg.ComplaintList, {}, Msg.Complaints)).entries || [];
    }

    /** Clears complaints against target (only those from fromUniqueID when given). */
    async clearComplaints(targetUniqueID, fromUniqueID = "") {
        return (await this.#request(Msg.ComplaintClear, { target_unique_id: targetUniqueID, from_unique_id: fromUniqueID || undefined }, Msg.Complaints)).entries || [];
    }

    // --- Roles ---------------------------------------------------------------------

    /** Role administration (channelID 0) or one channel's access state. */
    roleState(channelID = 0) {
        return this.#request(Msg.RoleQuery, { channel_id: channelID }, Msg.RoleState);
    }

    roleChange(change) {
        return this.#request(Msg.RoleChange, change, Msg.RoleChangeResult);
    }

    roleMembers({ channelID = 0, revision = 0, search = "", afterID = 0 } = {}) {
        return this.#request(Msg.RoleMemberQuery, { channel_id: channelID, expected_revision: revision, search, after_id: afterID }, Msg.RoleMembers);
    }

    accessCheck({ userID, channelID, capability, revision }) {
        return this.#request(Msg.AccessCheck, { user_id: userID, channel_id: channelID, capability, expected_revision: revision }, Msg.AccessCheckResult);
    }

    /** Impact of a channel_access_set draft (change) or of a tree change (tree). */
    accessPreview({ change, tree, userIDs = [0], scopeChannelID = 0 }) {
        const request = { user_ids: userIDs };
        if (tree) request.tree = tree; else request.change = change;
        if (scopeChannelID) request.scope_channel_id = scopeChannelID;
        return this.#request(Msg.ChannelAccessPreview, request, Msg.ChannelAccessImpact);
    }

    // --- Channels --------------------------------------------------------------------

    /** kind is channel_create (channelID = parent), channel_edit, channel_move or channel_delete. */
    channelState(kind, channelID) {
        return this.#request(Msg.RoleChannelQuery, { kind, channel_id: channelID }, Msg.RoleChannelState);
    }

    channelChange(request) {
        return this.#request(Msg.RoleChannelChange, request, Msg.RoleChannelResult);
    }

    /** Sets a channel icon from image bytes (base64) or copies another channel's. */
    channelIcon(channelID, { data = "", copyFrom = 0 } = {}) {
        const request = { channel_id: channelID, data_base64: data };
        if (copyFrom) request.copy_from_channel_id = copyFrom;
        return this.#request(Msg.RoleChannelIconSet, request, Msg.RoleChannelIconSaved, r => r.channel_id === channelID);
    }

    /** A channel icon as a data: URL, or "" when none is set. */
    async channelIconURL(channelID) {
        const data = await this.#request(Msg.ChannelIconGet, { channel_id: channelID }, Msg.ChannelIconData, r => r.channel_id === channelID);
        return data.data_base64 ? `data:${data.content_type || "image/png"};base64,${data.data_base64}` : "";
    }

    /** action: get, regenerate (with access guest|member) or revoke. */
    invite(action, channelID, access) {
        return this.#request(Msg.ChannelInviteRequest, { action, channel_id: channelID, access: action === "regenerate" ? access : undefined },
            Msg.ChannelInviteResult, r => r.action === action && r.channel_id === channelID);
    }

    /** Looks an invitation up; the caller then opens or joins the channel. */
    resolveInvite(channelID, token) {
        return this.#request(Msg.ChannelInviteRequest, { action: "resolve", channel_id: channelID, token },
            Msg.ChannelInviteResult, r => r.action === "resolve" && r.channel_id === channelID);
    }

    /** action: list, create (with name) or revoke (with id). */
    webhooks(action, channelID, extra = {}) {
        return this.#request(Msg.WebhookRequest, { action, channel_id: channelID, ...extra }, Msg.WebhookResult,
            r => r.action === action && r.channel_id === channelID);
    }

    // --- Server --------------------------------------------------------------------

    audit(beforeID = 0, limit = 100) {
        return this.#request(Msg.AuditLog, { before_id: beforeID || undefined, limit }, Msg.AuditLogResponse);
    }

    filters() {
        return this.#request(Msg.ChatFilterGet, {}, Msg.ChatFilterResponse);
    }

    /** patch: {word_filter, link_blacklist, link_whitelist}; omitted lists stay. */
    setFilters(patch) {
        return this.#request(Msg.ChatFilterSet, patch, Msg.ChatFilterResponse);
    }

    async emojiList() {
        return (await this.#request(Msg.EmojiList, {}, Msg.EmojiListResponse)).emojis || [];
    }

    #asset(type, payload, name = "") {
        return this.#request(type, { ack_requested: true, ...payload }, Msg.AssetMutationSaved,
            r => r.operation === type && (!name || r.name === name));
    }

    emojiUpload(name, data) { return this.#asset(Msg.EmojiUpload, { name, data_base64: data }, name); }
    emojiRename(name, newName) { return this.#asset(Msg.EmojiRename, { name, new_name: newName }, name); }
    emojiDelete(name) { return this.#asset(Msg.EmojiDelete, { name }, name); }
    setServerIcon(data) { return this.#asset(Msg.ServerIconSet, { data_base64: data }); }
    setServerBanner(data) { return this.#asset(Msg.ServerBannerSet, { data_base64: data }); }

    async serverIconURL() {
        const data = await this.#request(Msg.ServerIconGet, {}, Msg.ServerIconData);
        return data.data_base64 ? `data:${data.content_type || "image/png"};base64,${data.data_base64}` : "";
    }

    async serverBannerURL() {
        const data = await this.#request(Msg.ServerBannerGet, {}, Msg.ServerBannerData);
        return data.data_base64 ? `data:${data.content_type || "image/png"};base64,${data.data_base64}` : "";
    }

    serverConfig() {
        return this.#request(Msg.ServerConfigQuery, {}, Msg.ServerConfigResponse);
    }

    /** All six settings are required: max_clients, client_timeout_seconds, opus_*. */
    setServerConfig(config) {
        return this.#request(Msg.ServerConfigSet, config, Msg.ServerConfigResponse);
    }

    /** All three fields are required; zero is unlimited. */
    setMediaLimits({ video_max_bitrate, video_max_width, video_max_height }) {
        return this.#request(Msg.MediaLimitsSet, { video_max_bitrate, video_max_width, video_max_height }, Msg.MediaLimitsSaved);
    }
}

const admins = new WeakMap();

/** The Admin of a session, created once. */
export function adminFor(session) {
    if (!admins.has(session)) admins.set(session, new Admin(session));
    return admins.get(session);
}

/**
 * Whether this session may administer roles: owners and administrators do;
 * others when the role query succeeds. Cached per session.
 */
const roleAccess = new WeakMap();
export function canManageRoles(session) {
    if (session.isAdmin) return Promise.resolve(true);
    if (!roleAccess.has(session)) roleAccess.set(session, adminFor(session).roleState(0).then(() => true, () => false));
    return roleAccess.get(session);
}
