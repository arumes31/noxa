// All event/preview paths share one original PCM sound set.
import { SoundEngine } from "./sound-engine.js";
import { SOUND_EVENTS, SOUND_DEFINITIONS, SOUND_URLS } from "./sound-catalog.js";
import { SPEECH_ASSETS } from "./speech-catalog.js";
import { SpeechQueue, SPEECH_EVENTS } from "./speech-queue.js";
export { SOUND_EVENTS, SOUND_EVENT_GROUPS } from "./sound-catalog.js";

const V = () => window.__voicx;
const definitions = { ...SOUND_DEFINITIONS }, urls = { ...SOUND_URLS };
for (const [language, clips] of Object.entries(SPEECH_ASSETS)) {
    for (const [event, clip] of Object.entries(clips)) {
        const id = "speech_" + language + "_" + event;
        definitions[id] = { duration: clip.duration, priority: SPEECH_EVENTS[event].priority, cooldown: 0, category: "Speech" };
        urls[id] = clip.url;
    }
}
export const soundEngine = new SoundEngine({
    definitions, urls,
    getState: () => V()?.state,
    isDND: () => !!window.__voicxPolish?.dndActive?.(),
    createContext: () => new (window.AudioContext || window.webkitAudioContext)({ latencyHint: "interactive" }),
    load: async (url) => {
        const response = await fetch(url);
        if (!response.ok) throw new Error("sound asset unavailable");
        return response.arrayBuffer();
    },
});
export const speechQueue = new SpeechQueue({ engine: soundEngine, assets: SPEECH_ASSETS,
    getState: () => V()?.state, isDND: () => !!window.__voicxPolish?.dndActive?.(),
    systemLanguage: () => navigator.language || "en" });
export function playSpeech(event, options) { return speechQueue.enqueue(event, options); }
export function clearSpeech(category) { speechQueue.clear(category); }
export async function previewSpeech(settings) {
    stopPreviews();
    const generation = previewGeneration;
    await Promise.all([soundEngine.preload(), soundEngine.resume()]);
    if (generation !== previewGeneration) return;
    await soundEngine.setOutput(settings?.playback_device_id);
    if (generation === previewGeneration) playSpeech("test", { settings, preview: true, delay: 0 });
}

export function playEvent(name, force = false) {
    return soundEngine.play(name, { force });
}

let previewGeneration = 0;
let previewTimer = null;
let finishDelay = null;
export function stopPreviews() {
    previewGeneration++;
    clearTimeout(previewTimer);
    previewTimer = null;
    finishDelay?.(); finishDelay = null;
    soundEngine.stopPreviews();
    speechQueue.stopPreview();
    void soundEngine.setOutput(V()?.state.settings?.playback_device_id);
}

// Previews bypass only master mute/history, preserving DND, per-event choices
// and volume. Draft settings never mutate saved preferences.
export async function previewSounds(events = SOUND_EVENTS, settings = V()?.state.settings, onLabel = () => {}) {
    stopPreviews();
    const generation = previewGeneration;
    await Promise.all([soundEngine.preload(), soundEngine.resume()]);
    if (generation !== previewGeneration) return;
    await soundEngine.setOutput(settings?.playback_device_id);
    for (const name of events) {
        if (generation !== previewGeneration) return;
        if (!Object.hasOwn(SOUND_DEFINITIONS, name)) continue;
        if (!soundEngine.play(name, { force: true, settings })) continue;
        onLabel(SOUND_DEFINITIONS[name].label);
        await new Promise(resolve => {
            finishDelay = resolve;
            previewTimer = setTimeout(resolve, (SOUND_DEFINITIONS[name].duration + .18) * 1000);
        });
        finishDelay = null; previewTimer = null;
    }
    if (generation === previewGeneration) onLabel("");
}

export function testAll(settings, onLabel) { return previewSounds(SOUND_EVENTS, settings, onLabel); }
export function updateSoundOutput() {
    speechQueue.reconcile();
    return soundEngine.setOutput(V()?.state.settings?.playback_device_id);
}

export function initSounds() {
    void soundEngine.preload();
    void updateSoundOutput();
    const resume = () => { void soundEngine.resume(); };
    const deviceChange = () => {
        soundEngine.requestedSink = undefined;
        void updateSoundOutput();
    };
    const visibility = () => { if (document.visibilityState === "visible") resume(); };
    window.addEventListener("pointerdown", resume, { passive: true });
    window.addEventListener("keydown", resume);
    window.addEventListener("focus", resume);
    document.addEventListener("visibilitychange", visibility);
    navigator.mediaDevices?.addEventListener?.("devicechange", deviceChange);
    window.addEventListener("pagehide", () => {
        stopPreviews();
        speechQueue.clear();
        window.removeEventListener("pointerdown", resume);
        window.removeEventListener("keydown", resume);
        window.removeEventListener("focus", resume);
        document.removeEventListener("visibilitychange", visibility);
        navigator.mediaDevices?.removeEventListener?.("devicechange", deviceChange);
        void soundEngine.dispose();
    }, { once: true });
}
