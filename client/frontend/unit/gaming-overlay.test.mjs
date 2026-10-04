import test from 'node:test';
import assert from 'node:assert/strict';
import { overlayVoiceState } from '../src/gaming-overlay-state.js';

const state = () => ({ settings: {}, channels: [{ ChannelID: 1, Name: 'Lobby' }], myChannelID: 1,
    myClientID: 'self', pc: {}, clients: [
        { client_id: 'self', unique_id: 'me', channel_id: 1, nickname: 'Me', is_speaking: true },
        { client_id: 'peer', unique_id: 'other', channel_id: 1, nickname: 'Peer', is_speaking: true },
        { client_id: 'silent', channel_id: 1, nickname: 'Quiet' },
        { client_id: 'elsewhere', channel_id: 2, nickname: 'Elsewhere', is_speaking: true },
    ] });

test('only current-channel talkers appear with separate avatar identities', () => {
    const snapshot = overlayVoiceState(state());
    assert.equal(snapshot.active, true);
    assert.deepEqual(snapshot.speakers.map(s => [s.id, s.name]), [['me', 'Me'], ['other', 'Peer']]);
});

test('silence hides immediately, including joining or reconnecting', () => {
    const s = state();
    s.clients.forEach(client => { client.is_speaking = false; });
    for (const sessionGeneration of [1, 2]) assert.deepEqual(overlayVoiceState({ ...s, sessionGeneration }), { active: false });
});

test('self mute or deafen hides all speakers until both are cleared', () => {
    const s = state();
    for (const privacy of [{ muted: true, deafened: false }, { muted: false, deafened: true }, { muted: true, deafened: true }]) {
        Object.assign(s, privacy);
        assert.deepEqual(overlayVoiceState(s), { active: false });
    }
    Object.assign(s, { muted: false, deafened: false });
    assert.deepEqual(overlayVoiceState(s).speakers.map(s => s.name), ['Me', 'Peer']);
    s.clients.forEach(client => { client.is_speaking = false; });
    assert.deepEqual(overlayVoiceState(s), { active: false });
});

test('global and private-call mute or deafen hide the whole private-call overlay', () => {
    const call = { active: true, label: 'Private call', speakers: [{ id: 'alice', name: 'Alice', speaking: true }] };
    for (const flag of ['muted', 'deafened']) {
        assert.deepEqual(overlayVoiceState({ ...state(), [flag]: true }, call), { active: false });
        assert.deepEqual(overlayVoiceState(state(), { ...call, [flag]: true }), { active: false });
        assert.deepEqual(overlayVoiceState(state(), { ...call, [flag]: false }).speakers, call.speakers);
        assert.equal(overlayVoiceState(state(), { ...call, active: false, [flag]: true }).title, 'Lobby');
    }
});

test('muted peers never appear even with a stale speaking flag', () => {
    for (const flag of ['input_muted', 'self_muted', 'self_deafened', 'server_muted']) {
        const s = state();
        s.clients[1][flag] = true;
        assert.deepEqual(overlayVoiceState(s).speakers.map(s => s.name), ['Me']);
    }
    const s = state();
    s.settings.muted_users = ['other'];
    assert.deepEqual(overlayVoiceState(s).speakers.map(s => s.name), ['Me']);
});

test('legacy all-members preference cannot expose quiet members', () => {
    const s = state();
    s.settings.gaming_overlay_speakers_only = false;
    assert.deepEqual(overlayVoiceState(s).speakers.map(s => s.name), ['Me', 'Peer']);
});

test('disable and disconnect hide the overlay', () => {
    assert.deepEqual(overlayVoiceState({ ...state(), settings: { gaming_overlay: false } }), { active: false });
    assert.deepEqual(overlayVoiceState({ ...state(), pc: null }), { active: false });
});

test('private calls filter silent and muted participants before the eight-person limit', () => {
    const call = { active: true, label: 'Private call', speakers: [
        ...Array.from({ length: 9 }, (_, i) => ({ name: `Quiet ${i}` })),
        { id: 'alice', name: 'Alice', speaking: true }, { id: 'bob', name: 'Bob', speaking: true, muted: true },
    ] };
    const snapshot = overlayVoiceState(state(), call);
    assert.deepEqual(snapshot.speakers.map(s => s.id), ['alice']);
    assert.equal(snapshot.title, 'Private call');
    call.speakers[9].speaking = false;
    assert.deepEqual(overlayVoiceState(state(), call), { active: false });
});
