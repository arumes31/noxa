// Original, locally synthesized waiting music. No network/media permissions.
const ID = "update_waiting_music";
const PREFERENCE = "noxa.updateMusicMuted";
const DEFINITION = { category: "Application", application: true, priority: 0, cooldown: 0, gain: .18, loop: true };

export function createWaitingMelody(ctx) {
    const rate = 24000, beat = .625, duration = 32 * beat;
    const buffer = ctx.createBuffer(1, duration * rate, rate);
    const samples = buffer.getChannelData(0);
    // A quiet pentatonic music-box phrase over four warm, sustained chords.
    const phrase = [76, 79, 83, 79, 74, 78, 81, 78, 72, 76, 79, 76, 74, 78, 81, 78];
    const chords = [[48, 55, 60], [47, 54, 59], [45, 52, 57], [43, 50, 55]];
    const note = (midi, start, length, volume) => {
        const frequency = 440 * 2 ** ((midi - 69) / 12);
        const count = Math.floor(length * rate), offset = Math.floor(start * rate);
        for (let i = 0; i < count && offset + i < samples.length; i++) {
            const time = i / rate;
            const envelope = Math.min(1, time / .035) * Math.min(1, (length - time) / .25) * Math.exp(-time * 1.5 / length);
            samples[offset + i] += volume * envelope * (Math.sin(2 * Math.PI * frequency * time) + .15 * Math.sin(4 * Math.PI * frequency * time));
        }
    };
    phrase.forEach((pitch, i) => note(pitch, i * 2 * beat, 1.15, .043));
    chords.forEach((chord, i) => chord.forEach(pitch => note(pitch, i * 8 * beat, 4.85, .012)));
    // Silence at the loop boundary prevents clicks, including on a short retry.
    samples[0] = samples[samples.length - 1] = 0;
    return buffer;
}

export function startUpdateMusic({ engine, storage, onChange = () => {},
    schedule = fn => setInterval(fn, 250), unschedule = clearInterval }) {
    let muted = false, closed = false, busy = false, lastState = "";
    try { storage ??= globalThis.localStorage; muted = storage?.getItem(PREFERENCE) === "true"; } catch { /* optional preference */ }
    engine.definitions[ID] = DEFINITION;
    const entries = () => [...engine.active].filter(entry => entry.name === ID);
    const silence = () => { for (const entry of entries()) engine.retire(entry); };
    const notify = () => {
        const blocked = !!engine.blockReason(ID);
        const state = { muted, playing: entries().length > 0, blocked };
        const key = JSON.stringify(state);
        if (key !== lastState && !closed) { lastState = key; onChange(state); }
    };
    async function reconcile() {
        if (closed) return;
        if (muted || !engine.allowed(ID)) { silence(); notify(); return; }
        if (busy) return;
        busy = true;
        try {
            const ctx = engine.context();
            if (!ctx) return;
            if (!engine.buffers.has(ID)) engine.buffers.set(ID, createWaitingMelody(ctx));
            await engine.setOutput(engine.getState()?.settings?.playback_device_id);
            if (closed || muted || !engine.allowed(ID)) return;
            const volume = engine.getState()?.settings?.sound_volume;
            for (const entry of entries()) if (entry.musicVolume !== volume) engine.retire(entry);
            if (!entries().length && ctx.state === "running") {
                engine.play(ID, { scope: "application" });
                for (const entry of entries()) entry.musicVolume = volume;
            }
        } catch { /* Audio is optional; download and restart must still work. */ }
        finally { busy = false; notify(); }
    }
    const resume = () => { try { if (!muted && engine.allowed(ID)) void engine.resume().catch(() => {}); } catch { /* optional audio */ } };
    notify();
    resume();
    void reconcile();
    const timer = schedule(() => { void reconcile(); });
    return {
        toggle() { muted = !muted; try { storage?.setItem(PREFERENCE, String(muted)); } catch { /* optional preference */ }
            if (muted) silence(); else resume(); void reconcile(); notify(); },
        stop() { if (closed) return; closed = true; unschedule(timer); silence(); engine.buffers.delete(ID); },
    };
}
