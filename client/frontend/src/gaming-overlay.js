import { t } from './i18n.js';
import { createOverlayVisibility, overlayVoiceState } from './gaming-overlay-state.js';
import { dndActive } from './polish-ui.js';

let notification = null;
export function overlayNotification(text) {
    const state = window.__noxa?.state;
    if (state && !dndActive(state.settings)) notification = { text: String(text).slice(0, 140), until: Date.now() + 5000, tabID: state.activeTabID, generation: state.serverGeneration };
}
export async function initGamingOverlay() {
    const app = window.go?.main?.App;
    if (!app?.GamingOverlayAvailable || !await app.GamingOverlayAvailable().catch(() => false)) return;
    let pending = false, warned = false, stopped = false, last = '', heartbeat = 0;
    const visible = createOverlayVisibility();
    const update = async () => {
        if (pending || stopped) return;
        const state = window.__noxa.state;
        const snapshot = visible(state, overlayVoiceState(state, window.__noxaPrivateCalls?.overlaySnapshot?.()));
        if (snapshot.active) {
            snapshot.status = t(snapshot.deafened ? 'overlay.deafened' : snapshot.muted ? 'overlay.muted' : 'overlay.ready');
            if (notification?.until > Date.now() && notification.tabID === state.activeTabID && notification.generation === state.serverGeneration && !dndActive(state.settings)) snapshot.notification = notification.text;
        } else notification = null;
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
