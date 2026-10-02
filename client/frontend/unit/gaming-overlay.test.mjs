import test from 'node:test';
import assert from 'node:assert/strict';
import { createOverlayVisibility, overlayVoiceState } from '../src/gaming-overlay-state.js';

test('connection overlay expires even while voice state keeps updating', () => {
    const visible = createOverlayVisibility();
    const state = { activeTabID: 'a', serverGeneration: 1, sessionGeneration: 1, myChannelID: 1 };
    const snapshot = { active: true, title: 'Lobby', speakers: [] };
    assert.equal(visible(state, snapshot, 1000).active, true);
    assert.equal(visible(state, { ...snapshot, speakers: [{ name: 'Talking', speaking: true }] }, 5999).active, true);
    assert.equal(visible(state, snapshot, 6000).active, false);
    assert.equal(visible(state, { ...snapshot, muted: true }, 10000).active, false);
    assert.equal(visible({ ...state, myChannelID: 2 }, snapshot, 11000).active, true);
    assert.equal(visible({ ...state, myChannelID: 2 }, snapshot, 16000).active, false);
});

test('reconnection starts a fresh notice and disconnection hides immediately', () => {
    const visible = createOverlayVisibility();
    const state = { activeTabID: 'a', serverGeneration: 1, sessionGeneration: 1, myChannelID: 1 };
    const snapshot = { active: true };
    visible(state, snapshot, 0);
    assert.equal(visible(state, snapshot, 6000).active, false);
    assert.equal(visible({ ...state, sessionGeneration: 2 }, snapshot, 7000).active, true);
    assert.equal(visible(state, { active: false }, 7001).active, false);
    assert.equal(visible(state, snapshot, 7002).active, true);
});

test('overlay defaults enabled only in active voice and respects the off switch', () => {
    const state = { settings: {}, channels: [{ ChannelID: 1, Name: 'Lobby' }], clients: [], myChannelID: 1, pc: {} };
    assert.equal(overlayVoiceState(state).active, true);
    assert.equal(overlayVoiceState({ ...state, settings: { gaming_overlay: false } }).active, false);
    assert.equal(overlayVoiceState({ ...state, pc: null }).active, false);
});
test('overlay prioritizes current-channel speakers and does not expose other channels', () => {
    const state = { settings: { muted_users: ['b'] }, channels: [], myChannelID: 1, pc: {}, clients: [{ channel_id: 1, nickname: 'B', unique_id: 'b' }, { channel_id: 2, nickname: 'Hidden', is_speaking: true }, { channel_id: 1, nickname: 'A', is_speaking: true }] };
    assert.deepEqual(overlayVoiceState(state).speakers, [{ name: 'A', speaking: true, muted: false }, { name: 'B', speaking: false, muted: true }]);
});
test('private call participants replace channel state and explicit disable wins', () => {
    const call = { active: true, label: 'Private call', speakers: [{ name: 'Alice' }] };
    assert.equal(overlayVoiceState({ settings: {} }, call).title, 'Private call');
    assert.equal(overlayVoiceState({ settings: { gaming_overlay: false } }, call).active, false);
});
test('speakers-only keeps active private-call speakers even after eight silent members', () => {
    const call = { active: true, label: 'Private call', speakers: [...Array.from({ length: 8 }, (_, i) => ({ name: `Silent ${i}`, speaking: false })), { name: 'Speaking', speaking: true }] };
    assert.deepEqual(overlayVoiceState({ settings: { gaming_overlay_speakers_only: true } }, call).speakers, [{ name: 'Speaking', speaking: true }]);
});
