import test from "node:test";
import * as iceLadder from "../src/core/ice-ladder.js";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import vm from "node:vm";
import { Msg } from "../src/core/frame.js";
import * as gates from "../src/core/voice-gate.js";
import * as streamResume from "../src/core/stream-resume.js";

async function fixture(t, { captureTimeout = 30000 } = {}) {
    let acquire = async () => { throw new DOMException("Microphone busy", "NotReadableError"); };
    const peers = [], offers = [], bindings = [], tracks = [], replacements = [], handlers = {};
    let streamControl = async data => ({ action: data.action, session: "1", streams: [] });
    class Track extends EventTarget {
        constructor() { super(); this.id = `mic-${tracks.length}`; this.kind = "audio"; this.enabled = true; this.readyState = "live"; tracks.push(this); }
        stop() { this.readyState = "ended"; }
        clone() { throw new Error("no meter in this fixture"); }
    }
    class Peer {
        constructor() { peers.push(this); this.transceivers = []; this.connectionState = "connected"; }
        addTransceiver(track, options) {
            const sender = {track: typeof track === "string" ? null : track, replaceTrack: async track => { replacements.push(track); sender.track = track; }, getParameters: () => ({encodings:[{}]}), setParameters: async () => {}};
            const transceiver = {sender, receiver: {track: {kind:"audio", readyState:"live", id:"remote"}}, mid: "0", direction: options.direction, currentDirection: options.direction};
            this.transceivers.push(transceiver); return transceiver;
        }
        getTransceivers() { return this.transceivers; }
        getReceivers() { return []; }
        async createOffer() { return {type:"offer",sdp:"v=0"}; }
        async createAnswer() { return {type:"answer",sdp:"v=0"}; }
        async setLocalDescription(value) { this.localDescription=value; }
        async setRemoteDescription(value) { this.remoteDescription=value; for (const transceiver of this.transceivers) transceiver.currentDirection=transceiver.direction; }
        close() { this.connectionState="closed"; }
    }
    class Media {
        setRoster() {} async start() {} stop() {} peerConfig() { return {}; } protectPeer(pc) { return pc; }
        async bindSenders(_pc, slots) { bindings.push(slots); } async syncReceivers() {} publicationsChanged() {}
    }
    const values = {};
    const settings = {get: key => key === "activation" ? "continuous" : values[key], all: () => ({...values}), onChange: () => () => {}};
    const imports = {
        "./frame.js": {Msg}, "./settings.js": settings, "./video.js": {}, "./ice-ladder.js": iceLadder,
        "./voice-audio.js": {Mixer:class {close() {} detach() {}}, LevelMeter:class {}, installAudioUnlock() {}},
        "./voice-gate.js": gates,
        "./media-e2ee/media-session.js": {MediaSession:Media, MediaSessionError:class extends Error {}, MEDIA_E2EE_VERSION:"test-encrypted", MEDIA_E2EE_CODEC_VERSION:"test"},
        "./media-e2ee/media-keys.js": {MediaKeyStartupInbox:class {clear() {} drain() {}}},
        "./stream-resume.js": streamResume,
    };
    Object.assign(imports["./video.js"], {CameraSource:class {}, capSender() {}, preferReceiveCodecs() {}, watchCodecNames() {}, wireQuality() {}});
    const context=vm.createContext({EventTarget,CustomEvent,DOMException,performance,console,RTCPeerConnection:Peer,navigator:{mediaDevices:{getUserMedia:(...args)=>acquire(...args)}},setInterval:()=>1,clearInterval(){},setTimeout:(fn,ms,...args)=>setTimeout(fn,ms===30000?captureTimeout:ms,...args),clearTimeout});
    const module=new vm.SourceTextModule(await fs.readFile(new URL("../src/core/voice.js",import.meta.url),"utf8"),{context});
    await module.link(name => new vm.SyntheticModule(Object.keys(imports[name]),function(){for(const [key,value] of Object.entries(imports[name])) this.setExport(key,value);},{context}));
    await module.evaluate();
    const session=Object.assign(new EventTarget(), {clientID:"self",clients:()=>[],channels:()=>[],self:()=>null,relayServers:async()=>[],onRelayRenewal:()=>()=>{},conn:{open:true,on:(type,fn)=>{handlers[type]=fn;return ()=>{};},send(){},request:async(type,data)=>{
        if(type===Msg.WebRTCOffer) {offers.push(data);return {sdp:"v=0"};}if(type===Msg.VideoStreamControl)return streamControl(data);return {channel_id:data.channel_id};
    }}});
    const voice=new module.namespace.Voice(session,null,{media:{status:async()=>({supported:true,principal:"alice",device:"phone",clientID:"self"})}});
    t.after(()=>voice.close());
    return {voice, session, peers, offers, bindings, replacements, setStreamControl: fn => { streamControl = fn; },
        blockNegotiation: async () => {
            const pc=voice.pc, original=pc.setRemoteDescription.bind(pc);
            let entered, release;const started=new Promise(resolve=>{entered=resolve;});
            pc.setRemoteDescription=async value=>{if(value.type==="offer"){entered();await new Promise(resolve=>{release=resolve;});}await original(value);};
            const pending=handlers[Msg.WebRTCOffer]({sdp:"v=0"});await started;return async()=>{release();await pending;};
        }, setInput:id=>{values.microphoneID=id;}, setAcquire:fn=>{acquire=fn;}, stream:()=>{const track=new Track();return {getTracks:()=>[track],getAudioTracks:()=>[track]};}};
}

