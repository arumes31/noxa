import assert from "node:assert/strict";
import { test } from "node:test";
import { readFileSync, readdirSync } from "node:fs";
import { createHash } from "node:crypto";
import { SpeechQueue, SPEECH_EVENTS, speechLanguage } from "../src/speech-queue.js";
import { SPEECH_ASSETS } from "../src/speech-catalog.js";
import { SOUND_DEFINITIONS } from "../src/sound-catalog.js";
import { SPOKEN_ACTIONS, EFFECT_EVENTS } from "../src/notification-audio.js";

const NEW_ANNOUNCEMENTS = [
    "connection_connected", "connection_disconnected", "connection_reconnecting", "connection_failed", "server_error",
    "poke", "buddy_online", "channel_watch", "stream_watch_started",
];
const NOTIFICATION_ANNOUNCEMENTS = NEW_ANNOUNCEMENTS.slice(5);
const MESSAGE_EFFECTS = ["mention", "keyword", "dm", "channel_message", "whisper", "announcement"];
const SPOKEN_COOLDOWNS = {
    connection_connected: 100, connection_disconnected: 100, connection_reconnecting: 5000,
    connection_failed: 3000, disconnect_failed: 3000, server_error: 3000,
    poke: 5000, buddy_online: 5000, channel_watch: 10000, stream_watch_started: 3000,
};

function announcementFixture() {
    const f = fixture();
    // Policy tests use fixed asset metadata; the separate real catalog test
    // verifies authoring without masking queue regressions behind missing WAVs.
    f.queue.assets = Object.fromEntries(["en", "de"].map(language => [language,
        Object.fromEntries([...Object.keys(SPEECH_EVENTS), ...NEW_ANNOUNCEMENTS, "disconnect_failed"].map(event =>
            [event, { duration: 1, transcript: event }]))]));
    return f;
}

function fixture() {
    let time = 0, timer;
    const played = [], state = { settings: { spoken_messages: true, speech_volume: 80 }, activeTabID: "one" };
    let dnd = false;
    const engine = { active: new Set(), retire(entry) { this.active.delete(entry); },
        play(id, options) { played.push({ id, options }); this.active.add({ family:id }); return true; } };
    const queue = new SpeechQueue({ engine, assets:SPEECH_ASSETS, getState:()=>state, isDND:()=>dnd, now:()=>time,
        schedule:cb=>{ timer=cb; return 1; }, cancel:()=>{timer=null;} });
    return { queue, engine, state, played, dnd:value=>{dnd=value;}, advance:ms=>{time+=ms;},
        tick:ms=>{time+=ms;const cb=timer;timer=null;cb?.();} };
}

test("announcement language can override the interface without changing fallback behavior", () => {
    assert.equal(speechLanguage({ language: "de", speech_language: "en" }), "en");
    assert.equal(speechLanguage({ language: "en", speech_language: "de" }), "de");
    assert.equal(speechLanguage({ language: "system", speech_language: "interface" }, "de-AT"), "de");
    assert.equal(speechLanguage({ language: "de", speech_language: "invalid" }), "de");
    const f = fixture();
    f.state.settings.language = "de"; f.state.settings.speech_language = "en";
    f.queue.enqueue("banned", { delay: 0 });
    assert.equal(f.played[0].id, "speech_en_banned");
});

test("each fixed speech event has valid English/German PCM and no orphaned clips", () => {
    const metrics = JSON.parse(readFileSync(new URL("../src/assets/speech/metrics.json", import.meta.url)));
    const transcripts = JSON.parse(readFileSync(new URL("../../../tools/speech-lines.json", import.meta.url)));
    for (const language of ["en", "de"]) {
        assert.deepEqual(Object.keys(SPEECH_ASSETS[language]).sort(), Object.keys(SPEECH_EVENTS).sort());
        const directory = new URL("../src/assets/speech/"+language+"/",import.meta.url);
        assert.equal(readdirSync(directory).filter(f=>f.endsWith(".wav")).length, Object.keys(SPEECH_EVENTS).length);
        for (const [event, clip] of Object.entries(SPEECH_ASSETS[language])) {
            const wav = readFileSync(new URL(event+".wav", directory));
            assert.equal(wav.toString("ascii",0,4),"RIFF"); assert.equal(wav.readUInt16LE(22),1);
            assert.ok([22050,24000,48000].includes(wav.readUInt32LE(24)));
            assert.equal(wav.readInt16LE(44),0); assert.equal(wav.readInt16LE(wav.length-2),0);
            assert.ok(clip.duration >= .5 && clip.duration <= 6);
            assert.equal(clip.duration, (wav.length - 44) / 2 / wav.readUInt32LE(24));
            assert.equal(clip.transcript, transcripts[language][event]);
            assert.equal(clip.transcript, metrics[language + "/" + event].text);
            assert.equal(createHash("sha256").update(wav).digest("hex"), metrics[language + "/" + event].sha256);
            assert.ok(wav.subarray(44).some(byte => byte !== 0), "speech cannot be a silent placeholder");
            for (let i=44;i<wav.length;i+=2) assert.ok(Math.abs(wav.readInt16LE(i)/32768)<=.1151);
        }
    }
});

