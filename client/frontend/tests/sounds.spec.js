import { test, expect } from "./fixtures.js";

const NEW_ANNOUNCEMENTS = ["connection_connected", "connection_disconnected", "connection_reconnecting", "connection_failed", "server_error", "poke", "buddy_online", "channel_watch", "stream_watch_started"];
const MESSAGE_EFFECTS = ["mention", "keyword", "dm", "channel_message", "whisper", "announcement"];

test.beforeEach(async ({ page }) => {
    // These tests exercise real Web Audio modules, not the Wails application.
    // Keep a same-origin document for asset imports without booting main.js.
    await page.route("**/__sounds_test__", route => route.fulfill({
        contentType: "text/html",
        body: "<!doctype html><html lang=\"en\"><title>Sound module test</title><body></body></html>",
    }));
    await page.goto("/__sounds_test__");
});

for (const language of ["en", "de"]) {
    test(`native close waits for the ${language} goodbye recording to finish`, async ({ page }) => {
        const result = await page.evaluate(async language => {
            window.__noxa = { state: { replayingTabID: "connecting-server", settings: { play_sounds: true, spoken_messages: true, speech_language: language, speech_volume: 75 } } };
            window.__noxaPolish = { dndActive: () => false };
            const { soundEngine, clearSpeech } = await import("/src/sounds.js");
            const { initClosingAudio } = await import("/src/closing-audio.js");
            const { SPEECH_ASSETS } = await import("/src/speech-catalog.js");
            let onClose, ready = false, completions = 0, ended = false, endedAtClose = false;
            const played = [];
            const play = soundEngine.play.bind(soundEngine);
            soundEngine.play = (id, options) => {
                const started = play(id, { ...options, onEnded: () => { ended = true; options.onEnded?.(); } });
                if (started) {
                    played.push(id);
                    clearSpeech(); // A tab replay/reset must not truncate goodbye.
                }
                return started;
            };
            initClosingAudio({
                runtime: { EventsOn(_name, callback) { onClose = callback; } },
                app: {
                    ReadyForCloseNotifications() { ready = typeof onClose === "function"; },
                    CompleteClose() { completions++; endedAtClose = ended; },
                },
            });
            const start = performance.now();
            const first = onClose(), second = onClose();
            const immediateCompletions = completions;
            await Promise.all([first, second]);
            const elapsed = performance.now() - start;
            const active = soundEngine.active.size;
            await soundEngine.dispose();
            return { ready, played, completions, immediateCompletions, endedAtClose, elapsed, active,
                duration: SPEECH_ASSETS[language].client_closing.duration };
        }, language);
        expect(result.ready).toBe(true);
        expect(result.played).toEqual([`speech_${language}_client_closing`]);
        expect(result.immediateCompletions).toBe(0);
        expect(result.completions).toBe(1);
        expect(result.endedAtClose).toBe(true);
        expect(result.active).toBe(0);
        expect(result.elapsed).toBeGreaterThanOrEqual(result.duration * 1000 - 100);
        expect(result.elapsed).toBeLessThan(4000);
    });
}

test("closing stays silent for notification preferences and acknowledges unavailable audio", async ({ page }) => {
    const result = await page.evaluate(async () => {
        window.__noxa = { state: { settings: {} } };
        let dnd = false;
        window.__noxaPolish = { dndActive: () => dnd };
        const { soundEngine } = await import("/src/sounds.js");
        const { initClosingAudio } = await import("/src/closing-audio.js");
        let completions = 0, plays = 0;
        const play = soundEngine.play.bind(soundEngine);
        soundEngine.play = (...args) => { const started = play(...args); if (started) plays++; return started; };
        const variants = [{ play_sounds: false }, { spoken_messages: false }, { speech_volume: 0 }, { speech_events: { client_closing: false } }, { dnd: true }, { unavailable: true }];
        for (const variant of variants) {
            window.__noxa.state.settings = { play_sounds: true, spoken_messages: true, speech_volume: 100, ...variant };
            dnd = !!variant.dnd;
            if (variant.unavailable) soundEngine.load = async () => { throw new Error("missing recording"); };
            let onClose;
            initClosingAudio({
                runtime: { EventsOn(_name, callback) { onClose = callback; } },
                app: { ReadyForCloseNotifications() {}, CompleteClose() { completions++; } },
            });
            await onClose();
        }
        await soundEngine.dispose();
        return { completions, plays };
    });
    expect(result).toEqual({ completions: 6, plays: 0 });
});

