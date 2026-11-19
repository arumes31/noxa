// All event/preview paths share one original PCM sound set.
import { SoundEngine } from "./sound-engine.js";
import { SOUND_DEFINITIONS, SOUND_URLS } from "./sound-catalog.js";
import { SPOKEN_ACTIONS, EFFECT_EVENTS as SOUND_EVENTS } from "./notification-audio.js";
import { SPEECH_ASSETS } from "./speech-catalog.js";
import { SpeechQueue, SPEECH_EVENTS, SPEECH_VARIANTS, CLOSING_SPEECH_CLIPS, speechLanguage } from "./speech-queue.js";
export { SPEECH_EVENTS, speechEventEnabled } from "./speech-queue.js";
export { EFFECT_EVENTS as SOUND_EVENTS, EFFECT_GROUPS as SOUND_EVENT_GROUPS } from "./notification-audio.js";

const V = () => window.__noxa;
const definitions = Object.fromEntries(SOUND_EVENTS.map(id => [id, SOUND_DEFINITIONS[id]]));
const urls = Object.fromEntries(SOUND_EVENTS.map(id => [id, SOUND_URLS[id]]));
for (const [language, clips] of Object.entries(SPEECH_ASSETS)) {
    for (const [event, clip] of Object.entries(clips)) {
        const id = "speech_" + language + "_" + event;
        const policy = SPEECH_EVENTS[event] || SPEECH_EVENTS[SPEECH_VARIANTS[event]];
        definitions[id] = { duration: clip.duration, priority: policy.priority, cooldown: 0, category: "Speech",
            muteException: event === "poke" || policy.muteException === true, application: policy.category === "application" };
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
    systemLanguage: () => navigator.language || "en", nameClip: spokenNameClip });

// Token bucket with a fractional level, refilled continuously.
function tokenBucket(capacity, perMs, now) {
    let tokens = capacity, updated = now();
    return {
        level() { const t = now(); tokens = Math.min(capacity, tokens + Math.max(0, t - updated) / perMs); updated = t; return tokens; },
        take() { if (this.level() < 1) return false; tokens--; return true; },
    };
}

// Every GetSpokenNameForTab request (prefetch, retry or announcement) is paced
// under the server's per-session limit of 40 burst then 1/s, keeping 10 for
// headroom; higher priority runs first. synthesis mirrors the server's budget
// for new clips (10 then 1/min): prefetch holds back while the clips it may
// cause (probing: first asks in flight) could spend the last two.
// shortcut: one pacer for all tabs, conservative since each tab has its own server budget.
export function createNamePacer({ burst = 30, perMs = 1000, now = () => Date.now(), schedule = (fn, ms) => setTimeout(fn, ms) } = {}) {
    const requests = tokenBucket(burst, perMs, now), queue = [];
    let timer = null;
    const drain = () => {
        timer = null;
        for (let index = 0; index < queue.length && requests.level() >= 1;) {
            const job = queue[index];
            if (job.stale?.(job)) { queue.splice(index, 1); continue; }
            if (job.held?.(job)) { index++; continue; }
            queue.splice(index, 1);
            requests.take();
            job.run();
        }
        if (!queue.length) return;
        // Wait for the next request token, or poll for held jobs.
        const mark = timer = {};
        const level = requests.level();
        schedule(() => { if (timer === mark) drain(); }, level >= 1 ? perMs : Math.ceil((1 - level) * perMs));
    };
    const pacer = {
        now, synthesis: tokenBucket(8, 60000, now), probing: 0,
        // job: { run, priority, stale(job): drop it, held(job): keep it queued for now }
        add(job) {
            const index = queue.findIndex(other => other.priority < job.priority);
            queue.splice(index < 0 ? queue.length : index, 0, job);
            if (!timer || requests.level() >= 1) drain();
            return job;
        },
        promote(job, priority) {
            const index = queue.indexOf(job);
            if (index < 0 || job.priority >= priority) return;
            queue.splice(index, 1);
            job.priority = priority;
            pacer.add(job);
        },
        get size() { return queue.length; },
    };
    return pacer;
}
export const namePacer = createNamePacer();

