export const VOICE_MAX_BYTES = 5 * 1024 * 1024;
export const VOICE_MAX_MS = 5 * 60 * 1000;

// Each recording owns its microphone independently of voice transmission.
export function createVoiceRecording({ capture, Recorder = globalThis.MediaRecorder, maxBytes = VOICE_MAX_BYTES, maxDurationMs = VOICE_MAX_MS, onReady = () => {}, onError = () => {} }) {
    let session = null, serial = 0, disposed = false;
    const release = owner => { clearTimeout(owner.timer); owner.stream?.getTracks().forEach(track => track.stop()); owner.stream = null; };
    const discard = () => {
        serial++;
        const owner = session; session = null;
        if (!owner) return;
        owner.cancelled = true;
        if (owner.recorder?.state !== 'inactive') owner.recorder?.stop();
        release(owner);
    };
    return {
        async start() {
            if (disposed) throw new Error('Recording is closed');
            if (!Recorder) throw new Error('Audio recording is unavailable');
            discard();
            const owner = { serial: ++serial, chunks: [], bytes: 0, stream: null };
            session = owner;
            try {
                const stream = await capture();
                if (disposed || owner !== session || owner.serial !== serial) { stream.getTracks().forEach(track => track.stop()); return; }
                owner.stream = stream;
                const mimeType = ['audio/webm;codecs=opus', 'audio/ogg;codecs=opus', 'audio/mp4'].find(type => Recorder.isTypeSupported(type));
                if (!mimeType) throw new Error('No supported audio recording format');
                const recorder = new Recorder(stream, { mimeType, audioBitsPerSecond: 48000 });
                owner.recorder = recorder;
                owner.result = new Promise((resolve, reject) => { owner.resolve = resolve; owner.reject = reject; });
                void owner.result.catch(() => {});
                recorder.ondataavailable = event => {
                    if (owner.cancelled || !event.data.size) return;
                    owner.bytes += event.data.size;
                    if (owner.bytes > maxBytes) { owner.error = new Error('Recording exceeds the size limit'); if (recorder.state !== 'inactive') recorder.stop(); return; }
                    owner.chunks.push(event.data);
                };
                recorder.onerror = event => { owner.error = event.error || new Error('Recording failed'); if (recorder.state !== 'inactive') recorder.stop(); else finish(); };
                const finish = () => {
                    release(owner);
                    if (owner.cancelled || owner !== session) { owner.resolve(null); return; }
                    if (owner.error || !owner.bytes) {
                        const error = owner.error || new Error('Recording contains no audio');
                        owner.reject(error); onError(error); return;
                    }
                    const type = recorder.mimeType || mimeType;
                    const result = { blob: new Blob(owner.chunks, { type }), extension: type.includes('ogg') ? 'ogg' : type.includes('mp4') ? 'm4a' : 'weba' };
                    owner.chunks = [];
                    owner.resolve(result); onReady(result);
                };
                recorder.onstop = finish;
                recorder.start(250);
                owner.timer = setTimeout(() => { if (recorder.state !== 'inactive') recorder.stop(); }, maxDurationMs);
            } catch (error) { release(owner); if (owner === session) { session = null; throw error; } }
        },
        async stop() { const owner = session; if (!owner?.recorder) return null; if (owner.recorder.state !== 'inactive') owner.recorder.stop(); return owner.result; },
        discard,
        dispose() { disposed = true; discard(); },
    };
}
