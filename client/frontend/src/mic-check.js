export const micDB = value => Math.max(-60, 20 * Math.log10(Math.max(value, 0.001)));

export function measureMic(samples) {
    let square = 0, mean = 0, peak = 0;
    for (const sample of samples) { square += sample * sample; mean += Math.abs(sample); peak = Math.max(peak, Math.abs(sample)); }
    const rms = Math.sqrt(square / (samples.length || 1));
    mean /= samples.length || 1;
    // The average uses the same absolute-amplitude measure as live voice activation.
    return { rms, mean, peak, db: micDB(mean), peakDB: micDB(peak),
        quality: peak >= 0.98 ? "clipping" : rms < 0.001 ? "silent" : rms < 0.025 ? "quiet" : "good" };
}

export function recommendThreshold(ambient, speech) {
    const percentile = (values, position) => [...values].sort((a, b) => a - b)[Math.floor((values.length - 1) * position)] || 0;
    const floor = percentile(ambient, 0.9), voice = percentile(speech, 0.75);
    const level = Math.max(0.002, floor * 1.5, floor + (voice - floor) * 0.25);
    // Round conservatively on the same 0.1% grid as the manual control.
    const suggested = Math.min(100, Math.max(1, Math.ceil(level / 0.2 * 1000) / 10));
    return { floor, voice, suggested, valid: voice > Math.max(0.005, floor * 2) && suggested / 100 * 0.2 < voice };
}

// Settings capture and playback are independent of all live call streams.
export class MicCheck {
    constructor({ onState = () => {}, onLevel = () => {}, onCountdown = () => {}, onDevice = () => {},
        onCalibrated = () => {}, onRecorded = () => {}, onError = () => {},
        getUserMedia = constraints => navigator.mediaDevices.getUserMedia(constraints),
        createContext = () => new (window.AudioContext || window.webkitAudioContext)(), now = () => performance.now() } = {}) {
        Object.assign(this, { onState, onLevel, onCountdown, onDevice, onCalibrated, onRecorded, onError, getUserMedia, createContext, now });
        this.current = null; this.playback = null; this.recordingURL = null;
    }

    async start(mode, constraints, outputDevice = "") {
        if (this.current) return false;
        this.clearRecording();
        const session = { mode, outputDevice, heldPeak: 0, peakUntil: 0 };
        this.current = session; this.onState("requesting", mode);
        try {
            const stream = await this.getUserMedia({ audio: constraints }); session.stream = stream;
            if (this.current !== session) { stream.getTracks().forEach(track => track.stop()); return false; }
            const track = stream.getAudioTracks()[0];
            if (!track) throw new DOMException("No microphone track", "NotFoundError");
            track.onended = () => { if (this.current === session) { this.stop(); this.onError(new DOMException("Microphone disconnected", "NotFoundError"), mode); } };
            this.onDevice(track.label, track.getSettings?.().deviceId || "");
            const ctx = this.createContext(); session.ctx = ctx;
            session.source = ctx.createMediaStreamSource(stream);
            session.analyser = ctx.createAnalyser(); session.analyser.fftSize = 2048;
            session.source.connect(session.analyser);
            await ctx.resume();
            if (this.current !== session) return false;
            if (mode === "record") {
                const recorder = new MediaRecorder(stream); session.recorder = recorder;
                const chunks = [];
                recorder.ondataavailable = event => { if (event.data.size) chunks.push(event.data); };
                recorder.onerror = event => { if (this.current === session) { this.stop(); this.onError(event.error || new Error("Recording failed"), mode); } };
                recorder.onstop = () => {
                    if (this.current !== session || !session.completed) return;
                    const blob = new Blob(chunks, { type: recorder.mimeType });
                    this.stop();
                    if (!blob.size) { this.onError(new Error("Empty recording"), mode); return; }
                    this.recordingURL = URL.createObjectURL(blob); this.onRecorded();
                };
                recorder.start();
            }
            const samples = new Float32Array(session.analyser.fftSize);
            const started = this.now(), ambient = [], speech = [];
            let countdown = "";
            this.onState("active", mode);
            const tick = () => {
                if (this.current !== session) return;
                session.analyser.getFloatTimeDomainData(samples);
                const level = measureMic(samples), now = this.now(), elapsed = now - started;
                if (level.peak >= session.heldPeak || now >= session.peakUntil) { session.heldPeak = level.peak; session.peakUntil = now + 1200; }
                this.onLevel({ ...level, quality: session.heldPeak >= 0.98 ? "clipping" : level.quality, heldPeakDB: micDB(session.heldPeak) });
                if (mode === "test") return;
                const phase = mode === "record" ? "record" : elapsed < 5000 ? "ambient" : "speech";
                const remaining = Math.max(0, Math.ceil(((phase === "speech" ? 10000 : 5000) - elapsed) / 1000));
                if (countdown !== `${phase}:${remaining}`) { countdown = `${phase}:${remaining}`; this.onCountdown(remaining, phase); }
                if (mode === "calibration") {
                    (phase === "ambient" ? ambient : speech).push(level.mean);
                    if (elapsed >= 10000) { this.stop(); this.onCalibrated(recommendThreshold(ambient, speech)); }
                } else if (elapsed >= 5000 && !session.completed) {
                    session.completed = true; clearInterval(session.timer); session.recorder.stop();
                }
            };
            session.timer = setInterval(tick, 50); tick(); return true;
        } catch (error) {
            if (this.current !== session) return false;
            this.stop(); this.onError(error, mode); return false;
        }
    }

