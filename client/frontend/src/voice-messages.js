import { t } from './i18n.js';
import { icon } from './icons.js';
import { captureConstraints } from './audio.js';
import { closeDialog, mountDialog } from './modal.js';
import { createVoiceRecording, VOICE_MAX_BYTES, VOICE_MAX_MS } from './voice-recording.js';
import { parseFileRef } from './chat-parsers.js';
import { isBase64Payload } from './safe-media.js';
import { voicePlaybackDetails, releaseVoicePlayback } from './voice-playback.js';
import './voice-messages.css';

const V = () => window.__noxa;
const players = new Set();
let playbackObserver;
export function refreshVoicePlayback() { for (const player of players) if (player.isConnected) V()?.applyOutputSettings?.(player); }
export function trackVoicePlayback(audio) {
    players.add(audio);
    audio.addEventListener('play', () => { for (const other of players) if (other !== audio) other.pause(); V()?.applyOutputSettings?.(audio); });
    if (!playbackObserver) {
        playbackObserver = new MutationObserver(() => {
            for (const player of players) if (!player.isConnected) { releaseVoicePlayback(player); player.pause(); player.removeAttribute('src'); player.load(); players.delete(player); }
            if (!players.size) { playbackObserver.disconnect(); playbackObserver = null; }
        });
        playbackObserver.observe(document.body, { childList: true, subtree: true });
    }
}

export function renderVoiceMessage(container, text, { tabID, channelID = 0, isCurrent = () => true }) {
    const match = /^\[file:([^\]]+)\]$/.exec(text || '');
    if (!match) return false;
    const ref = parseFileRef(match[1]);
    if (!ref.valid) return false;
    const extension = ref.name.split('.').pop().toLowerCase();
    const mime = { weba: 'audio/webm', ogg: 'audio/ogg', m4a: 'audio/mp4' }[extension];
    if (!mime) return false;
    const wrap = document.createElement('div'); wrap.className = 'voice-message';
    const load = document.createElement('button'); load.type = 'button'; load.textContent = t('voiceMessage.play');
    const status = document.createElement('span'); status.setAttribute('role', 'status');
    wrap.append(load, status); container.append(wrap);
    const retry = () => {
        if (!isCurrent() || !wrap.isConnected) return;
        const focused = wrap.contains(document.activeElement);
        load.removeAttribute('aria-disabled'); load.textContent = t('voiceMessage.retry');
        wrap.replaceChildren(load, status); status.textContent = t('voiceMessage.unavailable');
        if (focused) load.focus();
    };
    load.onclick = async () => {
        if (!isCurrent() || !wrap.isConnected || load.getAttribute('aria-disabled') === 'true') return;
        // Keep the initiating control focused during download so keyboard users
        // continue at the player, without stealing focus if they move elsewhere.
        load.setAttribute('aria-disabled', 'true'); status.textContent = t('voiceMessage.loading');
        try {
            const data = await window.go.main.App.DownloadChatAttachmentForTab(tabID, channelID, ref.storage, ref.key);
            if (!isCurrent() || !wrap.isConnected) return;
            if (data.length > Math.ceil(VOICE_MAX_BYTES / 3) * 4 || !isBase64Payload(data)) throw new Error('Invalid audio attachment');
            const audio = document.createElement('audio'); audio.controls = true; audio.preload = 'metadata'; audio.setAttribute('aria-label', t('voiceMessage.player'));
            audio.src = `data:${mime};base64,${data}`;
            audio.onerror = () => {
                retry();
                audio.onerror = null; releaseVoicePlayback(audio); audio.pause(); audio.removeAttribute('src'); audio.load();
            };
            const focused = document.activeElement === load;
            wrap.replaceChildren(audio, status); status.textContent = '';
            if (focused) audio.focus();
            trackVoicePlayback(audio);
            const bytes = Uint8Array.from(atob(data), character => character.charCodeAt(0));
            voicePlaybackDetails(audio, new Blob([bytes], {type:mime}), `${ref.storage}#${ref.key}`);
        } catch { retry(); }
    };
    return true;
}

function asBase64(blob) {
    return new Promise((resolve, reject) => {
        const reader = new FileReader(); reader.onload = () => resolve(reader.result.split(',')[1]); reader.onerror = () => reject(reader.error); reader.readAsDataURL(blob);
    });
}

// createContext is evaluated on click so a toolbar never retains an old recipient.
export function voiceMessageButton(createContext) {
    const button = document.createElement('button'); button.type = 'button'; button.className = 'icon-btn voice-message-button';
    button.innerHTML = icon('mic'); button.title = t('voiceMessage.open'); button.setAttribute('aria-label', t('voiceMessage.open'));
    button.onclick = () => openVoiceMessage(createContext());
    return button;
}

