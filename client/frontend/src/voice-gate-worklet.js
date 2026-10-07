import { VoiceGate } from "./voice-gate-core.js";

class MicrophoneGate extends globalThis.AudioWorkletProcessor {
    constructor() {
        super();
        this.gate = new VoiceGate(globalThis.sampleRate);
        this.frames = 0;
        this.revision = 0;
        this.port.onmessage = ({ data }) => {
            this.gate.configure(data);
            this.revision = data.revision;
            this.port.postMessage({ revision: this.revision, ready: true });
        };
    }

    process(inputs, outputs) {
        const output = outputs[0];
        this.gate.process(inputs[0] || [], output);
        this.frames += output[0]?.length || 0;
        if (this.active !== this.gate.active || this.frames >= globalThis.sampleRate / 10) {
            this.active = this.gate.active;
            this.frames = 0;
            this.port.postMessage({ revision: this.revision, active: this.active, level: this.gate.level });
        }
        return true;
    }
}

globalThis.registerProcessor("noxa-microphone-gate", MicrophoneGate);
