import test from 'node:test';
import assert from 'node:assert/strict';
import { captureChannelPresence, channelPresenceChanges } from '../src/channel-presence.js';

const member = (id, channel_id = 7) => ({ client_id: id, nickname: id, channel_id });
const state = (clients, extra = {}) => ({ activeTabID: 'server-a', serverGeneration: 1, sessionGeneration: 1,
    myClientID: 'self', myChannelID: 7, clients: [member('self'), ...clients], ...extra });
const changes = (before, after) => channelPresenceChanges(captureChannelPresence(before), captureChannelPresence(after));

test('live membership snapshots distinguish disconnect, channel leave and move', () => {
    const before = state([member('disconnected'), member('left'), member('moved')]);
    const after = state([member('left', 0), member('moved', 8)]);
    assert.deepEqual(changes(before, after).map(change => [change.client.client_id, change.speechEvent]),
        [['disconnected', 'user_disconnected'], ['left', 'user_leave'], ['moved', 'user_moved']]);
});

test('joins notify only in the current channel; repeated or legacy-updated snapshots stay quiet', () => {
    const before = state([member('peer', 8), member('elsewhere', 9)]);
    const after = state([member('peer'), member('elsewhere', 10)]);
    assert.deepEqual(changes(before, after).map(change => [change.client.client_id, change.soundEvent]), [['peer', 'user_move_in']]);
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
    assert.equal(changes(before, after)[0].speechEvent, 'user_moved');
    assert.equal(before.clients[1].channel_id, '7');
});