test("a listen-only join can restore its microphone on the same encrypted peer",async t=>{
    const f=await fixture(t);await f.voice.join(2);
    assert.equal(f.voice.microphoneAvailable,false);assert.equal(f.voice.transmitting,false);
    assert.equal(f.voice.microphoneError.name,"NotReadableError");
    f.voice.setMuted(true);const stream=f.stream();f.setAcquire(async()=>stream);
    assert.equal(await f.voice.retryMicrophone(),true);
    assert.equal(f.peers.length,1);assert.equal(f.offers.length,2);
    assert.equal(f.voice.microphoneAvailable,true);assert.equal(f.voice.microphoneError,null);
    assert.equal(stream.getAudioTracks()[0].enabled,false,"retry never unmutes the user");
    assert.equal(f.voice.transmitting,false);assert.equal(f.peers[0].transceivers[0].currentDirection,"sendrecv");
    assert.equal(f.bindings.at(-1)[0].slot,"mic");assert.ok(f.offers.every(offer=>offer.media_e2ee==="test-encrypted"));
    let changed=0;f.voice.addEventListener("state",()=>changed++);
    stream.getAudioTracks()[0].stop();stream.getAudioTracks()[0].dispatchEvent(new Event("ended"));
    assert.equal(f.voice.microphoneAvailable,false);assert.ok(changed>0);
});

test("overlapping microphone retries share capture and cannot attach to a replacement peer",async t=>{
    const f=await fixture(t);await f.voice.join(2);
    let release,calls=0;f.setAcquire(()=>{calls++;return new Promise(resolve=>{release=resolve;});});
    const first=f.voice.retryMicrophone(),second=f.voice.retryMicrophone();
    assert.equal(calls,1);
    await f.voice.leave();
    f.setAcquire(async()=>{throw new DOMException("denied","NotAllowedError");});
    await f.voice.join(3);const replacement=f.voice.pc;
    const late=f.stream();release(late);
    assert.equal(await first,false);assert.equal(await second,false);
    assert.equal(late.getAudioTracks()[0].readyState,"ended");assert.equal(f.voice.pc,replacement);assert.equal(f.voice.mic,null);
    assert.equal(f.voice.microphoneError.name,"NotAllowedError");
});

test("failed microphone replacement releases capture and keeps the previous input",async t=>{
    const f=await fixture(t);await f.voice.join(2);
    const old=f.stream();f.voice.mic=old;const stream=f.stream();f.setAcquire(async()=>stream);
    f.peers[0].transceivers[0].sender.replaceTrack=async()=>{throw new Error("replace refused");};
    await assert.rejects(f.voice.retryMicrophone(),/replace refused/);
    assert.equal(stream.getAudioTracks()[0].readyState,"ended");assert.equal(f.voice.mic,old);assert.equal(old.getAudioTracks()[0].readyState,"live");
});


test("a changed microphone choice is applied after an in-flight retry", async t=>{
    const f=await fixture(t);await f.voice.join(2);
    let release;const input=[];const firstStream=f.stream(),secondStream=f.stream();
    f.setAcquire(constraints=>{input.push(constraints.audio.deviceId?.exact||"default");return input.length===1?new Promise(resolve=>{release=()=>resolve(firstStream);}):Promise.resolve(secondStream);});
    const first=f.voice.retryMicrophone();f.setInput("new-device");const second=f.voice.retryMicrophone();
    assert.deepEqual(input,["default","new-device"],"the new device starts without waiting for the old browser prompt");
    assert.equal(await second,true);assert.equal(await first,false);release();await new Promise(resolve=>setTimeout(resolve,0));
    assert.equal(f.voice.mic,secondStream);assert.equal(firstStream.getAudioTracks()[0].readyState,"ended");
    assert.deepEqual(f.replacements,[secondStream.getAudioTracks()[0]],"the retired input never reaches replaceTrack");
});


