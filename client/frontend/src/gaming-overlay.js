import { t } from './i18n.js';
import { overlayVoiceState } from './gaming-overlay-state.js';
import { createOverlayAvatars } from './gaming-overlay-avatars.js';

export async function initGamingOverlay() {
    const app = window.go?.main?.App;
    if (!app?.GamingOverlayAvailable || !await app.GamingOverlayAvailable().catch(() => false)) return;
    let pending = false, warned = false, stopped = false, last = '', heartbeat = 0;
    const avatars = createOverlayAvatars();
    const update = async () => {
        if (pending || stopped) return;
        const state = window.__noxa.state;
        const snapshot = avatars(overlayVoiceState(state, window.__noxaPrivateCalls?.overlaySnapshot?.()), state);
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
