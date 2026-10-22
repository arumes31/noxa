import { createRequire } from "node:module";
import assert from "node:assert/strict";
const require = createRequire(new URL("../client/frontend/package.json", import.meta.url));
const { chromium } = require("@playwright/test");
const args = ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream", "--autoplay-policy=no-user-gesture-required"];
// Trust only this fixture's ephemeral key. Certificate rejection is tested
// separately with verifying Go TLS clients; never disable verification globally.
if (process.env.NOXA_TEST_TLS_PIN) args.push(`--ignore-certificate-errors-spki-list=${process.env.NOXA_TEST_TLS_PIN}`);
const browser = await chromium.launch({ args });
try {
    const page = await browser.newPage({ permissions: ["microphone", "camera"] });
    await page.goto(process.argv[2]);
    const result = await page.evaluate(async token => {
        const interval = window.setInterval;
        window.setInterval = (fn, ms) => interval(fn, ms === 15000 ? 100 : ms);
        const { maintainRelay } = await import("/module.js");
        const load = id => fetch(`/lease?id=${id}`, { headers: { "X-Test-Token": token } }).then(r => { if (!r.ok) throw Error("lease denied"); return r.json(); });
        const trace = [];
        const record = (event, details) => trace.push({ at: Math.round(performance.now()), event, ...details });
        const leases = [await load("a"), await load("b")];
        const pcs = leases.map(l => new RTCPeerConnection({ iceServers: l.ice_servers, iceTransportPolicy: "relay" }));
        const pendingCandidates = [[], []];
        const remoteVideos = [], frameTimes = new Map(), frameGaps = [];
        let measuring = false;
        for (let i = 0; i < 2; i++) {
            pcs[i].addEventListener("iceconnectionstatechange", () => record("ice", { peer: i, state: pcs[i].iceConnectionState }));
            pcs[i].onicecandidate = e => {
                if (!e.candidate) return;
                if (!pcs[1-i].remoteDescription) pendingCandidates[1-i].push(e.candidate);
                else void pcs[1-i].addIceCandidate(e.candidate).catch(() => {});
            };
            pcs[i].ontrack = e => {
                if (e.track.kind !== "video") return;
                const video = document.createElement("video"); video.autoplay = true; video.muted = true; video.srcObject = new MediaStream([e.track]); document.body.append(video); remoteVideos.push(video);
                const frame = now => { const last = frameTimes.get(video); if (measuring && last) frameGaps.push(now-last); frameTimes.set(video, now); video.requestVideoFrameCallback(frame); };
                video.requestVideoFrameCallback(frame);
            };
            const stream = await navigator.mediaDevices.getUserMedia({ audio: true, video: true });
            stream.getTracks().forEach(track => pcs[i].addTrack(track, stream));
            const canvas = document.createElement("canvas"); canvas.width = 320; canvas.height = 240;
            const context = canvas.getContext("2d"); let color = 0;
            interval(() => { context.fillStyle = `hsl(${color++%360} 100% 50%)`; context.fillRect(0,0,320,240); }, 50);
            const screen = canvas.captureStream(20); screen.getTracks().forEach(track => pcs[i].addTrack(track, screen));
        }
        let chain = Promise.resolve();
        const negotiate = i => {
            const task = chain.then(async () => {
                record("negotiate", { peer: i, generation: leases[i].generation });
                const pc=pcs[i], other=pcs[1-i];
                await pc.setLocalDescription(await pc.createOffer({ iceRestart: true }));
                await other.setRemoteDescription(pc.localDescription);
                await other.setLocalDescription(await other.createAnswer());
                await pc.setRemoteDescription(other.localDescription);
                for (let j=0;j<2;j++) for (const c of pendingCandidates[j].splice(0)) await pcs[j].addIceCandidate(c);
            }); chain = task.catch(error => record("negotiation-error", { peer: i, message: String(error) })); return task;
        };
        let lastData = 0, maxDataGap = 0, received = 0, sent = 0, expected = 0;
        const dc = pcs[0].createDataChannel("continuity");
        pcs[1].ondatachannel = e => { e.channel.onmessage = message => {
            const n = Number(message.data); if (n !== expected++) throw Error("data-channel sequence gap");
            const now = performance.now(); if (measuring && lastData) maxDataGap = Math.max(maxDataGap, now-lastData); lastData = now; received++;
        }; };
        await negotiate(0);
        const until = async (predicate, timeout=10000) => { const end=Date.now()+timeout;while(!predicate()){if(Date.now()>end)throw Error(`condition timeout: ${pcs.map(p=>`${p.connectionState}/${p.iceConnectionState}`).join(",")}, dc=${dc.readyState}, videos=${remoteVideos.length}`);await new Promise(r=>setTimeout(r,25));} };
        await until(() => dc.readyState === "open" && pcs.every(p => p.connectionState === "connected") && remoteVideos.length === 4);
        const sendTimer=interval(()=>{if(dc.readyState==="open")dc.send(String(sent++));},20);
        await new Promise(r=>setTimeout(r,1000)); measuring=true;
        const endpoints=[new Set(),new Set()], generations=[new Set(),new Set()];
        const audioCounts=[0,0], audioTimes=[performance.now(),performance.now()]; let maxAudioGap=0;
        const samplers=interval(async()=>{for(let i=0;i<2;i++){
            const stats=await pcs[i].getStats();
            const tr=[...stats.values()].find(s=>s.type==="transport"&&s.selectedCandidatePairId);
            const pair=tr&&stats.get(tr.selectedCandidatePairId), local=pair&&stats.get(pair.localCandidateId);
            if(local?.candidateType==="relay")endpoints[i].add(`${local.address}:${local.port}`);
            const packets=[...stats.values()].filter(s=>s.type==="inbound-rtp"&&s.kind==="audio").reduce((n,s)=>n+s.packetsReceived,0);
            if(packets>audioCounts[i]){const now=performance.now();maxAudioGap=Math.max(maxAudioGap,now-audioTimes[i]);audioTimes[i]=now;audioCounts[i]=packets;}
        }},25);
        const stops=pcs.map((pc,i)=>maintainRelay(pc,{
            app:{GetRelayLeaseForTab:async()=>{leases[i]=await load(i?"b":"a");if(!generations[i].has(leases[i].generation))record("lease",{peer:i,generation:leases[i].generation});generations[i].add(leases[i].generation);return leases[i];},GetICEServersForTab:async()=>leases[i].ice_servers},
            tabID:String(i),current:()=>true,restart:()=>negotiate(i),onFailure:()=>record("renewal-failure",{peer:i}),
        }));
        await new Promise(r=>setTimeout(r,14000));
        measuring=false;clearInterval(sendTimer);clearInterval(samplers);stops.forEach(stop=>stop());await new Promise(r=>setTimeout(r,100));
        const result={trace,maxDataGap,maxAudioGap,maxVideoGap:Math.max(...frameGaps),received,sent,endpoints:endpoints.map(e=>e.size),generations:generations.map(g=>g.size),audioCounts,connected:pcs.every(p=>p.connectionState==="connected")};
        pcs.forEach(p=>p.close());return result;
    }, process.env.NOXA_TEST_RELAY_TOKEN);
    console.log(JSON.stringify(result));
    assert.ok(result.connected, "connection lost");
    assert.equal(result.received,result.sent,"data loss");
    assert.ok(result.endpoints.every(n=>n>=3),"selected relay allocation did not migrate three times");
    assert.ok(result.generations.every(n=>n>=3),"credentials did not rotate");
    assert.ok(result.audioCounts.every(n=>n>100),"audio not received");
    assert.ok(result.maxDataGap<=500, "data gap exceeds 500 ms");
    assert.ok(result.maxAudioGap<=200, "audio gap exceeds 200 ms");
    assert.ok(result.maxVideoGap<=500, "video freeze exceeds 500 ms");
} finally { await browser.close(); }
