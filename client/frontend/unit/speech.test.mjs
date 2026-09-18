import assert from "node:assert/strict";
import { test } from "node:test";
import { readFileSync, readdirSync } from "node:fs";
import { createHash } from "node:crypto";
import { SpeechQueue, SPEECH_EVENTS, speechLanguage } from "../src/speech-queue.js";
import { SPEECH_ASSETS } from "../src/speech-catalog.js";

function fixture() {
    let time = 0, timer;
    const played = [], state = { settings: { spoken_messages: true, speech_volume: 80 }, activeTabID: "one" };
    let dnd = false;
    const engine = { active: new Set(), retire(entry) { this.active.delete(entry); },
        play(id, options) { played.push({ id, options }); this.active.add({ family:id }); return true; } };
    const queue = new SpeechQueue({ engine, assets:SPEECH_ASSETS, getState:()=>state, isDND:()=>dnd, now:()=>time,
        schedule:cb=>{ timer=cb; return 1; }, cancel:()=>{timer=null;} });
    return { queue, engine, state, played, dnd:value=>{dnd=value;}, tick:ms=>{time+=ms;const cb=timer;timer=null;cb?.();} };
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
        assert.equal(readdirSync(directory).filter(f=>f.endsWith(".wav")).length, 9);
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

test("preview never interrupts a live critical announcement", () => {
    const f = fixture();
    f.queue.enqueue("banned", { delay: 0 });
    assert.equal(f.queue.enqueue("test", { preview: true, delay: 0 }), false);
    assert.equal(f.queue.current.event, "banned");
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

test("speech cooldown does not hide subsequent rejected-action effects", () => {
    const f = fixture();
    f.queue.enqueue("permission_denied", { delay: 0 });
    f.played[0].options.onEnded();
    f.tick(1000);
    assert.equal(f.queue.enqueue("permission_denied", { withEffect: true }), true);
    assert.equal(f.played.at(-1).id, "server_error");
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

test("effect completion, then a 150ms gap, gates speech; speech-only remains available", () => {
    const f = fixture();
    f.engine.definitions = { ban: { duration: .22 } };
    assert.equal(f.queue.enqueue("banned", { withEffect: true }), true);
    assert.equal(f.played[0].id, "ban");
    f.tick(1000);
    assert.equal(f.played.length, 1);
    f.played[0].options.onEnded();
    f.tick(149); assert.equal(f.played.length, 1);
    f.tick(1); assert.equal(f.played[1].id, "speech_en_banned");
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