test("German announcements use the pinned AN06 reference offline and retain Channel wording and attribution", () => {
    const provenance = JSON.parse(readFileSync(new URL("../src/assets/speech/provenance.json", import.meta.url)));
    assert.equal(provenance.schema_version, 2);
    assert.equal(provenance.models.de, "Qwen/Qwen3-TTS-12Hz-1.7B-Base");
    assert.equal(provenance.providers.de.engine, "qwen3_tts");
    assert.equal(provenance.providers.de.revision, "e479ac197bae727f69574261a8025696bc51de48");
    assert.equal(provenance.providers.de.sha256, "b55e06c7890d43c208d15aed8b4ed3f18215f295e47d5960e061b15bff338ab0");
    assert.equal(provenance.authoring.runtime_generation, false);
    assert.equal(provenance.authoring.de.audition, "AN06");
    assert.equal(provenance.authoring.de.task, "tts");
    const reference = provenance.authoring.de.reference;
    assert.equal(reference.path, "tools/voices/AN06.wav");
    assert.equal(reference.sha256, "38f2ae669bbda13d21a6ad446fbbd97ac734729a83cd1633a6e0248fe847b35b");
    const referenceWav = readFileSync(new URL("../../../" + reference.path, import.meta.url));
    assert.equal(createHash("sha256").update(referenceWav).digest("hex"), reference.sha256);
    assert.ok(reference.text.includes("Mikrofon stummgeschaltet."));
    assert.equal(provenance.authoring.de.runtime.version, "v0.9.1");
    assert.equal(provenance.authoring.de.runtime.archive_sha256, "57a17896aba4447f088b27d2875e90a6563afd7860023d50da03a61fb143609f");
    assert.equal(SPEECH_ASSETS.de.user_join.transcript, "Benutzer ist dem Channel beigetreten.");
    for (const clip of Object.values(SPEECH_ASSETS.de)) assert.doesNotMatch(clip.transcript, /kanal/i);
    const notice = readFileSync(new URL("../public/noxa-audio-licenses.txt", import.meta.url), "utf8");
    assert.match(notice, /AN06/);
    assert.match(notice, /Qwen3-TTS/);
    assert.match(notice, /Apache-2\.0/);
});

