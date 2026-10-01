import { t } from './i18n.js';

const panels = new WeakMap();
const triggers = new WeakMap();
let opening = false;
let active = null;

function copyPresentation(target) {
    target.document.documentElement.lang = document.documentElement.lang;
    for (const [key, value] of Object.entries(document.documentElement.dataset)) target.document.documentElement.dataset[key] = value;
    target.document.documentElement.style.cssText = document.documentElement.style.cssText;
    for (const sheet of document.querySelectorAll('link[rel="stylesheet"],style')) {
        const copy = sheet.cloneNode(true);
        if (sheet.href) copy.href = sheet.href;
        target.document.head.append(copy);
    }
    const style = target.document.createElement('style');
    style.textContent = 'html,body{margin:0;height:100%;overflow:auto;background:var(--bg,#14212a);color:var(--text,#eee)}body .private-call-panel{position:static;inset:auto;width:100%;max-width:none;max-height:none;min-height:100%;box-sizing:border-box;border:0;border-radius:0}.undocked-channel{padding:16px;box-sizing:border-box}.undocked-channel-controls{display:flex;flex-wrap:wrap;gap:8px}.undocked-channel video{width:100%;max-height:45vh;object-fit:contain}.undocked-channel-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:8px}';
    target.document.head.append(style);
}

export function closeCallUndock(panel) { panels.get(panel)?.dock(); }

function restoreCallFocus(panel, trigger) {
    for (const control of new Set([trigger, ...(triggers.get(panel) || [])])) {
        if (!control?.isConnected || control.ownerDocument !== document) continue;
        const closedOptions = control.closest('details:not([open])');
        const target = closedOptions?.querySelector(':scope > summary') || control;
        if (target.disabled || !target.getClientRects().length) continue;
        target.focus({ preventScroll: true });
        return;
    }
}

export function callUndockButton(panel) {
    const button = document.createElement('button'); button.type = 'button'; button.className = 'call-control ui-button';
    const refresh = () => { button.textContent = t(panels.has(panel) ? 'call.dock' : 'call.undock'); button.setAttribute('aria-pressed', String(panels.has(panel))); };
    refresh();
    if (!triggers.has(panel)) triggers.set(panel, new Set());
    triggers.get(panel).add(button);
    if (!window.documentPictureInPicture) { button.disabled = true; button.title = t('call.undockUnavailable'); }
    button.onclick = async () => {
        if (panels.has(panel)) { panels.get(panel).dock(true); refresh(); return; }
        if (opening || !panel.isConnected) return;
        opening = true; button.disabled = true;
        try {
            active?.dock();
            const target = await window.documentPictureInPicture.requestWindow({ width: 640, height: 520 });
            if (!panel.isConnected) { target.close(); return; }
            const anchor = document.createComment('docked call position'); panel.before(anchor);
            let closed = false;
            const blur = () => panel.dispatchEvent(new Event('noxa-call-blur'));
            const dock = (restoreFocus = false) => {
                if (closed) return; closed = true;
                panel.dispatchEvent(new Event('noxa-call-docking'));
                if (anchor.parentNode) anchor.replaceWith(panel);
                else panel.remove();
                panels.delete(panel); if (active?.panel === panel) active = null;
                target.removeEventListener('pagehide', onPageHide); window.removeEventListener('beforeunload', dock);
                target.removeEventListener('blur', blur);
                if (!target.closed) target.close();
                // Call controls may have been recreated by a membership update.
                for (const control of triggers.get(panel) || []) {
                    if (!control.isConnected) { triggers.get(panel).delete(control); continue; }
                    control.textContent = t('call.undock'); control.setAttribute('aria-pressed', 'false');
                }
                // User docking returns to a visible control; teardown and
                // switching calls preserve focus in the active workspace.
                if (restoreFocus === true) restoreCallFocus(panel, button);
            };
            const onPageHide = () => dock(true);
            panels.set(panel, { dock }); active = { panel, dock };
            copyPresentation(target); target.document.title = t('call.active'); target.document.body.append(panel);
            target.addEventListener('pagehide', onPageHide, { once: true }); window.addEventListener('beforeunload', dock, { once: true });
            target.addEventListener('blur', blur);
            refresh();
        } catch (error) { window.__noxa?.toast?.(t('call.failed', { error: error.message || String(error) }), 'warn'); }
        finally { opening = false; button.disabled = !window.documentPictureInPicture; }
    };
    button.dataset.callUndock = '';
    return button;
}

