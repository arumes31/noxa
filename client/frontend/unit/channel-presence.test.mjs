import test from 'node:test';
import assert from 'node:assert/strict';
import { captureChannelPresence, channelPresenceChanges } from '../src/channel-presence.js';

const member = (id, channel_id = 7) => ({ client_id: id, nickname: id, channel_id });
const state = (clients, extra = {}) => ({ activeTabID: 'server-a', serverGeneration: 1, sessionGeneration: 1,
    myClientID: 'self', myChannelID: 7, clients: [member('self'), ...clients], ...extra });
const changes = (before, after) => channelPresenceChanges(captureChannelPresence(before), captureChannelPresence(after));

test('live membership snapshots use neutral departure speech without inferring a disconnect', () => {
    const before = state([member('disconnected'), member('left'), member('moved')]);
    const after = state([member('left', 0), member('moved', 8)]);
    assert.deepEqual(changes(before, after).map(change => [change.client.client_id, change.speechEvent]),
        [['disconnected', 'user_leave'], ['left', 'user_leave'], ['moved', 'user_leave']]);
});

test('joins notify only in the current channel; repeated or legacy-updated snapshots stay quiet', () => {
    const before = state([member('peer', 8), member('elsewhere', 9)]);
    const after = state([member('peer'), member('elsewhere', 10)]);
    assert.deepEqual(changes(before, after).map(change => [change.client.client_id, change.soundEvent]), [['peer', 'user_join']]);
    assert.deepEqual(changes(after, after), []);
    assert.deepEqual(changes(state([]), state([member('new')])).map(change => change.soundEvent), ['user_join']);
    assert.deepEqual(changes(state([member('elsewhere', 9)]), state([])), []);
});

test('initial snapshots, replay, reconnect, tab switches and own channel changes stay quiet', () => {
    const before = state([member('peer')]);
    assert.deepEqual(channelPresenceChanges(null, captureChannelPresence(before)), []);
    for (const extra of [{ activeTabID: 'server-b' }, { serverGeneration: 2 }, { sessionGeneration: 2 },
        { myClientID: 'new-self' }, { myChannelID: 8 }, { myChannelID: 0 }, { replayingTabID: 'server-a' }]) {
        assert.deepEqual(changes(before, state([], extra)), []);
    }
    assert.deepEqual(changes({ ...before, replayingTabID: 'server-a' }, state([])), []);
    assert.deepEqual(changes(before, state([], { clients: [] })), []);
});

test('membership comparison normalizes numeric channel IDs without mutating inputs', () => {
    const before = state([member('peer', '7')]);
    const after = state([member('peer', '8')]);
    assert.equal(changes(before, after)[0].speechEvent, 'user_leave');
    assert.equal(before.clients[1].channel_id, '7');
});


test('arrival uses the committed new token and departure retains the shared previous token', () => {
    const before = state([{ ...member('leaving'), presence_token: 'old-visit' }, { ...member('arriving', 8), presence_token: 'other-visit' }]);
    const after = state([{ ...member('leaving', 8), presence_token: 'next-visit' }, { ...member('arriving'), presence_token: 'new-visit' }]);
    const events = changes(before, after);
    assert.equal(events.find(change => change.client.client_id === 'leaving').client.presence_token, 'old-visit');
    assert.equal(events.find(change => change.client.client_id === 'arriving').client.presence_token, 'new-visit');
});


test('different destination visibility cannot change the spoken departure or its shared token', async () => {
    const { presenceSpeechKey, variedSpeechClip } = await import('../src/speech-variation.js');
    const before = state([{ ...member('peer'), presence_token: 'shared-visit' }]);
    const visible = changes(before, state([{ ...member('peer', 8), presence_token: 'next-visit' }]))[0];
    const restricted = changes(before, state([]))[0];
    assert.equal(visible.speechEvent, 'user_leave');
    assert.equal(restricted.speechEvent, 'user_leave');
    assert.equal(visible.soundEvent, restricted.soundEvent, 'identical preference gates regardless of destination visibility');
    assert.notEqual(visible.message, restricted.message, 'authorized local text can retain destination knowledge');
    const clip = change => variedSpeechClip(change.speechEvent, presenceSpeechKey(change.client, 7));
    assert.equal(clip(visible), clip(restricted));
});