test("every English announcement uses the exact selected EA60 reference with reviewed static output", () => {
    const root = new URL("../../../", import.meta.url);
    const provenance = JSON.parse(readFileSync(new URL("../src/assets/speech/provenance.json", import.meta.url)));
    assert.equal(provenance.models.en, "Qwen/Qwen3-TTS-12Hz-1.7B-Base");
    assert.deepEqual(provenance.providers.en, provenance.providers.de);
    assert.equal(provenance.authoring.runtime_generation, false);
    assert.equal(provenance.authoring.en.audition, "EA60");
    assert.equal(provenance.authoring.en.language, "English");
    const reference = provenance.authoring.en.reference;
    assert.equal(reference.path, "tools/voices/EA60.wav");
    assert.equal(reference.sha256, "4d016355ffef426b9b1bcb9a369c67bcaeaa8cbaa93d3ce34063c85e37aa6d5d");
    assert.equal(createHash("sha256").update(readFileSync(new URL(reference.path, root))).digest("hex"), reference.sha256);
    const original = JSON.parse(readFileSync(new URL(reference.provenance, root)));
    assert.equal(original.voice.id, "EA60");
    assert.equal(original.voice.sha256, reference.sha256);
    assert.equal(original.transcript, reference.text);
    assert.equal(original.voice.seed, 203085240);
    const metrics = JSON.parse(readFileSync(new URL("../src/assets/speech/metrics.json", import.meta.url)));
    const review = JSON.parse(readFileSync(new URL("../src/assets/speech/ea60-content-review.json", import.meta.url)));
    assert.deepEqual(Object.keys(review).sort(), Object.keys(SPEECH_ASSETS.en).sort());
    for (const [event, clip] of Object.entries(SPEECH_ASSETS.en)) {
        const metric = metrics["en/" + event];
        assert.equal(metric.voice, "EA60", event);
        assert.equal(metric.referenceSha256, reference.sha256, event);
        assert.equal(metric.modelSha256, provenance.providers.en.sha256, event);
        assert.equal(metric.sampleRate, 24000, event);
        assert.equal(review[event].sha256, metric.sha256, event);
        assert.equal(review[event].expected, clip.transcript, event);
        assert.equal(review[event].reviewStatus, "accepted-automatic-content-review", event);
    }
    const notice = readFileSync(new URL("../public/noxa-audio-licenses.txt", import.meta.url), "utf8");
    assert.match(notice, /EA60/);
    assert.match(notice, /Qwen3-TTS/);
    assert.match(notice, /MIT License/);
});

test("preview never interrupts a live critical announcement", () => {
    const f = fixture();
    f.queue.enqueue("banned", { delay: 0 });
    assert.equal(f.queue.enqueue("test", { preview: true, delay: 0 }), false);
    assert.equal(f.queue.current.event, "banned");
});

test("channel announcements use the selected cue, repeat after a short cooldown and yield to critical speech", () => {
    const f = fixture();
    f.state.settings.event_sounds = { user_join: false, user_move_in: true };
    assert.equal(f.queue.enqueue("user_join", { withEffect: true, effect: "user_move_in" }), true);
    assert.equal(f.played.length, 0);
    f.tick(150);
    assert.deepEqual(f.played.map(item => item.id), ["speech_en_user_join"]);
    f.played[0].options.onEnded(); f.tick(1000);
    assert.equal(f.queue.enqueue("user_join", { effect: "user_move_in", delay: 0 }), true);
    assert.equal(f.played.at(-1).id, "speech_en_user_join");
    f.queue.enqueue("banned", { delay: 0 });
    assert.equal(f.queue.current.event, "banned");
    assert.equal(f.queue.enqueue("user_leave", { delay: 0 }), false);
});

test("leaving voice and reconnecting speak in both languages and respect category preferences", () => {
    for (const event of ["channel_leave", "connection_reconnected"]) {
        for (const language of ["en", "de"]) {
            const f = fixture();
            f.state.settings.speech_language = language;
            assert.equal(f.queue.enqueue(event, { delay: 0 }), true);
            assert.equal(f.played[0].id, `speech_${language}_${event}`);
            f.queue.clear();
            f.state.settings[`speech_${SPEECH_EVENTS[event].category}`] = false;
            assert.equal(f.queue.allowed(event, f.state.settings, false), false);
        }
    }
});

test("channel announcement preferences and notification matrix suppress speech", () => {
    const f = fixture();
    assert.equal(f.queue.allowed("user_join", f.state.settings, false), true);
    f.state.settings.speech_events = { user_join: false };
    assert.equal(f.queue.allowed("user_join", f.state.settings, false), false);
    assert.equal(f.queue.allowed("user_leave", f.state.settings, false), true);
    f.state.settings.notify_matrix = { join_leave: { sound: false } };
    assert.equal(f.queue.allowed("user_leave", f.state.settings, false), false);
    delete f.state.settings.notify_matrix;
    f.state.settings.event_sounds = { user_move_out: false };
    assert.equal(f.queue.allowed("user_leave", f.state.settings, false, "user_move_out"), false);
    assert.equal(f.queue.allowed("user_moved_out", f.state.settings, false), false);
    delete f.state.settings.event_sounds;
    assert.equal(f.queue.allowed("user_moved_out", f.state.settings, false), true);
    f.state.settings.notify_matrix = { join_leave: { sound: false } };
    assert.equal(f.queue.allowed("user_moved_out", f.state.settings, false), false);
});