    async setLoopback(enabled) {
        const session = this.current;
        if (!session?.source || session.mode !== "test") return;
        this.stopPlayback();
        session.delay?.disconnect();
        if (session.delay) session.source.disconnect(session.delay);
        session.output?.stream.getTracks().forEach(track => track.stop());
        session.delay = null; session.output = null;
        if (!enabled) return;
        session.delay = session.ctx.createDelay(.5); session.delay.delayTime.value = .15;
        session.output = session.ctx.createMediaStreamDestination();
        session.source.connect(session.delay).connect(session.output);
        const output = session.output;
        try { await this.play(output.stream, session.outputDevice); }
        catch (error) {
            if (this.current === session && session.output === output) await this.setLoopback(false);
            throw error;
        }
    }

    async play(source, device) {
        this.stopPlayback();
        const audio = new Audio(); this.playback = audio;
        if (typeof source === "string") audio.src = source; else audio.srcObject = source;
        try {
            if (audio.setSinkId) await audio.setSinkId(device || "");
            else if (device) throw new Error("Output device selection is unavailable");
            if (this.playback !== audio) return;
            await audio.play();
        } catch (error) {
            if (this.playback !== audio) return;
            this.stopPlayback(); throw error;
        }
    }

    stopPlayback() {
        const audio = this.playback; this.playback = null;
        if (audio) { audio.pause(); audio.srcObject = null; audio.removeAttribute("src"); audio.load(); }
    }

    clearRecording() {
        this.stopPlayback();
        if (this.recordingURL) URL.revokeObjectURL(this.recordingURL);
        this.recordingURL = null;
    }

    stop() {
        this.stopPlayback();
        const session = this.current;
        if (!session) return;
        this.current = null; clearInterval(session.timer);
        if (session.recorder) {
            session.recorder.ondataavailable = session.recorder.onstop = session.recorder.onerror = null;
            if (session.recorder.state !== "inactive") session.recorder.stop();
        }
        session.stream?.getTracks().forEach(track => { track.onended = null; track.stop(); });
        session.output?.stream.getTracks().forEach(track => track.stop());
        for (const node of [session.source, session.analyser, session.delay, session.output]) {
            try { node?.disconnect(); } catch { /* already detached */ }
        }
        if (session.ctx) void session.ctx.close().catch(() => {});
        this.onLevel({ ...measureMic([]), heldPeakDB: -60 }); this.onState("idle", session.mode);
    }

    dispose() { this.stop(); this.clearRecording(); }
}