test('arrival audio policy is identical whether the previous channel was visible or private', () => {
    const after = state([{ ...member('peer'), presence_token: 'new-visit' }]);
    const known = changes(state([member('peer', 8)]), after)[0];
    const unknown = changes(state([]), after)[0];
    assert.equal(known.soundEvent, 'user_join');
    assert.equal(unknown.soundEvent, 'user_join');
    assert.equal(known.speechEvent, unknown.speechEvent);
    assert.equal(known.client.presence_token, unknown.client.presence_token);
});

test('a lost connection and a reconnect use their own cues; a replaced session stays silent', () => {
    const before = state([member('dropped'), member('replaced'), member('left')]);
    const after = state([member('back')], {
        presenceDepartures: { dropped: 'connection_lost', replaced: 'replaced' },
        presenceReconnected: new Set(['back']),
    });
    assert.deepEqual(changes(before, after).map(change => [change.client.client_id, change.message, change.speechEvent]), [
        ['dropped', 'runtime.connectionLostUser', 'user_connection_lost'],
        ['left', 'runtime.left', 'user_leave'],
        ['back', 'runtime.reconnectedUser', 'user_reconnected'],
    ]);
});

test('after the own reconnect only the server announcement plays while the roster refills', async () => {
    const { notePresenceDrop, notePresenceRestored, presenceCueSuppressed, RECONNECT_SETTLE_MS, RECONNECT_KNOWN_MS } = await import('../src/channel-presence.js');
    const s = state([{ ...member('peer'), unique_id: 'peer-uid' }, { ...member('other', 9), unique_id: 'other-uid' }]);
    notePresenceDrop(s);
    assert.deepEqual([...s.presenceBeforeDrop], ['peer-uid'], 'only members of the own channel are remembered');
    notePresenceRestored(s, 1000);
    const join = uid => ({ kind: 'join', client: { unique_id: uid } });
    assert.equal(presenceCueSuppressed(s, join('stranger'), 1000 + RECONNECT_SETTLE_MS - 1), true, 'the settle window is quiet');
    assert.equal(presenceCueSuppressed(s, join('peer-uid'), 1000 + RECONNECT_SETTLE_MS + 1), true, 'a member from before the drop reappears quietly');
    assert.equal(presenceCueSuppressed(s, join('peer-uid'), 1000 + RECONNECT_SETTLE_MS + 2), false, 'only once');
    assert.equal(presenceCueSuppressed(s, join('stranger'), 1000 + RECONNECT_SETTLE_MS + 1), false, 'a genuine join after the window announces');
    assert.equal(presenceCueSuppressed(s, { kind: 'leave', client: { unique_id: 'peer-uid' } }, 1000 + RECONNECT_KNOWN_MS - 1), false, 'leaves after the window announce');
    notePresenceDrop(s);
    s.presenceBeforeDrop.add('early-uid');
    notePresenceRestored(s, 3000);
    assert.equal(presenceCueSuppressed(s, join('early-uid'), 3001), true, 'a return inside the settle window is quiet');
    assert.equal(presenceCueSuppressed(s, join('early-uid'), 3000 + RECONNECT_SETTLE_MS + 1), false, 'and consumed, so a later rejoin announces');
    notePresenceDrop(s);
    s.presenceBeforeDrop.add('late-uid');
    notePresenceRestored(s, 5000);
    assert.equal(presenceCueSuppressed(s, join('late-uid'), 5000 + RECONNECT_KNOWN_MS + 1), false, 'the known set expires');
});
