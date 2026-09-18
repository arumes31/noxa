// A settings-only capture session. It never borrows or stops the live call stream.
export class MicCheck {
    constructor({ onState = () => {}, onLevel = () => {}, onSilence = () => {}, onCountdown = () => {},
        onCalibrated = () => {}, onError = () => {}, getUserMedia = constraints => navigator.mediaDevices.getUserMedia(constraints),
        createContext = () => new (window.AudioContext || window.webkitAudioContext)(), now = () => performance.now() } = {}) {
        Object.assign(this, { onState, onLevel, onSilence, onCountdown, onCalibrated, onError, getUserMedia, createContext, now });
        this.current = null;
    }

    async start(mode, constraints) {
        if (this.current) return false;
        const session = { mode };
        this.current = session;
        this.onState("requesting", mode);
        try {
            const stream = await this.getUserMedia({ audio: constraints });
            session.stream = stream;
            if (this.current !== session) { stream.getTracks().forEach(track => track.stop()); return false; }
            const ctx = this.createContext();
            session.ctx = ctx;
            session.source = ctx.createMediaStreamSource(stream);
            session.analyser = ctx.createAnalyser();
            session.analyser.fftSize = 512;
            session.source.connect(session.analyser);
            await ctx.resume();
            if (this.current !== session) return false;
            const samples = new Float32Array(session.analyser.fftSize);
            const started = this.now();
            let lastSignal = started, silence = false, seconds = -1, sum = 0, count = 0;
            this.onState("active", mode);
            const tick = () => {
                if (this.current !== session) return;
                session.analyser.getFloatTimeDomainData(samples);
                const level = samples.reduce((total, value) => total + Math.abs(value), 0) / samples.length;
                this.onLevel(Math.min(1, level * 5));
                const elapsed = this.now() - started;
                if (mode === "calibration") {
                    sum += level; count++;
                    const remaining = Math.max(0, Math.ceil((5000 - elapsed) / 1000));
                    if (seconds !== remaining) { seconds = remaining; this.onCountdown(remaining); }
                    if (elapsed >= 5000) {
                        const floor = sum / count;
                        this.stop();
                        this.onCalibrated({ floor, suggested: Math.min(100, Math.max(1, Math.round((floor * 1.5 + .01) * 100))) });
                    }
                } else {
                    if (level > .001) lastSignal = this.now();
                    const nextSilence = this.now() - lastSignal >= 2000;
                    if (silence !== nextSilence) { silence = nextSilence; this.onSilence(silence); }
                }
            };
            session.timer = setInterval(tick, 50);
            tick();
            return true;
        } catch (error) {
            if (this.current !== session) return false;
            this.stop();
            this.onError(error, mode);
            return false;
        }
    }

    setLoopback(enabled) {
        const session = this.current;
        if (!session?.source || session.mode !== "test") return;
        if (session.delay) {
            session.source.disconnect(session.delay);
            session.delay.disconnect();
            session.delay = null;
        }
        if (enabled) {
            session.delay = session.ctx.createDelay(.5);
            session.delay.delayTime.value = .15;
            session.source.connect(session.delay).connect(session.ctx.destination);
        }
    }

    stop() {
        const session = this.current;
        if (!session) return;
        this.current = null;
        clearInterval(session.timer);
        session.stream?.getTracks().forEach(track => track.stop());
        for (const node of [session.source, session.analyser, session.delay]) {
            try { node?.disconnect(); } catch { /* already detached */ }
        }
        if (session.ctx) void session.ctx.close().catch(() => {});
        this.onLevel(0);
        this.onState("idle", session.mode);
    }
}