// Announcements outrank prefetch (my channel 2, voice channels 1, others 0).
export const NAME_PRIORITY_LIVE = 3;
// Raised from 256: a whole-server prefetch needs one entry per visible online
// user. Clips are decoded only when announced, so an entry is its ~50 KB data URL.
export const SPOKEN_NAME_LIMIT = 512;
const NAME_PREFETCH_LIMIT = SPOKEN_NAME_LIMIT - 64; // room for announcements
const NAME_RETRY_MS = 60000; // prefetch backoff after a failed fetch

// Spoken names fetched from the server this session: tab, language and
// nickname -> {id} once fetched, {ready, job} in flight, or {failedAt}.
// Least recently used first.
const spokenNames = new Map();
function rememberName(key, entry, engine) {
    spokenNames.delete(key);
    spokenNames.set(key, entry);
    while (spokenNames.size > SPOKEN_NAME_LIMIT) {
        const [oldKey, old] = spokenNames.entries().next().value;
        spokenNames.delete(oldKey);
        if (old.id && ![...spokenNames.values()].some(other => other.id === old.id)) {
            delete engine.definitions[old.id];
            delete engine.urls[old.id];
            engine.buffers.delete(old.id);
        }
    }
}

// Returns the decoded name clip ID, "" when none is coming, or (for an
// announcement) a promise of the ID or "". Prefetch (priority below
// NAME_PRIORITY_LIVE) only fetches and returns "".
export function spokenNameClip({ language, speaker, phrase }, app = window.go?.main?.App, engine = soundEngine,
    schedule = (fn, ms) => setTimeout(fn, ms), pacer = namePacer, priority = NAME_PRIORITY_LIVE) {
    const activeTab = () => V()?.state?.activeTabID || "";
    const tabID = activeTab();
    const key = JSON.stringify([tabID, language, speaker.nickname || ""]);
    const live = priority >= NAME_PRIORITY_LIVE;
    const decoded = id => {
        if (engine.buffers.has(id) && engine.buffers.has(phrase)) return id;
        return Promise.resolve(engine.preload([id, phrase])).then(() => engine.buffers.has(id) && engine.buffers.has(phrase) ? id : "");
    };
    const known = spokenNames.get(key);
    if (known?.id) {
        rememberName(key, known, engine);
        return live ? decoded(known.id) : "";
    }
    if (known && !known.failedAt) { // in flight
        if (known.job) pacer.promote(known.job, priority);
        if (!live) return "";
        return known.ready.then(ok => ok && known.id ? decoded(known.id) : "");
    }
    // Announcements retry a failed name at once; prefetch backs off.
    if (!app?.GetSpokenNameForTab || (!live && known && pacer.now() - known.failedAt < NAME_RETRY_MS)) return "";
    let settle;
    const entry = { ready: new Promise(resolve => { settle = resolve; }), job: null };
    rememberName(key, entry, engine);
    const fail = () => { entry.failedAt = pacer.now(); entry.job = null; settle(false); };
    // The server answers at once; a name it is still voicing is asked again a
    // few times. A failed fetch (no clip, user gone, older server) is retried
    // by a later announcement.
    const request = attempt => {
        entry.job = pacer.add({ priority: entry.job?.priority ?? priority, run: () => fetchName(attempt),
            // Evicted from the cache or asked for another tab.
            stale: () => {
                if (spokenNames.get(key) === entry && activeTab() === tabID) return false;
                fail();
                return true;
            },
            // A first prefetch ask may start a new clip: keep two for announcements.
            held: job => attempt === 1 && job.priority < NAME_PRIORITY_LIVE && pacer.synthesis.level() - pacer.probing < 1 });
    };
    const fetchName = attempt => {
        if (attempt === 1) pacer.probing++;
        const answered = pending => { if (attempt === 1) { pacer.probing--; if (pending) pacer.synthesis.take(); } };
        return new Promise(resolve => resolve(app.GetSpokenNameForTab(tabID, speaker.client_id, language))).then(data => {
            answered(data?.pending);
            // The session may have been renamed since: never file a clip under another name.
            if (data.nickname !== speaker.nickname) throw new Error("renamed");
            if (data.pending) {
                settle(false); // a new clip takes longer than an announcement waits
                if (attempt >= 6) throw new Error("still pending");
                schedule(() => request(attempt + 1), 3000);
                return;
            }
            const id = "speech_name_" + data.key;
            engine.definitions[id] = { duration: 1, priority: 1, cooldown: 0, category: "Speech" };
            engine.urls[id] = "data:audio/wav;base64," + data.data_base64;
            entry.id = id;
            entry.job = null;
            settle(true);
        }, error => { answered(false); throw error; }).catch(fail);
    };
    request(1);
    return live ? entry.ready.then(ok => ok ? decoded(entry.id) : "") : "";
}
// entries: [client, priority] pairs. When a server has more visible users
// than the cache holds, the highest priorities are kept.
export function prefetchSpokenNames(entries, settings = V()?.state.settings,
    { app = window.go?.main?.App, engine = soundEngine, pacer = namePacer, schedule } = {}) {
    if (!settings || settings.speech_names === false || settings.spoken_messages === false) return;
    const language = speechLanguage(settings, navigator.language);
    const ranked = [...entries].sort((a, b) => b[1] - a[1]).slice(0, NAME_PREFETCH_LIMIT);
    for (const [speaker, priority] of ranked) {
        if (speaker?.client_id && speaker.nickname) spokenNameClip({ language, speaker, phrase: "" }, app, engine, schedule, pacer, priority);
    }
}
export function playSpeech(event, options) { return speechQueue.enqueue(event, options); }
export function playAlert(event, options) {
    stopPreviews();
    return speechQueue.enqueue(event, options);
}
export function reconcileNotificationMute() {
    speechQueue.reconcile();
    soundEngine.reconcile();
}
export function clearSpeech(category) { speechQueue.clear(category); }
// Closing plays one of the recorded goodbyes at random. Only the closing
// user hears it, so the choice does not need to match other observers.
export function closingClip(language, random = Math.random) {
    const clips = CLOSING_SPEECH_CLIPS.filter(clip => SPEECH_ASSETS[language]?.[clip]);
    if (!clips.length) return "client_closing";
    const index = Math.min(clips.length - 1, Math.floor(Math.max(0, Number(random()) || 0) * clips.length));
    return clips[index];
}
export async function playUpdateAnnouncement(current = () => true) {
    const event = "client_updating", settings = V()?.state.settings;
    const language = speechLanguage(settings, navigator.language);
    const id = "speech_" + language + "_" + event;
    let finished = false;
    const allowed = () => !finished && current() && speechQueue.allowed(event, V()?.state.settings, false)
        && soundEngine.allowed(id, { settings: V()?.state.settings, volume: V()?.state.settings?.speech_volume ?? 100 });
    if (!allowed()) return;
    stopPreviews();
    speechQueue.clear();
    let timer;
    // Bounded even if an audio device disappears or decoding never settles.
    const playback = (async () => {
        await Promise.all([soundEngine.preload([id]), soundEngine.resume()]);
        if (!allowed()) return;
        await soundEngine.setOutput(settings.playback_device_id);
        if (!allowed()) return;
        await new Promise(resolve => {
            if (!soundEngine.play(id, { scope: "application", volume: V()?.state.settings?.speech_volume ?? 100, onEnded: resolve })) resolve();
        });
    })();
    try { await Promise.race([playback, new Promise(resolve => { timer = setTimeout(resolve, 7500); })]); }
    finally {
        finished = true;
        clearTimeout(timer);
        for (const entry of soundEngine.active) if (entry.family === id) soundEngine.retire(entry);
    }
}
export async function playClosingAnnouncement({ random = Math.random } = {}) {
    const settings = V()?.state.settings;
    const language = speechLanguage(settings, navigator.language);
    const id = "speech_" + language + "_" + closingClip(language, random);
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
    // Routing a suspended context is not playback readiness. Prepare it when
    // saved settings arrive and after device changes, before the first cue.
    // A blocked browser resume is retried by the trusted gesture below.
    void soundEngine.resume();
    void preloadSounds();
    return soundEngine.setOutput(V()?.state.settings?.playback_device_id, { retry: true });
}

export function initSounds() {
    void preloadSounds();
    void updateSoundOutput();
    const resume = (event) => {
        const userGesture = !!event?.isTrusted && ["pointerdown", "keydown"].includes(event.type);
        void soundEngine.resume({ userGesture });
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
