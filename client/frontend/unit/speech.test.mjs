import assert from "node:assert/strict";
import { test } from "node:test";
import { readFileSync, readdirSync } from "node:fs";
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

test("each fixed speech event has valid English/German PCM and no orphaned clips", () => {
    for (const language of ["en", "de"]) {
        assert.deepEqual(Object.keys(SPEECH_ASSETS[language]).sort(), Object.keys(SPEECH_EVENTS).sort());
        const directory = new URL("../src/assets/speech/"+language+"/",import.meta.url);
        assert.equal(readdirSync(directory).filter(f=>f.endsWith(".wav")).length, 9);
        for (const [event, clip] of Object.entries(SPEECH_ASSETS[language])) {
            const wav = readFileSync(new URL(event+".wav", directory));
            assert.equal(wav.toString("ascii",0,4),"RIFF"); assert.equal(wav.readUInt16LE(22),1);
            assert.ok([22050,24000,48000].includes(wav.readUInt32LE(24)));
            assert.equal(wav.readInt16LE(44),0); assert.equal(wav.readInt16LE(wav.length-2),0);
            assert.ok(clip.duration >= 1 && clip.duration <= 3);
            for (let i=44;i<wav.length;i+=2) assert.ok(Math.abs(wav.readInt16LE(i)/32768)<=.1151);
        }
    }
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
