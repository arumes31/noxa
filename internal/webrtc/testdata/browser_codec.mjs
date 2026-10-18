import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { readFile } from 'node:fs/promises';
const require = createRequire(new URL('../../../client/frontend/package.json', import.meta.url));
const { chromium, expect } = require('@playwright/test');
const endpoint = process.argv[2];
const browser = await chromium.launch({headless:true});
const request = async (path,body) => {
    const response = await fetch(`${endpoint}/${path}`,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
    if(!response.ok) throw new Error(await response.text());
    return response.json();
};
try {
    const sender = await browser.newPage();
    const receiver = await browser.newPage();
    const legacy = await browser.newPage();
    for(const page of [sender,receiver,legacy]) {
        await page.goto('about:blank');
        await page.exposeFunction('request',request);
        await page.evaluate(() => {
            window.pc=new RTCPeerConnection({iceServers:[]});
            window.gather=()=>pc.iceGatheringState==='complete'?Promise.resolve():new Promise(resolve=>pc.addEventListener('icegatheringstatechange',()=>{if(pc.iceGatheringState==='complete')resolve();}));
        });
    }
    // Load the production module graph through a closed fixture origin so its
    // relative HEVC helper import is exercised without external network access.
    await sender.route('https://noxa-codec-fixture.invalid/**',async route=>{
        const name=new URL(route.request().url()).pathname.slice(1);
        if(!['video-codecs.js','video-hevc.js'].includes(name))return route.abort();
        await route.fulfill({status:200,contentType:'text/javascript',headers:{'Access-Control-Allow-Origin':'*'},
            body:await readFile(new URL(`../../../client/frontend/src/${name}`,import.meta.url),'utf8')});
    });
    await sender.addScriptTag({type:'module',content:'import {preferPublicationCodec} from "https://noxa-codec-fixture.invalid/video-codecs.js"; window.preferTestCodec=preferPublicationCodec;'});
    // Inline modules execute asynchronously; inserting the element is not a
    // readiness signal for the exported helper used by the next evaluation.
    await sender.waitForFunction(() => typeof window.preferTestCodec === 'function');
    await sender.evaluate(async () => {
        const canvas=document.createElement('canvas');canvas.width=1280;canvas.height=720;document.body.append(canvas);
        const context=canvas.getContext('2d');let frame=0;
        setInterval(()=>{context.fillStyle=`hsl(${frame++%360} 50% 40%)`;context.fillRect(0,0,1280,720);context.fillStyle='white';context.font='40px sans-serif';context.fillText(`Codec recovery ${frame}`,frame%700,200);},33);
        const track=canvas.captureStream(30).getVideoTracks()[0];
		const audio=new AudioContext();const oscillator=audio.createOscillator();const destination=audio.createMediaStreamDestination();
		oscillator.connect(destination);oscillator.start();await audio.resume();window.audioContext=audio;
		const audioTrack=destination.stream.getAudioTracks()[0];pc.addTransceiver(audioTrack,{direction:'sendonly',streams:[destination.stream]});
        window.transceiver=pc.addTransceiver(track,{direction:'sendonly',streams:[new MediaStream([track])],sendEncodings:[{maxBitrate:4000000}]});
        window.changeCodec=async mime=>{
            if(!preferTestCodec(transceiver,mime==='video/H264'?'h264':'vp8'))throw new Error(`Missing test codec ${mime}`);
            await pc.setLocalDescription(await pc.createOffer());await gather();
            await pc.setRemoteDescription(await request('offer',{id:'pub',sdp:pc.localDescription.sdp,tracks:[{track_id:track.id,slot:'screen'},{track_id:audioTrack.id,slot:'mic'}]}));
        };
        await changeCodec('video/H264');
    });
    for (const [page,id] of [[receiver,'sub'],[legacy,'legacy']]) await page.evaluate(id=>{
        pc.ontrack=event=>{const video=document.createElement('video');video.muted=true;video.autoplay=true;video.srcObject=new MediaStream([event.track]);document.body.append(video);};
        window.negotiate=async revision=>{
            await pc.setRemoteDescription(await request('viewer',{id,revision,codecs:id==='sub'?['h264','vp8']:[]}));
            if(id==='legacy')for(const transceiver of pc.getTransceivers())if(transceiver.receiver.track.kind==='video')transceiver.setCodecPreferences(RTCRtpReceiver.getCapabilities('video').codecs.filter(codec=>codec.mimeType==='video/VP8'));
            await pc.setLocalDescription(await pc.createAnswer());await gather();await request('answer',{id,sdp:pc.localDescription.sdp});
        };
        window.decoded=async mime=>{
            const stats=await pc.getStats();let frames=0;
            for(const stat of stats.values())if(stat.type==='inbound-rtp'&&stat.kind==='video'&&stats.get(stat.codecId)?.mimeType===mime)frames+=stat.framesDecoded||0;
            return frames;
        };
    },id);
    await expect.poll(()=>request('codec',{id:'pub'}),{timeout:15000}).toBe('video/H264');
    await receiver.evaluate(()=>negotiate(1));
    for(const [index,mime] of ['video/H264','video/VP8','video/H264'].entries()) {
        if(index) {
            if(index===1) {
                await legacy.evaluate(()=>negotiate(1));
                assert.equal(await legacy.evaluate(()=>pc.localDescription.sdp.includes('H264')),false);
            } else await request('watch',{id:'legacy',active:false,revision:2});
            const selected=await request('selected',{id:'pub'});
            assert.equal(selected,index===1?'vp8':'h264');
            await sender.evaluate(selected=>changeCodec(selected==='h264'?'video/H264':'video/VP8'),selected);
            await expect.poll(()=>request('codec',{id:'pub'}),{timeout:15000}).toBe(mime);
            await receiver.evaluate(revision=>negotiate(revision),index*100+1);
        }
        const before=await receiver.evaluate(mime=>decoded(mime),mime);
        await expect.poll(()=>receiver.evaluate(mime=>decoded(mime),mime),{timeout:15000}).toBeGreaterThan(before+20);
        assert.equal(await sender.evaluate(()=>pc.getSenders().filter(s=>s.track?.kind==='video').length),1);
        assert.equal(await sender.evaluate(()=>transceiver.sender.getParameters().encodings.length),1);
        assert.equal(await sender.evaluate(mime=>{
            const codecs=transceiver.sender.getParameters().codecs;
            const primary=codecs.find(c=>c.mimeType===mime);
            return codecs.some(c=>c.mimeType==='video/rtx'&&c.sdpFmtpLine===`apt=${primary?.payloadType}`);
        },mime),true,'sender codec switch lost RTX');
        assert.equal(await receiver.evaluate(async mime=>{
            const stats=await pc.getStats();
            const incoming=[...stats.values()].find(s=>s.type==='inbound-rtp'&&s.kind==='video'&&stats.get(s.codecId)?.mimeType===mime&&s.framesDecoded>0);
            return !!incoming&&pc.remoteDescription.sdp.includes(`a=ssrc-group:FID ${incoming.ssrc} `);
        },mime),true,'active receiver stream has no negotiated repair SSRC');
        console.log(`${mime}: decoded frames advanced with one sender encoding`);
        const repairBefore=await request('loss',{id:'sub'});
        await expect.poll(async()=>(await request('loss-state',{id:'sub'})).dropped,{timeout:5000}).toBe(repairBefore.dropped+1);
        await expect.poll(async()=>(await request('loss-state',{id:'sub'})).repaired,{timeout:5000}).toBeGreaterThan(repairBefore.repaired);
        const repairedFrames=await receiver.evaluate(mime=>decoded(mime),mime);
        await expect.poll(()=>receiver.evaluate(mime=>decoded(mime),mime),{timeout:5000}).toBeGreaterThan(repairedFrames+20);
        console.log(`${mime}: one lost SRTP media packet triggered RTX and decoding continued`);
        const ingressBefore=await request('ingress-loss',{id:'pub'});
        await expect.poll(async()=>(await request('ingress-loss-state',{id:'pub'})).dropped,{timeout:5000}).toBe(ingressBefore.dropped+1);
        await expect.poll(async()=>(await request('ingress-loss-state',{id:'pub'})).repaired,{timeout:5000}).toBeGreaterThan(ingressBefore.repaired);
        const ingressFrames=await receiver.evaluate(mime=>decoded(mime),mime);
        await expect.poll(()=>receiver.evaluate(mime=>decoded(mime),mime),{timeout:5000}).toBeGreaterThan(ingressFrames+20);
        console.log(`${mime}: sender repaired one lost upstream SRTP packet and decoding continued`);
        if(index===1) {
            const before=await legacy.evaluate(()=>decoded('video/VP8'));
            await expect.poll(()=>legacy.evaluate(()=>decoded('video/VP8')),{timeout:15000}).toBeGreaterThan(before+20);
            console.log('VP8-only late join negotiated successfully and both viewers decoded shared fallback');
        }
        if(index===0) {
            await request('watch',{id:'sub',active:false,revision:10,codecs:['h264','vp8']});
            await request('watch',{id:'sub',active:true,revision:11,codecs:['h264','vp8']});
            const paused=await receiver.evaluate(()=>decoded('video/H264'));
            await expect.poll(()=>receiver.evaluate(()=>decoded('video/H264')),{timeout:15000}).toBeGreaterThan(paused+20);
            console.log('H264 watch resume decoded a fresh keyframe');
            await receiver.evaluate(()=>{
                window.recoveryPeer=pc;
                window.recoveryState=async()=>{
                    const stats=await pc.getStats();
                    const active=new Set(pc.getTransceivers().filter(t=>t.receiver.track.kind==='video'&&['recvonly','sendrecv'].includes(t.currentDirection)).map(t=>t.mid));
                    const video=[...stats.values()].find(s=>s.type==='inbound-rtp'&&s.kind==='video'&&active.has(s.mid)&&s.framesDecoded>0);
                    const audio=[...stats.values()].find(s=>s.type==='inbound-rtp'&&s.kind==='audio'&&s.packetsReceived>0);
                    if(!video||!audio)throw new Error('Expected active video and audio before scoped recovery');
                    return {mid:video.mid,videoId:video.id,frames:video.framesDecoded,baseline:Object.fromEntries([...stats.values()].filter(s=>s.type==='inbound-rtp'&&s.kind==='video').map(s=>[s.id,s.framesDecoded||0])),audioId:audio.id,audioPackets:audio.packetsReceived,transceivers:pc.getTransceivers().length};
                };
            });
            const recoveryMids=[];
            let serverTransceivers;
            let recoveryGrowth;
            for(let cycle=0;cycle<12;cycle++) {
                const before=await receiver.evaluate(()=>recoveryState());
                const result=await request('receiver-reset',{id:'sub',revision:11+cycle});
                await receiver.evaluate(revision=>negotiate(revision),12+cycle);
                await expect.poll(()=>receiver.evaluate(async before=>{
                    const stats=await pc.getStats();
                    const active=new Set(pc.getTransceivers().filter(t=>t.receiver.track.kind==='video'&&['recvonly','sendrecv'].includes(t.currentDirection)).map(t=>t.mid));
                    return [...stats.values()].some(s=>s.type==='inbound-rtp'&&s.kind==='video'&&active.has(s.mid)&&s.mid!==before.mid&&s.id!==before.videoId&&s.framesDecoded>(before.baseline[s.id]||0)+20&&stats.get(s.codecId)?.mimeType==='video/H264');
                },before),{timeout:15000}).toBe(true);
                const after=await receiver.evaluate(()=>recoveryState());
                assert.equal(after.audioId,before.audioId,'receiver recovery replaced audio binding');
                assert.ok(after.audioPackets>before.audioPackets,'audio packets stopped during recovery');
                assert.equal(await receiver.evaluate(()=>pc===recoveryPeer&&pc.connectionState==='connected'),true);
                if(cycle===0){
                    serverTransceivers=result.transceivers;recoveryGrowth=result.growth;
                    // Prior codec/watch changes can leave a reusable video slot.
                    // Charge only actual growth, with at most one extra slot.
                    assert.equal(recoveryGrowth,serverTransceivers-before.transceivers,'recovery growth does not match new transceivers');
                    assert.ok(recoveryGrowth===0||recoveryGrowth===1,'first recovery grew beyond its alternating pair');
                }
                assert.equal(result.transceivers,serverTransceivers,'server transceivers grew after first reset');
                assert.equal(result.growth,recoveryGrowth,'transceiver reuse consumed recovery growth budget');
                assert.equal(after.transceivers,serverTransceivers,'browser/server transceiver count differs');
                recoveryMids.push(after.mid);
                if(cycle>1)assert.equal(after.mid,recoveryMids[cycle-2],'recovery did not recycle alternating MIDs');
                console.log(`H264 receiver reset ${cycle+1}: MID ${before.mid}->${after.mid}, fresh decoded frames ${after.frames}, transceivers ${result.transceivers}, same audio advanced`);
            }
            assert.equal(new Set(recoveryMids).size,2,'expected two reusable video MIDs');
        }
    }
} finally {await browser.close();}
