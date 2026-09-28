import { t } from './i18n.js';
import { waveformPeaks, boundedPlaybackHistory, PLAYBACK_SPEEDS } from './voice-playback-state.js';

const storageKey = 'noxa:voice-playback:v1';
const releases = new WeakMap();
export function releaseVoicePlayback(audio) { releases.get(audio)?.(); releases.delete(audio); }
const readHistory = () => { try { return boundedPlaybackHistory(JSON.parse(localStorage.getItem(storageKey) || '[]')); } catch { return []; } };
const time = seconds => `${Math.floor(seconds / 60)}:${String(Math.floor(seconds % 60)).padStart(2,'0')}`;
async function referenceKey(reference) {
    if (!reference) return '';
    const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(reference));
    return Array.from(new Uint8Array(digest), n => n.toString(16).padStart(2,'0')).join('');
}

export function voicePlaybackDetails(audio, blob, reference = '') {
    releaseVoicePlayback(audio);
    const controls = document.createElement('div'); controls.className = 'voice-playback-details';
    const canvas = document.createElement('canvas'); canvas.width = 384; canvas.height = 40; canvas.className = 'voice-waveform'; canvas.setAttribute('role','img'); canvas.setAttribute('aria-label',t('voiceMessage.waveform'));
    const seek = document.createElement('input'); seek.type = 'range'; seek.min = '0'; seek.max = '0'; seek.step = '.1'; seek.value = '0'; seek.disabled = true; seek.setAttribute('aria-label',t('voiceMessage.seek'));
    const duration = document.createElement('output'); duration.textContent = '0:00'; duration.setAttribute('aria-label',t('voiceMessage.duration'));
    const speed = document.createElement('select'); speed.setAttribute('aria-label',t('voiceMessage.speed'));
    for (const value of PLAYBACK_SPEEDS) { const option = document.createElement('option'); option.value = String(value); option.textContent = `${value}×`; speed.append(option); } speed.value = '1';
    controls.append(canvas, seek, duration, speed); audio.after(controls);
    let seconds = 0, key = '', restored = false, peaks = Array(64).fill(0), lastSaved = 0, released = false, analysis, sampledStream, sampleTimer;
    const current = () => !released && controls.isConnected && audio.isConnected;
    const draw = () => {
        const ctx = canvas.getContext('2d'); ctx.clearRect(0,0,384,40);
        const position = Number(audio.currentTime) || 0;
        for (let i=0; i<peaks.length; i++) {
            const height = Math.max(2, peaks[i]*38);
            ctx.fillStyle = i / peaks.length <= position / seconds ? '#36d7ed' : '#657985'; ctx.fillRect(i*6, (40-height)/2, 4, height);
        }
    };
    const save = () => {
        if (!key || !seconds || !audio.hasAttribute('src')) return;
        const position = audio.ended || audio.currentTime >= seconds-.25 ? 0 : Math.max(0,Math.min(seconds, audio.currentTime || 0));
        try { localStorage.setItem(storageKey, JSON.stringify(boundedPlaybackHistory([{key,position,speed:audio.playbackRate,at:Date.now()}, ...readHistory().filter(e => e.key !== key)]))); } catch { /* Playback remains usable when local storage is unavailable. */ }
        lastSaved = Date.now();
    };
    const restore = () => {
        if (restored || !key || !seconds || audio.readyState < 1) return;
        restored = true;
        const saved = readHistory().find(e => e.key === key);
        if (saved) { audio.playbackRate = saved.speed; speed.value = String(saved.speed); if (saved.position < seconds-.25) audio.currentTime = saved.position; }
    };
    const stopAnalysis = () => {
        clearInterval(sampleTimer);
        sampledStream?.getTracks().forEach(track => track.stop()); sampledStream = null;
        if (analysis) void analysis.close().catch(() => {}); analysis = null;
    };
    // Remote attachments are untrusted compressed media. Sample playback into fixed
    // bins instead of expanding an arbitrary attachment into a complete PCM buffer.
    const samplePlayback = () => {
        if (!reference || analysis || !current() || typeof audio.captureStream !== 'function') return;
        try {
            sampledStream = audio.captureStream();
            if (!sampledStream.getAudioTracks().length) { stopAnalysis(); return; }
            analysis = new AudioContext();
            void analysis.resume().catch(() => {});
            const source = analysis.createMediaStreamSource(sampledStream);
            const analyser = analysis.createAnalyser(); analyser.fftSize = 256;
            source.connect(analyser);
            const samples = new Float32Array(256);
            sampleTimer = setInterval(() => {
                if (!current()) { stopAnalysis(); return; }
                if (audio.paused || !seconds) return;
                analyser.getFloatTimeDomainData(samples);
                const index = Math.min(63, Math.floor(audio.currentTime / seconds * 64));
                for (const value of samples) peaks[index] = Math.max(peaks[index], Math.abs(value));
                draw();
            }, 50);
        } catch { stopAnalysis(); }
    };
    const refresh = () => {
        if (!current()) return;
        if (Number.isFinite(audio.duration) && audio.duration > 0) seconds = audio.duration;
        seek.disabled = !seconds; seek.max = String(seconds); seek.value = String(Math.min(seconds,audio.currentTime || 0));
        duration.textContent = `${time(audio.currentTime || 0)} / ${time(seconds)}`;
        restore(); draw();
    };
    let durationProbe = false;
    const metadata = () => {
        // MediaRecorder WebM often omits its duration. Let the media element find
        // its seekable end without decoding the entire clip into a JS buffer.
        if (reference && audio.duration === Infinity && !durationProbe) {
            durationProbe = true; audio.currentTime = 1e10;
        }
        refresh();
    };
    const durationChanged = () => {
        if (durationProbe && Number.isFinite(audio.duration)) { durationProbe = false; audio.currentTime = 0; }
        refresh();
    };
    const progress = () => { refresh(); if (!durationProbe && Date.now()-lastSaved > 5000) save(); };
    audio.addEventListener('loadedmetadata', metadata); audio.addEventListener('durationchange', durationChanged);
    audio.addEventListener('timeupdate', progress);
    audio.addEventListener('playing', samplePlayback);
    audio.addEventListener('pause', save); audio.addEventListener('ended', save);
    audio.addEventListener('pause', stopAnalysis); audio.addEventListener('ended', stopAnalysis);
    seek.oninput = () => { if (seconds) { audio.currentTime = Number(seek.value); refresh(); save(); } };
    speed.onchange = () => { audio.playbackRate = Number(speed.value); save(); };
    void referenceKey(reference).then(value => { key = value; if (current()) refresh(); });
    releases.set(audio, () => {
        save(); released = true; stopAnalysis();
        audio.removeEventListener('loadedmetadata', metadata); audio.removeEventListener('durationchange', durationChanged);
        audio.removeEventListener('timeupdate', progress); audio.removeEventListener('playing', samplePlayback);
        audio.removeEventListener('pause', save); audio.removeEventListener('ended', save);
        audio.removeEventListener('pause', stopAnalysis); audio.removeEventListener('ended', stopAnalysis);
        controls.remove();
    });
    if (!reference) void (async () => {
        let context;
        try {
            context = new AudioContext();
            const decoded = await context.decodeAudioData(await blob.arrayBuffer());
            if (!current()) return;
            seconds = decoded.duration;
            peaks = waveformPeaks(Array.from({length:decoded.numberOfChannels},(_,i) => decoded.getChannelData(i)));
            refresh();
        } catch { if (current()) refresh(); }
        finally { if (context) await context.close().catch(() => {}); }
    })();
    refresh();
    return controls;
}