test("rejected previews cannot reroute a pending live announcement", async ({ page }) => {
    const result = await page.evaluate(async () => {
        window.__noxa = { state: { settings: { play_sounds: true, spoken_messages: true, sound_volume: 100 }, activeTabID: "one" } };
        window.__noxaPolish = { dndActive: () => false };
        const { soundEngine, speechQueue, previewSpeech, previewSounds } = await import("/src/sounds.js");
        await soundEngine.preload(); await soundEngine.resume();
        speechQueue.enqueue("kicked", { delay: 2000 });
        const routes = [];
        const original = soundEngine.setOutput.bind(soundEngine);
        soundEngine.setOutput = id => { routes.push(id); return original(id); };
        const draft = { ...window.__noxa.state.settings, playback_device_id: "draft-speakers" };
        await previewSpeech(draft, ["banned"]);
        await previewSounds(["ban"], draft);
        const pending = speechQueue.pending.map(item => item.event);
        speechQueue.clear(); await soundEngine.dispose();
        return { routes, pending };
    });
    expect(result.routes).toEqual([]);
    expect(result.pending).toEqual(["kicked"]);
});

for (const language of ["en", "de"]) {
    test(`preview cleanup cannot start stale ${language} connection loss before recovery`, async ({ page }) => {
        const result = await page.evaluate(async language => {
            const settings = { play_sounds: true, spoken_messages: true, speech_language: language,
                speech_volume: 100, effects_enabled: false, speech_events: {} };
            const state = { activeTabID: "recovery", settings };
            window.__noxa = { state };
            window.__noxaPolish = { dndActive: () => false };
            const { soundEngine, speechQueue, playAlert, stopPreviews } = await import("/src/sounds.js");
            await soundEngine.preload([`speech_${language}_connection_lost`, `speech_${language}_connection_reconnected`]);
            await soundEngine.resume();
            const played = [], play = soundEngine.play.bind(soundEngine);
            soundEngine.play = (id, options) => { const ok = play(id, options); if (ok) played.push(id); return ok; };
            let clock = 0; speechQueue.now = () => clock;
            const cases = {};
            for (const variant of ["muted", "enabled", "cooldown"]) {
                speechQueue.clear(); await new Promise(resolve => setTimeout(resolve, 30));
                state.activeTabID = variant; settings.speech_events = {}; clock = 0; played.length = 0;
                if (variant === "cooldown") {
                    playAlert("connection_reconnected", { delay: 0 });
                    speechQueue.clear(); await new Promise(resolve => setTimeout(resolve, 30));
                    clock = 200;
                }
                playAlert("connection_lost", { delay: 150 });
                // The event arrives after the queue deadline while JavaScript
                // still owns the turn, before the scheduled timer callback.
                clock += 200;
                settings.speech_events.connection_reconnected = variant !== "muted";
                const accepted = playAlert("connection_reconnected", { delay: 0 });
                await new Promise(resolve => setTimeout(resolve, 200));
                cases[variant] = { accepted, played: [...played], pending: speechQueue.pending.length,
                    current: speechQueue.current?.event || null };
            }
            speechQueue.clear(); await new Promise(resolve => setTimeout(resolve, 30));
            state.activeTabID = "live-timer"; clock = 0; played.length = 0;
            settings.speech_events = {};
            playAlert("connection_lost", { delay: 150 });
            clock = 200; stopPreviews();
            const immediate = [...played];
            await new Promise(resolve => setTimeout(resolve, 200));
            const afterTimer = [...played];
            speechQueue.clear(); await soundEngine.dispose();
            return { cases, immediate, afterTimer };
        }, language);
        const recovered = `speech_${language}_connection_reconnected`;
        expect(result.cases).toEqual({
            muted: { accepted: false, played: [], pending: 0, current: null },
            enabled: { accepted: true, played: [recovered], pending: 0, current: "connection_reconnected" },
            cooldown: { accepted: false, played: [recovered], pending: 0, current: null },
        });
        expect(result.immediate).toEqual([]);
        expect(result.afterTimer).toEqual([`speech_${language}_connection_lost`]);
    });
}

