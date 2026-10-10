import { expect, test } from "./fixtures.js";

// Use real WebRTC receivers: a synthetic local MediaStream bypasses the
// Chromium decoder-start issue and can pass while remote callers stay silent.
test("remote WebRTC audio reaches the processing graph", async ({ page }) => {
    await page.route("**/audio-check", route => route.fulfill({ contentType: "text/html", body: "<!doctype html><body>Audio check</body>" }));
    await page.goto("/audio-check");
    await page.locator("body").click({ position: { x: 5, y: 5 } });
    await page.evaluate(async () => {
        const { createRemoteAudioSource, createAudioLevelSampler } = await import("/src/audio.js");
        const sender = new RTCPeerConnection();
        const receiver = new RTCPeerConnection();
        sender.onicecandidate = e => { if (e.candidate) void receiver.addIceCandidate(e.candidate); };
        receiver.onicecandidate = e => { if (e.candidate) void sender.addIceCandidate(e.candidate); };
        const context = new AudioContext();
        // Drive the graph without playing the synthetic test tone on hardware.
        await context.setSinkId({ type: "none" });
        await context.resume();
        const oscillator = context.createOscillator();
        const volume = context.createGain();
        volume.gain.value = 0.05;
        const stream = context.createMediaStreamDestination();
        oscillator.connect(volume).connect(stream);
        oscillator.start();
        sender.addTrack(stream.stream.getAudioTracks()[0], stream.stream);
        window.__remoteAudioCheck = { context, sender, receiver, oscillator };
        receiver.ontrack = ({ track }) => {
            const source = createRemoteAudioSource(context, track);
            const analyser = context.createAnalyser();
            source.src.connect(analyser).connect(context.destination);
            Object.assign(window.__remoteAudioCheck, { ...source, analyser, sampleLevel: createAudioLevelSampler(context, source.src) });
        };
        const offer = await sender.createOffer();
        await sender.setLocalDescription(offer);
        await receiver.setRemoteDescription(offer);
        const answer = await receiver.createAnswer();
        await receiver.setLocalDescription(answer);
        await sender.setRemoteDescription(answer);
    });
    await expect.poll(() => page.evaluate(() => {
        const analyser = window.__remoteAudioCheck.analyser;
        if (!analyser) return 0;
        const samples = new Float32Array(analyser.fftSize);
        analyser.getFloatTimeDomainData(samples);
        return Math.sqrt(samples.reduce((sum, value) => sum + value * value, 0) / samples.length);
    })).toBeGreaterThan(0.01);
    expect(await page.evaluate(() => window.__remoteAudioCheck.playback.muted)).toBe(true);
    await expect.poll(() => page.evaluate(() => window.__remoteAudioCheck.sampleLevel())).toBeGreaterThan(0.01);
    await page.evaluate(async () => {
        const { sender, receiver, context, oscillator, playback, src } = window.__remoteAudioCheck;
        playback.pause();
        playback.srcObject = null;
        src.disconnect();
        oscillator.stop();
        sender.close();
        receiver.close();
        await context.close();
    });
});