test("channel joins, forced moves and kicks have separate recordings and preferences", () => {
    for (const event of ["channel_join", "user_kicked", "user_kicked_channel", "moved_by_admin", "user_moved_out", "user_disconnected", "user_moved"]) {
        const f = fixture();
        assert.equal(f.queue.enqueue(event, { delay: 0 }), true);
        assert.equal(f.played[0].id, `speech_en_${event}`);
        f.queue.clear();
        f.state.settings.speech_events = { [event]: false };
        assert.equal(f.queue.allowed(event, f.state.settings, false), false);
    }
    const f = fixture();
    f.state.settings.speech_removal = false;
    assert.equal(f.queue.allowed("user_kicked", f.state.settings, false), false);
    assert.equal(f.queue.allowed("user_kicked_channel", f.state.settings, false), false);
    assert.equal(f.queue.allowed("channel_join", f.state.settings, false), true);
});

test("live alerts take precedence over previews both during the effect gap and during speech", () => {
    const f = fixture();
    f.queue.enqueue("kicked");
    assert.equal(f.queue.enqueue("banned", { preview: true, delay: 0 }), false);
    f.queue.clear();
    f.queue.enqueue("banned", { preview: true, delay: 0 });
    f.queue.enqueue("connection_lost", { delay: 0 });
    assert.equal(f.queue.current.event, "connection_lost");
});

test("speech cooldown suppresses the whole announcement without adding a beep", () => {
    const f = fixture();
    f.queue.enqueue("permission_denied", { delay: 0 });
    f.played[0].options.onEnded();
    f.tick(1000);
    assert.equal(f.queue.enqueue("permission_denied", { withEffect: true }), false);
    assert.deepEqual(f.played.map(item => item.id), ["speech_en_permission_denied"]);
    assert.equal(f.queue.pending.length, 0);
});

test("speech respects master, speech, event, matrix, DND and replay gates", () => {
    const f=fixture();
    for (const field of ["play_sounds", "spoken_messages", "speech_admin"]) {
        f.state.settings[field]=false; assert.equal(f.queue.enqueue("banned"),false); delete f.state.settings[field];
    }
    f.dnd(true); assert.equal(f.queue.enqueue("test",{preview:true}),false);f.dnd(false);
    f.state.replayingTabID="old";assert.equal(f.queue.enqueue("banned"),false);delete f.state.replayingTabID;
    f.state.settings.event_sounds={ban:false};assert.equal(f.queue.enqueue("banned"),false);delete f.state.settings.event_sounds;
    f.state.settings.speech_events={banned:false};assert.equal(f.queue.enqueue("banned"),false);delete f.state.settings.speech_events;
    f.state.settings.notify_matrix={kick:{sound:false}};assert.equal(f.queue.enqueue("banned"),false);
});

test("terminal speech supersedes obsolete messages, deduplicates, and uses fixed German assets", () => {
    const f=fixture();f.state.settings.language="de";
    f.queue.enqueue("connection_lost");f.queue.enqueue("reconnect_failed");f.queue.enqueue("banned");
    assert.equal(f.queue.pending.length,1);assert.equal(f.queue.enqueue("banned"),false);
    f.tick(400);assert.equal(f.played[0].id,"speech_de_banned");assert.equal(f.played[0].options.volume,80);
    assert.equal(f.queue.enqueue("permission_denied"),false);
    f.played[0].options.onEnded();assert.equal(f.queue.current,null);assert.equal(f.queue.pending.length,0);
    assert.equal(speechLanguage({language:"system"},"de-AT"),"de");assert.equal(speechLanguage({language:"fr"}),"en");
});

test("speech cancellation prevents stale playback and late ended callbacks cannot clear a successor", () => {
    const f=fixture();f.queue.enqueue("connection_lost",{delay:0});const old=f.played[0];
    f.queue.enqueue("banned",{delay:0});old.options.onEnded();assert.equal(f.queue.current.event,"banned");
    f.queue.clear();assert.equal(f.queue.current,null);assert.equal(f.queue.pending.length,0);
    f.tick(15000);f.queue.enqueue("connection_lost");f.queue.clear("connection");f.tick(500);assert.equal(f.played.length,2);
    f.queue.engine.play=()=>false;f.queue.enqueue("test",{preview:true,delay:0});assert.equal(f.queue.current,null);
    assert.equal(f.queue.enqueue("a dynamic reason or username"),false);
});