for (const language of ["en", "de"]) {
    test(`Test All plays every ${language} announcement once and no retired click`, async ({ page }) => {
        test.setTimeout(100_000);
        const result = await page.evaluate(async language => {
            const settings = { spoken_messages: true, speech_language: language, speech_volume: 100, sound_volume: 100 };
            window.__noxa = { state: { settings, activeTabID: "one" } };
            window.__noxaPolish = { dndActive: () => false };
            const { soundEngine, testAll, SPEECH_EVENTS, SOUND_EVENTS } = await import("/src/sounds.js");
            const { SPOKEN_ACTIONS } = await import("/src/notification-audio.js");
            const played = [], labels = [];
            const play = soundEngine.play.bind(soundEngine);
            soundEngine.play = (id, options) => { const started = play(id, options); if (started) played.push(id); return started; };
            await testAll(settings, label => labels.push(label));
            const retired = Object.keys(SPOKEN_ACTIONS).filter(id => soundEngine.definitions[id]);
            const active = soundEngine.active.size;
            await soundEngine.dispose();
            return { played, retired, active, labels, expected: [
                ...Object.keys(SPEECH_EVENTS).map(event => `speech_${language}_${event}`), ...SOUND_EVENTS,
            ] };
        }, language);
        expect(result.played).toEqual(result.expected);
        expect(result.retired).toEqual([]);
        expect(result.active).toBe(0);
        expect(result.labels.at(-1)).toBe("");
        expect(result.labels).not.toContain(undefined);
    });
}

test("legacy channel previews use speech and stopping Test All cancels the rest", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const settings = { spoken_messages: true, sound_volume: 100, speech_volume: 100 };
        window.__noxa = { state: { settings, activeTabID: "one" } };
        window.__noxaPolish = { dndActive: () => false };
        const { previewSounds, testAll, stopPreviews, soundEngine } = await import("/src/sounds.js");
        const played = [], play = soundEngine.play.bind(soundEngine);
        soundEngine.play = (id, options) => { const started = play(id, options); if (started) played.push(id); return started; };
        await previewSounds(["own_channel_join", "join_leave", "kick"], settings);
        const previews = [...played]; played.length = 0;
        await testAll(settings, label => { if (label) setTimeout(stopPreviews, 50); });
        await new Promise(resolve => setTimeout(resolve, 300));
        const active = soundEngine.active.size;
        await soundEngine.dispose();
        return { previews, played, active };
    });
    expect(result.previews).toEqual(["speech_en_channel_join", "speech_en_user_join", "speech_en_kicked"]);
    expect(result.played).toEqual(["speech_en_microphone_muted"]);
    expect(result.active).toBe(0);
});