for (const control of ["setMuted", "setDeafened"]) {
    test(`${control} during replaceTrack keeps the new microphone closed`,async t=>{
        const f=await fixture(t);await f.voice.join(2);
        const stream=f.stream();f.setAcquire(async()=>stream);
        let enter,release;const entered=new Promise(resolve=>{enter=resolve;});
        f.peers[0].transceivers[0].sender.replaceTrack=async track=>{
            assert.equal(track.enabled,false,"an unowned replacement starts closed");enter();await new Promise(resolve=>{release=resolve;});
        };
        const retry=f.voice.retryMicrophone();await entered;f.voice[control](true);release();
        assert.equal(await retry,true);assert.equal(stream.getAudioTracks()[0].enabled,false);assert.equal(f.voice.transmitting,false);
    });
}


test("a timed-out permission prompt is retired and its eventual stream is stopped",async t=>{
    const f=await fixture(t,{captureTimeout:20});await f.voice.join(2);
    let late;f.setAcquire(()=>new Promise(resolve=>{late=resolve;}));
    await assert.rejects(f.voice.retryMicrophone(),error=>error.name==="TimeoutError");
    assert.equal(f.voice.microphoneError.name,"TimeoutError");
    const old=f.stream(),current=f.stream();f.setAcquire(async()=>current);
    assert.equal(await f.voice.retryMicrophone(),true);late(old);await new Promise(resolve=>setTimeout(resolve,0));
    assert.equal(old.getAudioTracks()[0].readyState,"ended");assert.equal(f.voice.mic,current);
});

test("a queued captured input is stopped immediately on leave",async t=>{
    const f=await fixture(t);await f.voice.join(2);const release=await f.blockNegotiation();
    const stream=f.stream();f.setAcquire(async()=>stream);const retry=f.voice.retryMicrophone();
    await new Promise(resolve=>setTimeout(resolve,0));assert.equal(stream.getAudioTracks()[0].enabled,false);
    await f.voice.leave();assert.equal(stream.getAudioTracks()[0].readyState,"ended","leaving must not wait for negotiation timeout");
    assert.equal(await retry,false);await release();assert.equal(f.replacements.length,0);
});

test("the serialized install discards a superseded configuration",async t=>{
    const f=await fixture(t);await f.voice.join(2);const release=await f.blockNegotiation();
    const a=f.stream(),b=f.stream();let calls=0;f.setAcquire(async()=>++calls===1?a:b);
    const first=f.voice.retryMicrophone();await new Promise(resolve=>setTimeout(resolve,0));
    f.setInput("new-device");const second=f.voice.retryMicrophone();await new Promise(resolve=>setTimeout(resolve,0));
    assert.equal(a.getAudioTracks()[0].readyState,"ended");assert.equal(b.getAudioTracks()[0].enabled,false);
    await release();assert.equal(await first,false);assert.equal(await second,true);
    assert.deepEqual(f.replacements,[b.getAudioTracks()[0]]);
});


test("a live but detached input is never reported as transmitting",async t=>{
    const f=await fixture(t);await f.voice.join(2);const stream=f.stream();f.setAcquire(async()=>stream);
    assert.equal(await f.voice.retryMicrophone(),true);assert.equal(f.voice.microphoneAvailable,true);
    f.peers[0].transceivers[0].sender.track=null;
    assert.equal(stream.getAudioTracks()[0].readyState,"live");assert.equal(f.voice.microphoneAvailable,false);assert.equal(f.voice.transmitting,false);
});

test("a resume hint refreshes a still selected watch to the republished source", async t => {
    const f = await fixture(t);
    const watches = [];
    f.setStreamControl(async data => {
        if (data.action === "watch") { watches.push(data); return { action: "watch", session: "1" }; }
        return { action: "list", session: "1", streams: [{ publisher_id: "pub", slot: "screen", generation: "2" }] };
    });
    f.voice.watching.add("pub|screen");
    f.voice.watched.set("pub|screen", { publisher_id: "pub", slot: "screen", generation: "1" });
    f.session.dispatchEvent(new CustomEvent("server-event", { detail: { type: "stream_watch_resume", data: { publisher_id: "pub", slot: "screen" } } }));
    await f.voice.streams();
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.equal(watches.length, 1);
    assert.equal(watches[0].generation, "2");
    assert.equal(String(f.voice.watched.get("pub|screen").generation), "2");
    await f.voice.streams();
    assert.equal(watches.length, 1, "the hint is used once");
});

test("a failed automatic watch rolls back and retries a bounded number of times", async t => {
    const f = await fixture(t);
    let attempts = 0;
    f.setStreamControl(async data => {
        if (data.action === "watch") { attempts++; throw new Error("busy"); }
        return { action: "list", session: "1", streams: [{ publisher_id: "pub", slot: "cam", generation: "4" }] };
    });
    f.session.dispatchEvent(new CustomEvent("server-event", { detail: { type: "stream_watch_resume", data: { publisher_id: "pub", slot: "cam" } } }));
    for (let i = 0; i < 5; i++) {
        await f.voice.streams();
        await new Promise(resolve => setTimeout(resolve, 0));
        assert.equal(f.voice.watching.has("pub|cam"), false, "no optimistic state survives a failure");
    }
    assert.equal(attempts, 3);
});
