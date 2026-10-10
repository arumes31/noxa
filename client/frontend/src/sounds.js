// All event/preview paths share one original PCM sound set.
import { SoundEngine } from "./sound-engine.js";
import { SOUND_DEFINITIONS, SOUND_URLS } from "./sound-catalog.js";
import { SPOKEN_ACTIONS, EFFECT_EVENTS as SOUND_EVENTS } from "./notification-audio.js";
import { SPEECH_ASSETS } from "./speech-catalog.js";
import { SpeechQueue, SPEECH_EVENTS, speechLanguage } from "./speech-queue.js";
export { SPEECH_EVENTS, speechEventEnabled } from "./speech-queue.js";
export { EFFECT_EVENTS as SOUND_EVENTS, EFFECT_GROUPS as SOUND_EVENT_GROUPS } from "./notification-audio.js";

const V = () => window.__noxa;
const definitions = Object.fromEntries(SOUND_EVENTS.map(id => [id, SOUND_DEFINITIONS[id]]));
const urls = Object.fromEntries(SOUND_EVENTS.map(id => [id, SOUND_URLS[id]]));
for (const [language, clips] of Object.entries(SPEECH_ASSETS)) {
    for (const [event, clip] of Object.entries(clips)) {
        const id = "speech_" + language + "_" + event;
        definitions[id] = { duration: clip.duration, priority: SPEECH_EVENTS[event].priority, cooldown: 0, category: "Speech",
            application: SPEECH_EVENTS[event].category === "application" };
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
    return speechQueue.enqueue(event, options);
}
export function clearSpeech(category) { speechQueue.clear(category); }
export async function playClosingAnnouncement() {
    const settings = V()?.state.settings;
    const id = "speech_" + speechLanguage(settings, navigator.language) + "_client_closing";
    if (!speechQueue.allowed("client_closing", settings, false)
        || !soundEngine.allowed(id, { settings, volume: settings?.speech_volume ?? 100 })) return;
    stopPreviews();
    speechQueue.clear();
    await Promise.all([soundEngine.preload([id]), soundEngine.resume()]);
    await soundEngine.setOutput(settings.playback_device_id);
    await new Promise(resolve => {
        // Closing belongs to the application, so a server switch or journal
        // replay must not cancel it with the per-server announcement queue.
        if (!speechQueue.allowed("client_closing", V()?.state.settings, false)
            || !soundEngine.play(id, { scope: "application", volume: settings.speech_volume ?? 100, onEnded: resolve })) resolve();
    });
}
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
    const matrixDisabled = Object.values(settings.notify_matrix || {}).filter(row => row?.sound === false).length;
    add(matrixDisabled, "disabled_notifications", matrixDisabled);
    const speechDisabled = Object.keys(SPEECH_EVENTS).filter(event => !speechQueue.allowed(event, { ...settings, play_sounds: true, spoken_messages: true, dnd_enabled: false, dnd_from: "", dnd_to: "" }, true)).length;
    add(speechDisabled, "disabled_speech", speechDisabled);
    const output = soundEngine.outputState;
    add(output === "unavailable", "output_unavailable");
    add(output === "fallback", "fallback");
    add(output === "routing", "routing");
    add(output === "uninitialized", "uninitialized");
    add(soundEngine.ctx?.state === "suspended", "suspended");
    const failed = [...soundEngine.warnings].filter(warning => warning.startsWith("could not load ")
        && !soundEngine.buffers.has(warning.slice("could not load ".length))).length;
    add(failed, "assets_failed", failed);
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
    // A transient startup/device failure must not leave previews on a cached
    // failed route after voice has successfully opened the selected device.
    await soundEngine.setOutput(settings?.playback_device_id, { retry: true });
    if (generation !== previewGeneration || !previewAllowed()) {
        void soundEngine.setOutput(V()?.state.settings?.playback_device_id);
        return false;
    }
    return true;
}
async function previewAudio(items, settings, onLabel) {
    if (!previewAllowed()) { previewFeedback("busy"); return; }
    previewFeedback();
    stopPreviews();
    const generation = previewGeneration;
    if (!await preparePreview(settings, generation)) return;
    for (const { event, speech = false, effect } of items) {
        if (generation !== previewGeneration) return;
        const id = speech ? "speech_" + speechLanguage(settings, navigator.language) + "_" + event : event;
        const options = { settings, preview: true, ...(speech ? { volume: settings?.speech_volume ?? 100 } : {}) };
        const reason = soundEngine.blockReason(id, options);
        if (reason || (speech && !speechQueue.allowed(event, settings, true, effect))) {
            previewFeedback(reason || (settings?.spoken_messages === false ? "speech_disabled" : "event_disabled"));
            continue;
        }
        onLabel(speech ? speechPreviewLabel(event, settings) : SOUND_DEFINITIONS[event].label, event, speech);
        await new Promise(resolve => {
            finishDelay = resolve;
            const onEnded = () => {
                if (generation !== previewGeneration) { resolve(); return; }
                previewTimer = setTimeout(resolve, 180);
            };
            const played = speech ? playSpeech(event, { ...options, effect, delay: 0, onEnded })
                : soundEngine.play(event, { ...options, onEnded });
            if (!played) resolve();
        });
        finishDelay = null; previewTimer = null;
    }
    if (generation === previewGeneration) onLabel("");
}

export function previewSpeech(settings, events = ["test"], onLabel = () => {}) {
    return previewAudio(events.map(event => ({ event, speech: true })), settings, (_label, event = "") => onLabel(event));
}

export function playEvent(name) {
    if (Object.hasOwn(SPOKEN_ACTIONS, name)) return playAlert(SPOKEN_ACTIONS[name], { effect: name });
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
export function previewSounds(events = SOUND_EVENTS, settings = V()?.state.settings, onLabel = () => {}) {
    return previewAudio(events.map(event => Object.hasOwn(SPOKEN_ACTIONS, event)
        ? { event: SPOKEN_ACTIONS[event], speech: true, effect: event } : { event }), settings, onLabel);
}

export function testAll(settings, onLabel = () => {}) {
    return previewAudio([
        ...Object.keys(SPEECH_EVENTS).map(event => ({ event, speech: true })),
        ...SOUND_EVENTS.map(event => ({ event })),
    ], settings, onLabel);
}
export function updateSoundOutput() {
    updateConversationDucking();
    speechQueue.reconcile();
    void preloadSounds();
    return soundEngine.setOutput(V()?.state.settings?.playback_device_id, { retry: true });
}

export function initSounds() {
    void preloadSounds();
    void updateSoundOutput();
    const resume = () => {
        void soundEngine.resume();
        void soundEngine.setOutput(V()?.state.settings?.playback_device_id);
    };
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