test("microphone and deafen actions dispatch their spoken recordings, never click effects", async ({ page }) => {
    const result = await page.evaluate(async () => {
        window.__noxa = { state: { settings: { spoken_messages: true, speech_volume: 100, sound_volume: 100 } } };
        window.__noxaPolish = { dndActive: () => false };
        const { playEvent, soundEngine, speechQueue } = await import("/src/sounds.js");
        await soundEngine.preload(); await soundEngine.resume();
        const played = [], play = soundEngine.play.bind(soundEngine);
        soundEngine.play = (id, options) => { const started = play(id, options); if (started) played.push(id); return started; };
        for (const action of ["mic_off", "mic_on", "deafen_on", "deafen_off"]) {
            playEvent(action);
            await new Promise(resolve => setTimeout(resolve, 200));
            speechQueue.clear();
        }
        await soundEngine.dispose();
        return played;
    });
    expect(result).toEqual(["speech_en_microphone_muted", "speech_en_microphone_unmuted", "speech_en_sound_muted", "speech_en_sound_unmuted"]);
});

for (const language of ["en", "de"]) {
    test(`rare action dispatch plays only ${language} speech while message notifications retain effects`, async ({ page }) => {
        const result = await page.evaluate(async ({ language, actions, messages }) => {
            window.__noxa = { state: { activeTabID: "actions", settings: { play_sounds: true, spoken_messages: true,
                speech_language: language, speech_volume: 100, sound_volume: 100, effects_enabled: true } } };
            window.__noxaPolish = { dndActive: () => false };
            const { playEvent, soundEngine, speechQueue } = await import("/src/sounds.js");
            await soundEngine.preload(); await soundEngine.resume();
            const played = [], play = soundEngine.play.bind(soundEngine);
            soundEngine.play = (id, options) => { const ok = play(id, options); if (ok) played.push(id); return ok; };
            let clock = 0; speechQueue.now = () => clock;
            for (const action of actions) {
                // The independent effects switch cannot silence a speech action.
                window.__noxa.state.settings.effects_enabled = false;
                playEvent(action); clock += 150; speechQueue.pump();
                speechQueue.clear(); await new Promise(resolve => setTimeout(resolve, 30));
            }
            const announcements = played.splice(0);
            window.__noxa.state.settings.effects_enabled = true;
            window.__noxa.state.settings.spoken_messages = false;
            for (const message of messages) {
                playEvent(message); await new Promise(resolve => setTimeout(resolve, 300));
            }
            const effects = played.splice(0);
            window.__noxa.state.settings.effects_enabled = false;
            for (const message of messages) playEvent(message);
            const mutedEffects = [...played];
            const retired = actions.filter(id => Object.hasOwn(soundEngine.definitions, id));
            await soundEngine.dispose();
            return { announcements, effects, mutedEffects, retired };
        }, { language, actions: NEW_ANNOUNCEMENTS, messages: MESSAGE_EFFECTS });
        expect(result.announcements).toEqual(NEW_ANNOUNCEMENTS.map(event => `speech_${language}_${event}`));
        expect(result.effects).toEqual(MESSAGE_EFFECTS);
        expect(result.mutedEffects).toEqual([]);
        expect(result.retired).toEqual([]);
    });

    test(`rare ${language} notification speech obeys matrix, channel, DND and replay gates while history remains recorded`, async ({ page }) => {
        const result = await page.evaluate(async language => {
            const state = { activeTabID: "notifications", lastConnect: { addr: "matrix.example" }, settings: {
                play_sounds: true, spoken_messages: true, speech_language: language, speech_volume: 100, sound_volume: 100,
                effects_enabled: false, notify_matrix: {}, event_sounds: {}, speech_events: {}, channel_notify: {} } };
            window.__noxa = { state, toast() {}, announceLive() {} };
            let dnd = false;
            const recorded = [];
            window.__noxaPolish = { dndActive: () => dnd, recordNotification: (event, text) => recorded.push({ event, text }) };
            const { soundEngine, speechQueue } = await import("/src/sounds.js");
            const { notify } = await import("/src/notifications.js");
            await soundEngine.preload(); await soundEngine.resume();
            const played = [], play = soundEngine.play.bind(soundEngine);
            soundEngine.play = (id, options) => { const ok = play(id, options); if (ok) played.push(id); return ok; };
            let clock = 0; speechQueue.now = () => clock;
            const send = async (event, variant) => {
                // Fresh scopes prevent cooldowns from masking a broken policy gate.
                state.activeTabID = event + ":" + variant;
                notify(event, variant, { channelID: 7 });
                clock += 150; speechQueue.pump(); speechQueue.clear();
                await new Promise(resolve => setTimeout(resolve, 30));
            };
            for (const event of ["poke", "buddy_online", "channel_watch"]) {
                state.settings.notify_matrix[event] = { toast: false, sound: true, flash: false, native: false };
                await send(event, "enabled");
                state.settings.notify_matrix[event].sound = false; await send(event, "matrix muted");
                state.settings.notify_matrix[event].sound = true;
                state.settings.event_sounds[event] = false; await send(event, "legacy muted");
                delete state.settings.event_sounds[event];
                state.settings.speech_events[event] = false; await send(event, "speech muted");
                delete state.settings.speech_events[event];
                state.settings.channel_notify["matrix.example#7"] = { muted: true }; await send(event, "channel muted");
                state.settings.channel_notify = {};
                dnd = true; await send(event, "DND"); dnd = false;
                state.replayingTabID = "journal"; await send(event, "history"); state.replayingTabID = "";
                state.settings.spoken_messages = false; await send(event, "speech disabled"); state.settings.spoken_messages = true;
            }
            await soundEngine.dispose();
            return { played, recorded };
        }, language);
        expect(result.played).toEqual(["poke", "buddy_online", "channel_watch"].map(event => `speech_${language}_${event}`));
        expect(result.recorded).toHaveLength(24);
        for (const event of ["poke", "buddy_online", "channel_watch"]) {
            expect(result.recorded.filter(item => item.event === event).map(item => item.text)).toEqual([
                "enabled", "matrix muted", "legacy muted", "speech muted", "channel muted", "DND", "history", "speech disabled",
            ]);
        }
    });
}

