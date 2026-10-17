import test from 'node:test';
import assert from 'node:assert/strict';
import { PEEK_KEEP_MS, chatPeekLine, createChatPeek } from '../src/gaming-overlay-peek.js';

const settings = (extra = {}) => ({ gaming_overlay_chat_peek: true, chat_notification_level: 'all', blocked_users: ['blocked'], ...extra });
const env = (extra = {}) => ({ settings: settings(), selfUID: 'me', focused: false, replaying: false, dnd: false, allowed: () => true, ...extra });
const channel = (extra = {}) => ({ from: 'Sam', from_unique_id: 'sam', channel_id: 4, text: 'gg, one more round?', enc_verified: true, enc_mode: 'server', ...extra });
const dm = (extra = {}) => ({ from: 'Alex', from_unique_id: 'alex', direct: true, e2e: true, text: 'see you at 8', enc_verified: true, enc_mode: 'e2e', ...extra });

test('verified channel and direct messages peek with the sender name', () => {
    assert.deepEqual(chatPeekLine(channel(), env()), { name: 'Sam', text: 'gg, one more round?' });
    assert.deepEqual(chatPeekLine(dm(), env()), { name: 'Alex', text: 'see you at 8' });
});

test('chat peek is opt-in and off with the overlay', () => {
    assert.equal(chatPeekLine(channel(), env({ settings: {} })), null);
    assert.equal(chatPeekLine(channel(), env({ settings: settings({ gaming_overlay: false }) })), null);
});

test('refused, sealed and plaintext bodies never show their content', () => {
    for (const [message, refusal] of [
        [channel({ enc_verified: false, enc_mode: '', text: 'SECRET plaintext from server' }), /plaintext/i],
        [dm({ text: '[encrypted message — key unavailable]' }), /key/i],
        [dm({ text: '[refused: server sent plaintext history]' }), /plaintext/i],
        [dm({ text: '[encrypted message — decryption failed]' }), /decrypt|fail/i],
    ]) {
        const line = chatPeekLine(message, env());
        assert.ok(line, 'refusal still peeks as its placeholder');
        assert.doesNotMatch(line.text, /SECRET|\[encrypted|\[refused/);
        assert.match(line.text, refusal);
    }
});

test('muted channels, matrix, DND, blocked users and own messages are never shown', () => {
    const calls = [];
    const muted = env({ allowed: (event, ctx) => { calls.push([event, ctx]); return ctx.channelID !== 4; } });
    assert.equal(chatPeekLine(channel(), muted), null);
    assert.deepEqual(calls[0], ['channel_message', { channelID: 4, className: 'messages' }]);
    assert.equal(chatPeekLine(dm(), env({ allowed: () => false })), null, 'matrix or override refuses');
    assert.equal(chatPeekLine(channel(), env({ dnd: true })), null, 'do not disturb / snooze');
    assert.equal(chatPeekLine(channel({ from_unique_id: 'blocked' }), env()), null, 'blocked sender');
    assert.equal(chatPeekLine(dm({ from_unique_id: 'blocked' }), env()), null, 'blocked DM sender');
    assert.equal(chatPeekLine(channel({ from_unique_id: 'me' }), env()), null, 'own message');
    assert.equal(chatPeekLine(channel(), env({ replaying: true })), null, 'history replay');
    assert.equal(chatPeekLine(channel(), env({ focused: true })), null, 'noXa itself has focus');
    assert.equal(chatPeekLine(dm({ offline: true }), env()), null, 'offline spool');
    assert.equal(chatPeekLine(channel({ deleted: true }), env()), null, 'deleted');
    assert.equal(chatPeekLine(channel({ channel_id: undefined }), env()), null, 'server-wide chat');
});

test('the chat notification level gates channel lines like toasts', () => {
    const level = value => env({ settings: settings({ chat_notification_level: value }) });
    assert.equal(chatPeekLine(channel(), level('direct')), null);
    assert.equal(chatPeekLine(channel(), level('channel_mentions')), null);
    let seen;
    const capture = value => env({ settings: settings({ chat_notification_level: value }), allowed: (event, ctx) => { seen = [event, ctx.className]; return true; } });
    assert.ok(chatPeekLine(channel({ mentions: ['me'] }), capture('channel_mentions')));
    assert.deepEqual(seen, ['mention', 'mentions']);
    assert.equal(chatPeekLine(channel({ mentions: ['me'] }), level('role_mentions')), null);
    assert.ok(chatPeekLine(channel({ role_mentions: ['me'] }), level('role_mentions')));
    assert.ok(chatPeekLine(dm(), level('direct')), 'DMs follow the dm matrix row');
});

test('long text and names are truncated, attachments show their name only', () => {
    const line = chatPeekLine(channel({ from: 'N'.repeat(60), text: 'word '.repeat(60) }), env());
    assert.ok([...line.text].length <= 90 && line.text.endsWith('…'));
    assert.ok([...line.name].length <= 32 && line.name.endsWith('…'));
    const file = chatPeekLine(dm({ text: '[file:abc123#4096#clip.png]' }), env());
    assert.doesNotMatch(file.text, /abc123/);
});

test('the queue keeps the newest three lines for about seven seconds', () => {
    const queue = createChatPeek();
    assert.equal(queue.entries(0).length, 0);
    for (let i = 1; i <= 4; i++) queue.push({ name: `S${i}`, text: `m${i}` }, i * 100);
    queue.push(null, 500);
    const lines = queue.entries(500);
    assert.deepEqual(lines.map(line => line.text), ['m2', 'm3', 'm4']);
    assert.equal(new Set(lines.map(line => line.id)).size, 3);
    assert.deepEqual(queue.entries(200 + PEEK_KEEP_MS).map(line => line.text), ['m3', 'm4']);
    assert.deepEqual(queue.entries(400 + PEEK_KEEP_MS), []);
});
