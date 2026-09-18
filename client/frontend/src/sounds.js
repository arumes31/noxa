// All event/preview paths share one original PCM sound set.
import { SoundEngine } from "./sound-engine.js";
import { SOUND_EVENTS, SOUND_DEFINITIONS, SOUND_URLS } from "./sound-catalog.js";
import { SPEECH_ASSETS } from "./speech-catalog.js";
import { SpeechQueue, SPEECH_EVENTS, speechLanguage } from "./speech-queue.js";
export { SPEECH_EVENTS } from "./speech-queue.js";
export { SOUND_EVENTS, SOUND_EVENT_GROUPS } from "./sound-catalog.js";

const V = () => window.__noxa;
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
    isDND: settings => !!window.__noxaPolish?.dndActive?.(settings),
    onStatusChange: () => window.dispatchEvent(new CustomEvent("noxa-audio-status")),
    createContext: () => new (window.AudioContext || window.webkitAudioContext)({ latencyHint: "interactive" }),
    load: async (url) => {
        const response = await fetch(url);
        if (!response.ok) throw new Error("sound asset unavailable");
        return response.arrayBuffer();
    },
});
export const speechQueue = new SpeechQueue({ engine: soundEngine, assets: SPEECH_ASSETS,
    getState: () => V()?.state, isDND: settings => !!window.__noxaPolish?.dndActive?.(settings),
    systemLanguage: () => navigator.language || "en" });
export function playSpeech(event, options) { return speechQueue.enqueue(event, options); }
export function playAlert(event, options) {
    stopPreviews();
    return speechQueue.enqueue(event, { ...options, withEffect: true });
}
export function clearSpeech(category) { speechQueue.clear(category); }
export function speechPreviewLabel(event, settings) {
    return SPEECH_ASSETS[speechLanguage(settings, navigator.language)]?.[event]?.transcript || event;
}
function preloadSounds(settings = V()?.state.settings) {
    const language = speechLanguage(settings, navigator.language);
    return soundEngine.preload([...SOUND_EVENTS, ...Object.keys(SPEECH_ASSETS[language]).map(id => "speech_" + language + "_" + id)]);
}
export function audioStatus(settings = V()?.state.settings) {
    if (!settings) return [{ reason: "unavailable" }];
    const reasons = [];
    const add = (condition, reason, count) => { if (condition) reasons.push({ reason, count }); };
    add(settings.play_sounds === false, "master_muted");
    add(window.__noxaPolish?.dndActive?.(settings), "dnd");
    add(settings.effects_enabled === false, "effects_disabled");
    add(settings.sound_volume === 0, "effects_zero");
    add(settings.spoken_messages === false, "speech_disabled");
    add(settings.speech_volume === 0, "speech_zero");
    const disabled = SOUND_EVENTS.filter(event => settings.event_sounds?.[event] === false).length;
    add(disabled, "disabled_events", disabled);
    const matrixDisabled = SOUND_EVENTS.filter(event => settings.notify_matrix?.[event]?.sound === false).length;
    add(matrixDisabled, "disabled_notifications", matrixDisabled);
    const speechDisabled = Object.keys(SPEECH_EVENTS).filter(event => !speechQueue.allowed(event, { ...settings, play_sounds: true, spoken_messages: true, dnd_enabled: false, dnd_from: "", dnd_to: "" }, true)).length;
    add(speechDisabled, "disabled_speech", speechDisabled);
    const output = soundEngine.outputState;
    add(output === "unavailable", "output_unavailable");
    add(output === "fallback", "fallback");
    add(output === "routing", "routing");
    add(output === "uninitialized", "uninitialized");
    add(soundEngine.ctx?.state === "suspended", "suspended");
    return reasons;
}
function previewFeedback(reason = "") {
    window.dispatchEvent(new CustomEvent("noxa-audio-preview-blocked", { detail: reason }));
}
function previewAllowed() {
    return !speechQueue.hasLiveSpeech() && ![...soundEngine.active].some(entry => !entry.preview);
}
async function preparePreview(settings, generation) {
    await Promise.all([preloadSounds(settings), soundEngine.resume()]);
    if (generation !== previewGeneration || !previewAllowed()) return false;
    await soundEngine.setOutput(settings?.playback_device_id);
    if (generation !== previewGeneration || !previewAllowed()) {
        void soundEngine.setOutput(V()?.state.settings?.playback_device_id);
        return false;
    }
    return true;
}
export async function previewSpeech(settings, events = ["test"], onLabel = () => {}) {
    if (!previewAllowed()) { previewFeedback("busy"); return; }
    previewFeedback();
    stopPreviews();
    const generation = previewGeneration;
    if (!await preparePreview(settings, generation)) return;
    for (const event of events) {
        if (generation !== previewGeneration) return;
        const id = "speech_" + speechLanguage(settings, navigator.language) + "_" + event;
        const reason = soundEngine.blockReason(id, { settings, preview: true, volume: settings?.speech_volume ?? 100 });
        if (reason || !speechQueue.allowed(event, settings, true)) {
            previewFeedback(reason || (settings?.spoken_messages === false ? "speech_disabled" : "event_disabled"));
            continue;
        }
        onLabel(event);
        await new Promise(resolve => {
            finishDelay = resolve;
            if (!playSpeech(event, { settings, preview: true, delay: 0, onEnded: resolve })) resolve();
        });
        finishDelay = null;
    }
    if (generation === previewGeneration) onLabel("");
}

export function playEvent(name) {
    return soundEngine.play(name);
}

export function updateConversationDucking() { soundEngine.updateDucking(); }

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
    if (!previewAllowed()) { previewFeedback("busy"); return; }
    previewFeedback();
    stopPreviews();
    const generation = previewGeneration;
    if (!await preparePreview(settings, generation)) return;
    for (const name of events) {
        if (generation !== previewGeneration) return;
        if (!Object.hasOwn(SOUND_DEFINITIONS, name)) continue;
        await new Promise(resolve => {
            finishDelay = resolve;
            const played = soundEngine.play(name, { preview: true, settings, onEnded: () => {
                if (generation !== previewGeneration) { resolve(); return; }
                previewTimer = setTimeout(resolve, 180);
            } });
            if (played) onLabel(SOUND_DEFINITIONS[name].label, name);
            else { previewFeedback(soundEngine.blockReason(name, { preview: true, settings }) || "busy"); resolve(); }
        });
        finishDelay = null; previewTimer = null;
    }
    if (generation === previewGeneration) onLabel("");
}

export function testAll(settings, onLabel) { return previewSounds(SOUND_EVENTS, settings, onLabel); }
export function updateSoundOutput() {
    updateConversationDucking();
    speechQueue.reconcile();
    void preloadSounds();
    return soundEngine.setOutput(V()?.state.settings?.playback_device_id);
}

export function initSounds() {
    void preloadSounds();
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