function openVoiceMessage(context) {
    if (!context?.isCurrent()) return;
    const overlay = document.createElement('div'); overlay.className = 'dlg-overlay';
    const dialog = document.createElement('div'); dialog.className = 'dlg voice-message-dialog';
    const heading = document.createElement('h3'); heading.textContent = t('voiceMessage.title');
    const hint = document.createElement('p'); hint.textContent = t('voiceMessage.hint');
    const status = document.createElement('p'); status.setAttribute('role', 'status');
    const feedback = document.createElement('div'); feedback.className = 'voice-recording-feedback'; feedback.hidden = true;
    const elapsed = document.createElement('output'); elapsed.className = 'voice-recording-time'; elapsed.setAttribute('aria-live', 'off');
    elapsed.setAttribute('aria-label', t('voiceMessage.elapsed'));
    const meter = document.createElement('div'); meter.className = 'voice-recording-meter'; meter.setAttribute('role', 'meter');
    meter.setAttribute('aria-label', t('voiceMessage.meter')); meter.setAttribute('aria-valuemin', '-60'); meter.setAttribute('aria-valuemax', '0'); meter.setAttribute('aria-valuenow', '-60');
    const level = document.createElement('span'); meter.append(level);
    const quality = document.createElement('span'); quality.className = 'voice-recording-quality';
    const warning = document.createElement('p'); warning.className = 'voice-recording-warning'; warning.setAttribute('role', 'status');
    feedback.append(elapsed, meter, quality);
    const player = document.createElement('audio'); player.controls = true; player.hidden = true; player.setAttribute('aria-label', t('voiceMessage.player'));
    const actions = document.createElement('div'); actions.className = 'voice-message-actions';
    const make = (key, callback) => { const b = document.createElement('button'); b.type = 'button'; b.textContent = t(key); b.onclick = callback; actions.append(b); return b; };
    let capture, details, url = '', result = null, sending = false, started = 0, recording = false, closed = false, token = '';
    const current = () => !closed && context.isCurrent() && overlay.isConnected;
    const clearPreview = () => { releaseVoicePlayback(player); player.pause(); player.removeAttribute('src'); player.load(); player.hidden = true; details?.remove(); if (url) URL.revokeObjectURL(url); url = ''; result = null; token = ''; };
    const fail = error => { if (!current()) return; recording = false; feedback.hidden = true; warning.textContent = ''; record.disabled = false; stop.disabled = true; status.textContent = t('voiceMessage.failed', { error: error.message || String(error) }); };
    const clockTime = milliseconds => { const seconds = Math.floor(milliseconds / 1000); return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`; };
    const updateTime = () => {
        const duration = Math.min(VOICE_MAX_MS, Math.max(0, Date.now() - started));
        elapsed.textContent = `${clockTime(duration)} / ${clockTime(VOICE_MAX_MS)}`;
        if (duration >= VOICE_MAX_MS - 30000 && !warning.textContent) warning.textContent = t('voiceMessage.limitWarning');
    };
    const record = make('voiceMessage.start', async () => {
        if (!current()) return;
        clearPreview(); warning.textContent = ''; meter.hidden = false; quality.textContent = ''; meter.setAttribute('aria-valuenow', '-60'); level.style.width = '0%';
        send.disabled = true; record.disabled = true; status.textContent = t('voiceMessage.permission');
        try { await capture.start(); if (current() && !result) { recording = true; started = Date.now(); stop.disabled = false; feedback.hidden = false; status.textContent = t('voiceMessage.recording'); updateTime(); } }
        catch (error) { fail(error); }
    });
    const stop = make('voiceMessage.stop', async () => { stop.disabled = true; try { await capture.stop(); } catch (error) { fail(error); } }); stop.disabled = true;
    const send = make('voiceMessage.send', async () => {
        if (!current() || !result || sending) return;
        sending = true; send.disabled = record.disabled = true; status.textContent = t('voiceMessage.sending');
        try {
            const data = await asBase64(result.blob);
            if (!current()) return;
            if (!token) token = await window.go.main.App.UploadChatAttachmentForTab(context.tabID, context.channelID || 0, `voice-${crypto.randomUUID()}.${result.extension}`, data);
            if (!current()) return;
            const error = await context.send(token);
            if (error) throw new Error(error);
            if (current()) closeDialog(overlay);
        } catch (error) { fail(error); }
        finally { sending = false; if (current()) { send.disabled = !result; record.disabled = false; } }
    }); send.disabled = true;
    make('voiceMessage.discard', () => closeDialog(overlay));
    capture = createVoiceRecording({
        capture: () => navigator.mediaDevices.getUserMedia({ audio: captureConstraints(null), video: false }),
        onLevel: reading => {
            if (!current()) return;
            meter.hidden = !reading;
            if (!reading) { quality.textContent = t('voiceMessage.meterUnavailable'); return; }
            meter.setAttribute('aria-valuenow', reading.db.toFixed(1));
            level.style.width = `${Math.max(0, Math.min(100, (reading.db + 60) / 60 * 100))}%`;
            meter.dataset.quality = reading.quality;
            quality.textContent = t(`mic.${reading.quality}`);
        },
        onReady: value => {
            if (!current()) return;
            recording = false; feedback.hidden = true; warning.textContent = ''; result = value; url = URL.createObjectURL(value.blob); player.src = url; player.hidden = false;
            details = voicePlaybackDetails(player, value.blob);
            V()?.applyOutputSettings?.(player); status.textContent = t('voiceMessage.ready'); record.disabled = false; stop.disabled = true; send.disabled = false;
        }, onError: fail,
    });
    dialog.append(heading, hint, status, feedback, warning, player, actions); overlay.append(dialog);
    const timer = setInterval(() => {
        if (!context.isCurrent()) { closeDialog(overlay); return; }
        if (recording) updateTime();
    }, 250);
    mountDialog(overlay, { onClose: () => { closed = true; clearInterval(timer); capture.dispose(); clearPreview(); } });
    trackVoicePlayback(player);
}
