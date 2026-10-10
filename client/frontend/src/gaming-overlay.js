import { t } from './i18n.js';
import { overlayVoiceState } from './gaming-overlay-state.js';
import { createOverlayAvatars } from './gaming-overlay-avatars.js';
import { createOverlayColors } from './overlay-colors.js';

export async function initGamingOverlay() {
    const app = window.go?.main?.App;
    if (!app?.GamingOverlayAvailable || !await app.GamingOverlayAvailable().catch(() => false)) return;
    let pending = false, warned = false, stopped = false, last = '', heartbeat = 0;
    const avatars = createOverlayAvatars();
    const colors = createOverlayColors();
    const update = async () => {
        if (pending || stopped) return;
        const state = window.__noxa.state;
        const call = window.__noxaPrivateCalls?.overlaySnapshot?.();
        const members = call?.active ? call.speakers || [] : state.clients.filter(member => member.channel_id === state.myChannelID)
            .map(member => ({ id: member.unique_id || String(member.client_id || '') }));
        const self = state.clients.find(member=>String(member.client_id) === String(state.myClientID));
        const selfID = call?.active ? call.selfID || "" : self?.unique_id || String(state.myClientID || "");
        const assigned = colors(JSON.stringify([state.activeTabID,state.serverGeneration,state.myChannelID,!!call?.active]), members,state.settings,selfID);
        const snapshot = avatars(overlayVoiceState(state, call), state);
        for (const speaker of snapshot.speakers || []) speaker.color = assigned.get(speaker.id);
        const encoded = JSON.stringify(snapshot) + state.settings?.gaming_overlay_position;
        if (last === encoded && (!snapshot.active || Date.now() - heartbeat < 1500)) return;
        pending = true;
        try {
            const error = await app.UpdateGamingOverlay(snapshot);
            if (error) throw new Error(error);
            last = encoded; heartbeat = Date.now();
        } catch (error) { if (!warned) { warned = true; window.__noxa.toast(t('overlay.failed', { error: error.message || String(error) }), 'warn'); } }
        finally { pending = false; }
    };
    const timer = setInterval(() => void update(), 250);
    window.addEventListener('beforeunload', () => { stopped = true; clearInterval(timer); void app.UpdateGamingOverlay({ active: false }); }, { once: true });
    void update();
}