test("a missing German speech WAV stays silent and retries the same recording without English or click fallback", async ({ page }) => {
    const result = await page.evaluate(async () => {
        window.__noxa = { state: { activeTabID: "retry", settings: { play_sounds: true, spoken_messages: true,
            speech_language: "de", speech_volume: 100, sound_volume: 100, effects_enabled: true } } };
        window.__noxaPolish = { dndActive: () => false };
        const { playEvent, soundEngine, speechQueue } = await import("/src/sounds.js");
        await soundEngine.preload(); await soundEngine.resume();
        const selected = "speech_de_poke", url = soundEngine.urls[selected], load = soundEngine.load;
        const englishAvailable = soundEngine.buffers.has("speech_en_poke");
        soundEngine.buffers.delete(selected);
        soundEngine.load = async candidate => { if (candidate === url) throw Error("missing German recording"); return load(candidate); };
        const attempted = [], played = [], play = soundEngine.play.bind(soundEngine);
        soundEngine.play = (id, options) => { attempted.push(id); const ok = play(id, options); if (ok) played.push(id); return ok; };
        let clock = 0; speechQueue.now = () => clock;
        playEvent("poke"); clock = 150; speechQueue.pump();
        await soundEngine.preload([selected]);
        const failed = { attempted: [...attempted], played: [...played], pending: speechQueue.pending.length, current: speechQueue.current };
        soundEngine.load = load; await soundEngine.preload([selected]);
        clock = 6000; playEvent("poke"); clock += 150; speechQueue.pump();
        const recovered = [...played];
        speechQueue.clear(); await soundEngine.dispose();
        return { englishAvailable, failed, recovered };
    });
    expect(result.englishAvailable).toBe(true);
    expect(result.failed).toEqual({ attempted: ["speech_de_poke"], played: [], pending: 0, current: null });
    expect(result.recovered).toEqual(["speech_de_poke"]);
});

