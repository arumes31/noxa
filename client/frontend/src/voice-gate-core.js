export const VOICE_PRE_ROLL_MS = 100;
export const VAD_RELEASE_MS = 450;

// Runs in the audio rendering thread. No timers, wall clock, or UI callbacks
// participate in opening the gate. The ring is local and never persisted.
export class VoiceGate {
    constructor(rate) {
        this.delay = Math.round(rate * VOICE_PRE_ROLL_MS / 1000);
        this.release = Math.round(rate * VAD_RELEASE_MS / 1000) + this.delay;
        this.rings = [new Float32Array(this.delay), new Float32Array(this.delay)];
        this.mode = "ptt";
        this.blocked = true;
        this.threshold = 0.1;
        this.reset();
    }

    reset() {
        for (const ring of this.rings) ring.fill(0);
        this.position = 0;
        this.remaining = 0;
        this.active = false;
    }

    configure({ mode, blocked, threshold }) {
        mode = ["vad", "continuous", "ptt"].includes(mode) ? mode : "ptt";
        blocked = blocked !== false;
        if (mode !== this.mode || blocked !== this.blocked) this.reset();
        this.mode = mode;
        this.blocked = blocked;
        if (Number.isFinite(threshold)) this.threshold = Math.max(0, Math.min(0.2, threshold));
    }

    process(input, output) {
        const size = output[0]?.length || 0;
        if (!size) return;
        let level = 0;
        for (const samples of input) {
            let sum = 0;
            for (let i = 0; i < size; i++) sum += Math.abs(samples[i] || 0);
            level = Math.max(level, sum / size);
        }
        this.level = level;
        if (this.blocked) {
            for (const samples of output) samples.fill(0);
            return;
        }
        if (this.mode !== "vad") {
            for (let c = 0; c < output.length; c++) {
                if (input[c]) output[c].set(input[c]);
                else output[c].fill(0);
            }
            this.active = true;
            return;
        }
        if (level > this.threshold) this.remaining = this.release + size;
        for (let i = 0; i < size; i++) {
            const open = this.remaining > 0;
            for (let c = 0; c < output.length; c++) {
                const ring = this.rings[c];
                output[c][i] = open && ring ? ring[this.position] : 0;
                if (ring) ring[this.position] = input[c]?.[i] || 0;
            }
            this.position = (this.position + 1) % this.delay;
            if (this.remaining > 0) this.remaining--;
        }
        this.active = this.remaining > 0;
    }
}
