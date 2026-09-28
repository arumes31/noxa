import test from 'node:test';
import assert from 'node:assert/strict';
import { overlayVoiceState } from '../src/gaming-overlay-state.js';

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