test("notification previews retry a failed selected output and report missing assets", async ({ page }) => {
    const result = await page.evaluate(async () => {
        window.__noxa = { state: { settings: { play_sounds: true, sound_volume: 100, playback_device_id: "headset" } } };
        const { soundEngine, previewSounds, audioStatus } = await import("/src/sounds.js");
        const context = soundEngine.context();
        context.setSinkId = async () => { throw new Error("device temporarily unavailable"); };
        await soundEngine.preload();
        const before = soundEngine.outputState;
        const routes = [], played = [];
        context.setSinkId = async id => { routes.push(id); };
        const play = soundEngine.play.bind(soundEngine);
        soundEngine.play = (id, options) => { const ok = play(id, options); if (ok) played.push(id); return ok; };
        await previewSounds(["dm"]);
        const after = soundEngine.outputState;
        soundEngine.buffers.delete("dm");
        soundEngine.report("could not load dm");
        const reasons = audioStatus();
        await soundEngine.dispose();
        return { before, after, routes, played, reasons };
    });
    expect(result.before).toBe("unavailable");
    expect(result.after).toBe("ready");
    expect(result.routes).toEqual(["headset"]);
    expect(result.played).toEqual(["dm"]);
    expect(result.reasons).toContainEqual({ reason: "assets_failed", count: 1 });
});

test("window focus recovers live notification output without replaying missed sounds", async ({ page }) => {
    const result = await page.evaluate(async () => {
        window.__noxa = { state: { settings: { play_sounds: true, sound_volume: 100, playback_device_id: "headset" } } };
        const { soundEngine, initSounds, playEvent } = await import("/src/sounds.js");
        soundEngine.now = () => 0;
        const context = soundEngine.context();
        context.setSinkId = async () => { throw Error("temporary device failure"); };
        initSounds();
        await soundEngine.preload(); await soundEngine.resume();
        const before = soundEngine.outputState;
        const missed = playEvent("dm");
        const routes = [];
        context.setSinkId = async id => { routes.push(id); };
        soundEngine.now = () => 2000;
        window.dispatchEvent(new Event("focus"));
        await soundEngine.output;
        const after = soundEngine.outputState;
        const replayed = soundEngine.active.size;
        window.dispatchEvent(new Event("focus"));
        const readyDuringInteraction = soundEngine.outputReady;
        const played = playEvent("dm");
        await soundEngine.dispose();
        return { before, missed, after, replayed, routes, readyDuringInteraction, played };
    });
    expect(result).toEqual({ before: "unavailable", missed: false, after: "ready", replayed: 0,
        routes: ["headset"], readyDuringInteraction: true, played: true });
});

test("static speech decodes and frequent contact cues survive mandatory repetition", async ({ page }) => {
    test.setTimeout(120000);
    const result=await page.evaluate(async()=>{
        const {SoundEngine}=await import("/src/sound-engine.js");
        const {SOUND_DEFINITIONS}=await import("/src/sound-catalog.js");
        const {SPEECH_ASSETS}=await import("/src/speech-catalog.js");
        const engine=new SoundEngine({getState:()=>({settings:{sound_volume:100}}),isDND:()=>false,
            createContext:()=>new AudioContext({latencyHint:"interactive"}),load:async url=>(await fetch(url)).arrayBuffer()});
        await engine.preload();await engine.resume();
        let speech=0;
        for(const clips of Object.values(SPEECH_ASSETS))for(const clip of Object.values(clips)){
            await engine.ctx.decodeAudioData(await (await fetch(clip.url)).arrayBuffer());speech++;
        }
        const counts={};
        for(const [events,repetitions] of [[["ptt_on","ptt_off"],100],[["user_join"],50],[["user_leave"],50],[["channel_message"],50],[["mic_on","mic_off"],50],[["own_channel_switch"],30]]){
            for(let i=0;i<repetitions;i++)for(const event of events){
                if(!engine.play(event,{preview:true}))throw Error(event+" failed");
                counts[event]=(counts[event]||0)+1;
                await new Promise(resolve=>setTimeout(resolve,SOUND_DEFINITIONS[event].duration*1000+30));
            }
        }
        const active=engine.active.size,retiring=engine.retiring.size;
        const expectedSpeech=Object.values(SPEECH_ASSETS).reduce((total,clips)=>total+Object.keys(clips).length,0);
        const languages=Object.keys(SPEECH_ASSETS).sort();
        await engine.dispose();return {counts,active,retiring,speech,expectedSpeech,languages};
    });
    expect(result.languages).toEqual(["de","en"]);
    expect(result.speech).toBe(result.expectedSpeech);expect(result.active).toBe(0);expect(result.retiring).toBe(0);
    expect(result.counts).toEqual({ptt_on:100,ptt_off:100,user_join:50,user_leave:50,channel_message:50,mic_on:50,mic_off:50,own_channel_switch:30});
});

