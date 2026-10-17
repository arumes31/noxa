// Chat peek for the in-game overlay: new channel and direct messages appear
// under the speakers for about six seconds (the native overlay times and fades
// them). Every line passes the same gates as a notification toast: DND and
// snooze, quiet hours, muted channels and per-channel overrides, the chat
// notification level and the notification matrix. Own, replayed, offline,
// deleted and blocked senders' messages never peek, and a body that stayed
// sealed, was refused or arrived as plaintext only ever shows its refusal text.
import { previewLine } from './dm-model.js';
import { mentionFlags } from './role-mentions.js';

export const PEEK_LINES = 3;
// Kept a little longer than the native ~6 s lifetime so the overlay, not this
// queue, decides when a line fades out.
export const PEEK_KEEP_MS = 7000;
const TEXT_MAX = 90;
const NAME_MAX = 32;

const clip = (text, limit) => {
    const chars = [...String(text ?? '').replace(/\s+/g, ' ').trim()];
    return chars.length > limit ? chars.slice(0, limit - 1).join('').trimEnd() + '…' : chars.join('');
};

// chatPeekLine decides whether a received chat payload may peek and returns
// { name, text } or null. env: { settings, selfUID, focused, replaying, dnd,
// allowed(event, ctx) } where allowed applies channel overrides and the matrix.
export function chatPeekLine(d, env) {
    const settings = env.settings || {};
    if (!d || settings.gaming_overlay === false || settings.gaming_overlay_chat_peek !== true) return null;
    if (env.focused || env.replaying || env.dnd) return null;
    const uid = d.from_unique_id || '';
    if (uid && uid === env.selfUID) return null;
    if (uid && (settings.blocked_users || []).includes(uid)) return null;
    if (d.deleted || d.offline) return null;
    const direct = !!d.direct || !!d.e2e;
    let event, ctx;
    if (direct) {
        event = 'dm'; ctx = { uid, className: 'messages' };
    } else {
        const channelID = d.channel_id !== undefined && d.channel_id !== null && d.channel_id !== '' ? Number(d.channel_id) : null;
        if (channelID === null || !Number.isFinite(channelID)) return null; // server-wide chat is not a channel message
        // The chat notification level, exactly as chat-ui.js applies it.
        const flags = mentionFlags(d, env.selfUID);
        const mentioned = flags.direct || flags.role;
        const level = settings.chat_notification_level || 'all';
        if (!(level === 'all' || (level === 'channel_mentions' && mentioned) || (level === 'role_mentions' && flags.role))) return null;
        event = mentioned ? 'mention' : 'channel_message';
        ctx = { channelID, className: event === 'mention' ? 'mentions' : 'messages' };
    }
    if (!env.allowed?.(event, ctx)) return null;
    // The same encryption-aware preview the DM list uses: refused or sealed
    // bodies become their refusal text, attachments their file name.
    const message = {
        text: d.text ?? d.body ?? '', deleted: false, self: false, direct, e2e: !!d.e2e,
        enc: !!d.enc || !!d.enc_verified,
        encMode: d.enc_verified && (d.enc_mode === 'e2e' || d.enc_mode === 'server') ? d.enc_mode : '',
    };
    const text = clip(previewLine(message), TEXT_MAX);
    const name = clip(d.from ?? d.from_nickname ?? '?', NAME_MAX);
    return text ? { name, text } : null;
}

// createChatPeek keeps the newest PEEK_LINES lines for PEEK_KEEP_MS. entries()
// is cheap and allocation-free when the queue is empty.
export function createChatPeek({ keep = PEEK_KEEP_MS, lines = PEEK_LINES } = {}) {
    let queue = [], sequence = 0;
    const EMPTY = [];
    return {
        push(line, now = Date.now()) {
            if (!line) return;
            queue.push({ id: `peek-${++sequence}`, name: line.name, text: line.text, at: now });
            if (queue.length > lines) queue = queue.slice(-lines);
        },
        entries(now = Date.now()) {
            if (!queue.length) return EMPTY;
            queue = queue.filter(entry => now - entry.at < keep);
            return queue.map(({ id, name, text }) => ({ id, name, text }));
        },
        clear() { queue = []; },
    };
}