test("advancing RTP automatically repairs one missing graph and its stale owner", async ({ page }) => {
    await page.route("**/audio-recovery", route => route.fulfill({ contentType:"text/html", body:"<!doctype html><button>Enable audio</button>" }));
    await page.goto("/audio-recovery");
    await page.locator("button").click();
    await page.evaluate(async () => {
        const { createRemoteAudio } = await import("/src/remote-audio.js");
        const { startRemoteMedia, reconcileRemoteMedia } = await import("/src/remote-media.js");
        const { createVoiceRecovery } = await import("/src/voice-recovery.js");
        const context = new AudioContext(); await context.setSinkId({type:"none"}); await context.resume();
        const sender = new RTCPeerConnection(), pc = new RTCPeerConnection();
        const state = { pc, activeTabID:"audio-test", serverGeneration:1, myChannelID:9, myClientID:"self", clients:[], trackUsers:new Map(), settings:{volume:80, muted_users:[], user_volumes:{}, voice_limiter:false} };
        window.__noxa = {state};
        const failures = [], oscillators = [], attachments = new Map();
        const audio = createRemoteAudio({ state, voiceEpoch:()=>1, toast: message=>failures.push(message), sysMsg: message=>failures.push(message) });
        for (let i=0; i<2; i++) {
            const oscillator = context.createOscillator(), gain = context.createGain(), stream = context.createMediaStreamDestination();
            gain.gain.value = .04; oscillator.frequency.value = 440+i*220;
            oscillator.connect(gain).connect(stream); oscillator.start(); oscillators.push(oscillator);
            const track = stream.stream.getAudioTracks()[0]; sender.addTrack(track,stream.stream);
            state.clients.push({client_id:track.id, unique_id:"account-"+i, channel_id:9});
        }
        sender.onicecandidate = e => { if (e.candidate) void pc.addIceCandidate(e.candidate); };
        pc.onicecandidate = e => { if (e.candidate) void sender.addIceCandidate(e.candidate); };
        const attached = (track,id) => audio.hasRemoteAudio(track) && state.trackUsers.get(track.id)?.track_id === id;
        const stop = startRemoteMedia(pc,()=>state.pc===pc,(track,id)=>{
            attachments.set(id,(attachments.get(id)||0)+1);
            state.trackUsers.set(track.id,{client_id:id,track_id:id});
            audio.attachRemoteAudio(track,state.clients.find(client=>client.client_id===id));
        },track=>{state.trackUsers.delete(track.id);audio.detachRemoteTrack(track.id);},attached);
        const recover = createVoiceRecovery({state,attached,repair:reconcileRemoteMedia,resync:async()=>{throw new Error("unnecessary renegotiation");},rebuild:async()=>{throw new Error("healthy transport rebuilt");}});
        const offer = await sender.createOffer(); await sender.setLocalDescription(offer); await pc.setRemoteDescription(offer);
        const answer = await pc.createAnswer(); await pc.setLocalDescription(answer); await sender.setRemoteDescription(answer);
        reconcileRemoteMedia(pc);
        await audio.remoteChain.ctx.setSinkId({type:"none"});
        window.__recovery = {state,pc,sender,context,audio,oscillators,stop,recover,attachments,failures};
    });
    await page.locator("button").click();
    const levels = () => page.evaluate(() => window.__recovery.pc.getReceivers().map(receiver=>window.__recovery.audio.readRemoteAudioLevel(receiver.track.id)));
    await expect.poll(async () => (await levels()).filter(level=>level>.01).length).toBe(2);
    const before = await page.evaluate(() => {
        const f=window.__recovery, track=f.pc.getReceivers()[0].track;
        f.audio.detachRemoteTrack(track.id);
        return [...f.attachments];
    });
    expect((await levels()).filter(level=>level===null)).toHaveLength(1);
    await expect.poll(async () => {
        await page.evaluate(async () => { const f=window.__recovery; await f.recover(f.pc,await f.pc.getStats()); });
        return (await levels()).filter(level=>level>.01).length;
    }).toBe(2);
    const repaired = await page.evaluate(() => [...window.__recovery.attachments]);
    expect(repaired.map(([,count],i)=>count-before[i][1]).sort()).toEqual([0,1]);
    // A live graph can survive while its owner metadata is lost during recovery.
    await page.evaluate(() => { const f=window.__recovery; f.state.trackUsers.delete(f.pc.getReceivers()[0].track.id); });
    await expect.poll(() => page.evaluate(async () => {
        const f=window.__recovery; await f.recover(f.pc,await f.pc.getStats());
        return f.state.trackUsers.size;
    })).toBe(2);
    await expect.poll(async () => (await levels()).filter(level=>level>.01).length).toBe(2);
    expect(await page.evaluate(() => window.__recovery.failures)).toEqual([]);
    await page.evaluate(async () => {
        const f=window.__recovery; f.state.pc=null; f.stop(); f.audio.detachRemoteAudio();
        f.oscillators.forEach(oscillator=>oscillator.stop()); f.pc.close(); f.sender.close(); await f.context.close();
    });
});