test("replacement sound set decodes, completes Test All, and releases all source nodes", async ({ page }) => {
    const result = await page.evaluate(async () => {
        window.__noxa = { state: { settings: { play_sounds: false, sound_volume: 100 } } };
        window.__noxaPolish = { dndActive: () => false };
        const { SoundEngine } = await import("/src/sound-engine.js");
        const { SOUND_EVENTS, SOUND_DEFINITIONS } = await import("/src/sound-catalog.js");
        const engine = new SoundEngine({
            getState: () => window.__noxa.state, isDND: () => false,
            createContext: () => new AudioContext({ latencyHint: "interactive" }),
            load: async url => (await fetch(url)).arrayBuffer(),
        });
        await engine.preload(); await engine.resume();
        const count = engine.buffers.size;
        const durations = Object.fromEntries([...engine.buffers].map(([name, buffer]) => [name, buffer.duration]));
        const ctx = engine.ctx;
        const source = ctx.createBufferSource.bind(ctx);
        let created = 0, ended = 0;
        ctx.createBufferSource = () => {
            const node = source(); created++;
            node.addEventListener("ended", () => ended++);
            return node;
        };
        for (const name of SOUND_EVENTS) {
            if (!engine.play(name, { preview: true })) throw Error("Preview failed: " + name);
            await new Promise(r => setTimeout(r, SOUND_DEFINITIONS[name].duration * 1000 + 60));
        }
        const active = engine.active.size;
        await engine.dispose();
        return { count, durations, active, created, ended, closed: ctx.state };
    });
    expect(result.count).toBe(33);
    expect(result.created).toBe(33);
    expect(result.ended).toBe(33);
    expect(result.active).toBe(0);
    expect(result.closed).toBe("closed");
    expect(result.durations.poke).toBeCloseTo(0.6, 4);
    expect(Math.max(...Object.entries(result.durations).filter(([name]) => name !== "poke").map(([, duration]) => duration))).toBeLessThan(.501);
});

test("four simultaneous cues at maximum gain retain headroom in real WebAudio", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const { SOUND_URLS } = await import("/src/sound-catalog.js");
        const peaks = [];
        for (const volume of [0, 1, 2]) {
            const ctx = new OfflineAudioContext(1, 48000, 48000);
            const gain = ctx.createGain(); gain.gain.value = volume; gain.connect(ctx.destination);
            const bytes = await (await fetch(SOUND_URLS.connection_lost)).arrayBuffer();
            const buffer = await ctx.decodeAudioData(bytes);
            for (let i = 0; i < 4; i++) {
                const source = ctx.createBufferSource(); source.buffer = buffer;
                source.connect(gain); source.start();
            }
            const rendered = await ctx.startRendering();
            peaks.push(rendered.getChannelData(0).reduce((max, sample) => Math.max(max, Math.abs(sample)), 0));
        }
        return peaks;
    });
    expect(result[0]).toBe(0);
    expect(result[1]).toBeGreaterThan(.1);
    expect(result[2]).toBeLessThanOrEqual(.921);
    expect(result[2]).toBeCloseTo(result[1] * 2, 4);
});