// Channel controls retain their original nodes and handlers in the main window.
// The detached view forwards actions and displays muted copies of video tracks;
// it never owns, stops, or duplicates the channel's audio pipeline.
export function initChannelUndocking() {
    const bar = document.querySelector('#voice-bar .voice-buttons');
    if (!bar || bar.querySelector('[data-channel-undock]')) return;
    const panel = document.createElement('section'); panel.className = 'undocked-channel'; panel.hidden = true;
    document.body.append(panel);
    const undock = callUndockButton(panel); undock.dataset.channelUndock = ''; undock.classList.add('voice-control');
    (bar.querySelector('.voice-secondary-actions') || bar).append(undock);
    const title = document.createElement('h2'), people = document.createElement('p'), controls = document.createElement('div'), grid = document.createElement('div');
    controls.className = 'undocked-channel-controls'; grid.className = 'undocked-channel-grid';
    panel.append(title, people, controls, grid, callUndockButton(panel));
    const copies = new Map(), buttons = new Map(); let scope = '';
    const hold = document.createElement('button'); hold.type = 'button'; hold.dataset.undockPtt = '';
    controls.append(hold);
    let held = false;
    const scopeNow = state => `${state.activeTabID}:${state.serverGeneration}:${state.myChannelID}`;
    const release = () => {
        if (!held) return;
        held = false; hold.setAttribute('aria-pressed', 'false'); window.__noxa?.setPTT?.(false);
    };
    const press = () => {
        const state = window.__noxa?.state;
        if (held || hold.disabled || !panels.has(panel) || !state?.pc || scopeNow(state) !== scope || (state.settings?.activation_mode || 'ptt') !== 'ptt') return;
        held = true; hold.setAttribute('aria-pressed', 'true'); window.__noxa.setPTT(true);
    };
    hold.onpointerdown = event => { if (event.button !== 0) return; hold.setPointerCapture(event.pointerId); press(); };
    hold.onpointerup = hold.onpointercancel = hold.onlostpointercapture = hold.onblur = release;
    hold.onkeydown = event => { if (event.key === ' ' || event.key === 'Enter') {event.preventDefault(); press();} };
    hold.onkeyup = event => { if (event.key === ' ' || event.key === 'Enter') {event.preventDefault(); release();} };
    panel.addEventListener('noxa-call-docking', release);
    panel.addEventListener('noxa-call-blur', release);
    const tick = () => {
        const state = window.__noxa?.state;
        if (!state) return;
        const nextScope = scopeNow(state);
        const live = !!(state.pc && state.myChannelID);
        undock.hidden = !live;
        if (!live || (scope && scope !== nextScope)) { release(); closeCallUndock(panel); }
        scope = nextScope;
        panel.hidden = !panels.has(panel);
        if (panel.hidden) { for (const video of copies.values()) { video.pause(); video.srcObject = null; } copies.clear(); grid.replaceChildren(); return; }
        hold.hidden = (state.settings?.activation_mode || 'ptt') !== 'ptt';
        hold.disabled = !!document.getElementById('ptt-btn')?.disabled || typeof window.__noxa.setPTT !== 'function';
        hold.textContent = t('workspace.ptt');
        if (hold.hidden || hold.disabled) release();
        title.textContent = state.channels.find(channel => channel.ChannelID === state.myChannelID)?.Name || t('call.active');
        people.textContent = state.clients.filter(client => client.channel_id === state.myChannelID).map(client => `${client.is_speaking ? '● ' : ''}${client.nickname}`).join(' · ');
        for (const id of ['voice-mute', 'voice-deafen', 'voice-video', 'voice-screen', 'voice-leave-channel']) {
            const original = document.getElementById(id); if (!original) continue;
            let copy = buttons.get(id);
            if (!copy) { copy = document.createElement('button'); buttons.set(id,copy); controls.append(copy); }
            copy.textContent = original.getAttribute('aria-label') || original.title || original.textContent;
            copy.disabled = original.disabled; copy.setAttribute('aria-pressed', original.getAttribute('aria-pressed') || 'false');
            copy.onclick = () => {
                const state = window.__noxa.state;
                if (`${state.activeTabID}:${state.serverGeneration}:${state.myChannelID}` === nextScope) original.click();
                else closeCallUndock(panel);
            };
        }
        const sources = new Set(document.querySelectorAll('#video-grid video, #local-video:not(.hidden)'));
        for (const [source, video] of copies) if (!sources.has(source)) { video.pause(); video.srcObject = null; video.remove(); copies.delete(source); }
        for (const source of sources) {
            let video = copies.get(source);
            if (!video) { video = document.createElement('video'); video.muted = true; video.autoplay = true; video.playsInline = true; copies.set(source, video); grid.append(video); }
            video.style.transform = source.style.transform;
            if (video.srcObject !== source.srcObject) { video.srcObject = source.srcObject; void video.play().catch(() => {}); }
        }
    };
    const timer = setInterval(tick, 500); tick();
    window.addEventListener('beforeunload', () => { clearInterval(timer); closeCallUndock(panel); }, { once: true });
}
