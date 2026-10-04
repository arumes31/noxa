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

test('self mute and deafen do not hide another active speaker', () => {
    for (const privacy of [{ muted: true }, { deafened: true }]) {
        assert.deepEqual(overlayVoiceState({ ...state(), ...privacy }).speakers.map(s => s.name), ['Peer']);
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