test("production frontend cannot reintroduce oscillator or speech synthesis", () => {
    const root=new URL("../src/",import.meta.url);
    for (const file of readdirSync(root).filter(f=>/\.[cm]?js$/.test(f))) {
        const source=readFileSync(new URL(file,root),"utf8");
        assert.doesNotMatch(source,/createOscillator|speechSynthesis|SpeechSynthesisUtterance|from\s+["'][^"']*(?:piper|generate-speech)/,file);
    }
});

test("distinct sessions do not share critical cooldowns and stale generations are discarded", () => {
    const f = fixture();
    f.state.serverGeneration = 1;
    assert.equal(f.queue.enqueue("banned"), true);
    f.state.activeTabID = "two";
    assert.equal(f.queue.enqueue("banned"), true);
    f.tick(200);
    assert.equal(f.played.length, 1);
    f.played[0].options.onEnded();
    f.state.serverGeneration++;
    assert.equal(f.queue.enqueue("banned"), true);
    f.queue.clear();
    f.queue.enqueue("connection_lost");
    f.state.serverGeneration++;
    f.tick(200);
    assert.equal(f.played.length, 1);
});

test("spoken alerts replace effects even when both sound categories are enabled", () => {
    const f = fixture();
    f.engine.definitions = { ban: { duration: .22 } };
    assert.equal(f.queue.enqueue("banned", { withEffect: true }), true);
    f.tick(149); assert.equal(f.played.length, 0);
    f.tick(1); assert.deepEqual(f.played.map(item => item.id), ["speech_en_banned"]);
    f.queue.clear();
    f.state.activeTabID = "two";
    f.state.settings.effects_enabled = false;
    f.engine.play = (id, options) => {
        if (id === "ban") return false;
        f.played.push({ id, options }); return true;
    };
    f.queue.enqueue("banned", { withEffect: true });
    f.tick(150);
    assert.equal(f.played.at(-1).id, "speech_en_banned");
});

test("explicit speech choices override retired effect toggles without bypassing the matrix", () => {
    const f = fixture();
    f.state.settings.event_sounds = { own_channel_join: false, user_join: false };
    assert.equal(f.queue.allowed("channel_join", f.state.settings, false), false);
    f.state.settings.speech_events = { channel_join: true, user_join: true };
    assert.equal(f.queue.allowed("channel_join", f.state.settings, false), true);
    assert.equal(f.queue.allowed("user_join", f.state.settings, false), true);
    f.state.settings.notify_matrix = { join_leave: { sound: false } };
    assert.equal(f.queue.allowed("user_join", f.state.settings, false), false);
});

test("mute and deafen announcements follow the latest state without stale or duplicate effects", () => {
    for (const [muted, unmuted] of [["microphone_muted", "microphone_unmuted"], ["sound_muted", "sound_unmuted"]]) {
        const f = fixture();
        assert.equal(f.queue.enqueue(muted, { delay: 0 }), true);
        const ended = f.played[0].options.onEnded;
        assert.equal(f.queue.enqueue(unmuted, { delay: 0 }), true);
        assert.equal(f.queue.current.event, unmuted);
        ended();
        assert.equal(f.queue.current.event, unmuted);
        assert.deepEqual(f.played.map(item => item.id), [`speech_en_${muted}`, `speech_en_${unmuted}`]);
        f.queue.clear();
        assert.equal(f.queue.enqueue(muted), true);
        assert.equal(f.queue.enqueue(unmuted), true);
        f.tick(150);
        assert.equal(f.queue.current.event, unmuted);
        assert.equal(f.queue.pending.length, 0);
    }
});

test("every spoken action stays silent when disabled or unavailable, without an effect fallback", () => {
    for (const event of Object.keys(SPEECH_EVENTS)) for (const language of ["en", "de"]) {
    for (const settings of [{ spoken_messages: false }, { speech_volume: 0 }, { speech_events: { [event]: false } }]) {
        const f = fixture();
        Object.assign(f.state.settings, settings, { speech_language: language });
        assert.equal(f.queue.enqueue(event, { withEffect: true }), false, event);
        f.tick(1000);
        assert.deepEqual(f.played, [], event);
    }
    const f = fixture();
    const play = f.engine.play;
    f.engine.play = (id, options) => id.startsWith("speech_") ? false : play.call(f.engine, id, options);
    f.state.settings.speech_language = language;
    f.queue.enqueue(event, { withEffect: true });
    f.tick(150);
    assert.deepEqual(f.played, [], event);
    }
});

test("rare action effects become fixed speech while messages and push-to-talk keep their short effects", () => {
    for (const event of NEW_ANNOUNCEMENTS) {
        assert.equal(SPOKEN_ACTIONS[event], event, event);
        assert.ok(SPEECH_EVENTS[event], event);
        assert.equal(SPEECH_EVENTS[event].priority, SOUND_DEFINITIONS[event].priority, event);
        assert.equal(SPEECH_EVENTS[event].cooldown, SPOKEN_COOLDOWNS[event], event);
    }
    assert.deepEqual(EFFECT_EVENTS.slice().sort(), [...MESSAGE_EFFECTS, "ptt_on", "ptt_off"].sort());
    for (const event of MESSAGE_EFFECTS) {
        assert.equal(SPOKEN_ACTIONS[event], undefined, event);
        assert.equal(SPEECH_EVENTS[event], undefined, event);
        assert.ok(EFFECT_EVENTS.includes(event), event);
    }
    assert.equal(SPEECH_EVENTS.ptt_on, undefined);
    assert.equal(SPEECH_EVENTS.ptt_off, undefined);
    assert.equal(SPEECH_EVENTS.disconnect_failed?.effect, "connection_failed");
    assert.equal(SPEECH_EVENTS.disconnect_failed?.category, "connection");
});

test("new announcements select both languages and preserve independent silence preferences", () => {
    for (const event of [...NEW_ANNOUNCEMENTS, "disconnect_failed"]) for (const language of ["en", "de"]) {
        const f = announcementFixture();
        Object.assign(f.state.settings, { speech_language: language, effects_enabled: false });
        assert.equal(f.queue.enqueue(event, { delay: 0 }), true, event);
        assert.deepEqual(f.played.map(item => item.id), [`speech_${language}_${event}`], event);
        f.queue.clear();
        f.state.settings.event_sounds = { [SPEECH_EVENTS[event].effect]: false };
        assert.equal(f.queue.allowed(event, f.state.settings, false), false, event + " legacy opt-out");
        f.state.settings.event_sounds = {};
        f.state.settings.speech_events = { [event]: false };
        assert.equal(f.queue.allowed(event, f.state.settings, false), false, event + " speech opt-out");
        f.state.settings.speech_events = {};
        if (SPEECH_EVENTS[event].category !== "notification") {
            f.state.settings["speech_" + SPEECH_EVENTS[event].category] = false;
            assert.equal(f.queue.allowed(event, f.state.settings, false), false, event + " category opt-out");
        }
    }
});

test("new notification speech respects its matrix row and stream notices have no invented row", () => {
    for (const event of NOTIFICATION_ANNOUNCEMENTS) {
        const f = announcementFixture();
        if (event === "stream_watch_started") {
            assert.equal(SPEECH_EVENTS[event]?.matrix, undefined);
            f.state.settings.notify_matrix = { stream_watch_started: { sound: false } };
            assert.equal(f.queue.allowed(event, f.state.settings, false), true, event);
        } else {
            assert.equal(SPEECH_EVENTS[event]?.matrix, event);
            f.state.settings.speech_events = { [event]: true };
            f.state.settings.notify_matrix = { [event]: { sound: false } };
            assert.equal(f.queue.enqueue(event, { delay: 0 }), false, event);
            assert.deepEqual(f.played, [], event);
        }
    }
});

test("missing German announcement assets and failed playback stay silent without English or effect fallback", () => {
    for (const event of [...NEW_ANNOUNCEMENTS, "disconnect_failed"]) {
        const f = announcementFixture();
        f.state.settings.speech_language = "de";
        delete f.queue.assets.de[event];
        assert.ok(f.queue.assets.en[event]);
        assert.equal(f.queue.enqueue(event, { delay: 0 }), false, event);
        assert.deepEqual(f.played, [], event);
        f.queue.assets.de[event] = { duration: 1, transcript: event };
        const attempted = [];
        f.queue.engine.play = id => { attempted.push(id); return false; };
        f.queue.enqueue(event, { delay: 0 });
        assert.deepEqual(attempted, [`speech_de_${event}`], event);
        assert.equal(f.queue.current, null, event);
        assert.equal(f.queue.pending.length, 0, event);
    }
});

test("pending notification speech rechecks DND, history and preferences before playback", () => {
    for (const block of [f => f.dnd(true), f => { f.state.replayingTabID = "history"; },
        f => { f.state.settings.play_sounds = false; }, f => { f.state.settings.spoken_messages = false; },
        f => { f.state.settings.speech_events = { poke: false }; },
        f => { f.state.settings.notify_matrix = { poke: { sound: false } }; }]) {
        const f = announcementFixture();
        assert.equal(f.queue.enqueue("poke"), true);
        block(f); f.tick(150);
        assert.deepEqual(f.played, []);
        assert.equal(f.queue.pending.length, 0);
    }
});

test("new routine announcement cooldowns suppress duplicates and expire at their defined boundary", () => {
    for (const event of [...NEW_ANNOUNCEMENTS, "disconnect_failed"].filter(event => event !== "connection_reconnecting")) {
        const f = announcementFixture();
        assert.equal(f.queue.enqueue(event, { delay: 0 }), true, event);
        f.played[0].options.onEnded();
        assert.equal(f.queue.enqueue(event, { delay: 0 }), false, event);
        f.tick(SPOKEN_COOLDOWNS[event] - 1);
        assert.equal(f.queue.enqueue(event, { delay: 0 }), false, event);
        f.tick(1);
        assert.equal(f.queue.enqueue(event, { delay: 0 }), true, event);
        assert.deepEqual(f.played.map(item => item.id), [`speech_en_${event}`, `speech_en_${event}`], event);
    }
});

test("reconnecting is spoken once per outage and a new outage or server scope can announce again", () => {
    const f = announcementFixture();
    assert.equal(f.queue.enqueue("connection_reconnecting", { delay: 0 }), true);
    f.played.at(-1).options.onEnded(); f.tick(11000);
    assert.equal(f.queue.enqueue("connection_reconnecting", { delay: 0 }), false);
    assert.equal(f.queue.enqueue("connection_lost", { delay: 0 }), true);
    f.played.at(-1).options.onEnded();
    assert.equal(f.queue.enqueue("connection_reconnecting", { delay: 0 }), true);
    f.played.at(-1).options.onEnded();
    f.state.activeTabID = "another-server";
    assert.equal(f.queue.enqueue("connection_reconnecting", { delay: 0 }), true);
});

test("connection outcomes discard pending and active reconnecting without stale ended callbacks", () => {
    for (const outcome of ["connection_reconnected", "connection_lost", "connection_failed", "connection_disconnected", "server_shutdown", "reconnect_failed"]) {
        for (const delay of [0, 500]) {
            const f = announcementFixture();
            assert.equal(f.queue.enqueue("connection_reconnecting", { delay }), true, outcome);
            const old = f.played[0];
            assert.equal(f.queue.enqueue(outcome, { delay: 0 }), true, outcome);
            assert.equal(f.queue.current.event, outcome, outcome);
            old?.options.onEnded();
            assert.equal(f.queue.current.event, outcome, outcome);
            f.played.at(-1).options.onEnded(); f.tick(1000);
            assert.equal(f.queue.pending.length, 0, outcome);
            assert.equal(f.played.filter(item => item.id.endsWith("_connection_reconnecting")).length, delay === 0 ? 1 : 0, outcome);
        }
    }
});

test("recovery replaces outdated connection loss while preserving an unrelated ban announcement", () => {
    for (const delay of [0, 500]) {
        const f = announcementFixture();
        assert.equal(f.queue.enqueue("connection_lost", { delay }), true);
        assert.equal(f.queue.enqueue("connection_reconnected", { delay: 0 }), true);
        assert.equal(f.queue.current.event, "connection_reconnected");
        f.played.at(-1).options.onEnded(); f.tick(1000);
        assert.equal(f.queue.current, null);
        assert.equal(f.queue.pending.length, 0);
    }
    const f = announcementFixture();
    f.queue.enqueue("banned", { delay: 0 });
    assert.equal(f.queue.enqueue("connection_reconnected", { delay: 0 }), false);
    assert.equal(f.queue.current.event, "banned");
});

test("a muted recovery still cancels obsolete connection announcements", () => {
    for (const delay of [0, 500]) {
        const f = announcementFixture();
        assert.equal(f.queue.enqueue("connection_lost", { delay }), true);
        f.state.settings.speech_events = { connection_reconnected: false };
        assert.equal(f.queue.enqueue("connection_reconnected", { delay: 0 }), false);
        f.tick(1000);
        assert.equal(f.queue.current, null);
        assert.equal(f.queue.pending.length, 0);
        assert.equal(f.played.filter(item => item.id.endsWith("_connection_reconnected")).length, 0);
    }
});

test("unexpected loss cannot replace a terminal shutdown and previews do not consume a live outage announcement", () => {
    const f = announcementFixture();
    f.queue.enqueue("server_shutdown", { delay: 0 });
    assert.equal(f.queue.enqueue("connection_lost", { delay: 0 }), false);
    assert.equal(f.queue.current.event, "server_shutdown");
    f.queue.clear();
    assert.equal(f.queue.enqueue("connection_reconnecting", { preview: true, delay: 0 }), true);
    f.queue.clear();
    assert.equal(f.queue.enqueue("connection_reconnecting", { delay: 0 }), true);
    assert.equal(f.queue.current.preview, false);
});

test("authoritative recovery invalidates ready loss before any preview cleanup can start it", () => {
    for (const muted of [true, false]) {
        const f = announcementFixture();
        f.queue.enqueue("connection_lost", { delay: 150 });
        // A native recovery event can arrive after the deadline but before the
        // timer callback. Old state must never start during enqueue cleanup.
        f.advance(200);
        f.state.settings.speech_events = { connection_reconnected: !muted };
        assert.equal(f.queue.enqueue("connection_reconnected", { delay: 0 }), !muted);
        assert.deepEqual(f.played.map(item => item.id), muted ? [] : ["speech_en_connection_reconnected"]);
        assert.equal(f.queue.pending.length, 0);
        assert.equal(f.queue.current?.event || null, muted ? null : "connection_reconnected");
    }
});

test("a cooldown-suppressed duplicate loss cannot rearm reconnect speech in the same outage", () => {
    const f = announcementFixture();
    f.queue.enqueue("connection_lost", { delay: 0 }); f.played.at(-1).options.onEnded();
    f.queue.enqueue("connection_reconnecting", { delay: 0 }); f.played.at(-1).options.onEnded();
    f.tick(6000);
    assert.equal(f.queue.enqueue("connection_lost", { delay: 0 }), false);
    assert.equal(f.queue.enqueue("connection_reconnecting", { delay: 0 }), false);
    assert.equal(f.played.filter(item => item.id === "speech_en_connection_reconnecting").length, 1);
});

test("a recovery during its own cooldown still invalidates pending and active loss", () => {
    for (const delay of [0, 150]) {
        const f = announcementFixture();
        f.queue.enqueue("connection_reconnected", { delay: 0 });
        f.played.at(-1).options.onEnded(); f.advance(200);
        assert.equal(f.queue.enqueue("connection_lost", { delay }), true);
        f.advance(200);
        assert.equal(f.queue.enqueue("connection_reconnected", { delay: 0 }), false);
        assert.deepEqual(f.played.map(item => item.id), delay === 0
            ? ["speech_en_connection_reconnected", "speech_en_connection_lost"]
            : ["speech_en_connection_reconnected"]);
        assert.equal(f.queue.current, null);
        assert.equal(f.queue.pending.length, 0);
    }
});

test("a new loss after recovery invalidates recovery even while its recording is cooling down", () => {
    for (const delay of [0, 150]) {
        const f = announcementFixture();
        f.queue.enqueue("connection_lost", { delay: 0 });
        f.played.at(-1).options.onEnded(); f.advance(200);
        assert.equal(f.queue.enqueue("connection_reconnected", { delay }), true);
        f.advance(200);
        // Audio anti-spam still suppresses the repeated loss clip, but the
        // newer loss state makes the intervening recovery obsolete.
        assert.equal(f.queue.enqueue("connection_lost", { delay: 0 }), false);
        assert.equal(f.queue.current, null, `recovery delay ${delay}`);
        assert.equal(f.queue.pending.length, 0, `recovery delay ${delay}`);
    }
});
