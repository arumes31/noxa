import { test } from "node:test";
import assert from "node:assert/strict";
import { MediaE2EE } from "../src/media-e2ee.js";

for (const failedOperation of ["worker stats", "sender stats", "proof"]) {
    test(`repeated ${failedOperation} rejection stops only the current screen capture`, async t => {
        let clock = 1, stopped = 0, ended = 0, error = "";
        t.mock.method(performance, "now", () => clock);
        class Worker {
            postMessage({seq}) { queueMicrotask(() => this.onmessage?.({data:{seq,ok:true,value:{gen:1}}})); }
            terminate() {}
        }
        const scope = {Worker, crypto:globalThis.crypto, RTCRtpSender:{prototype:{createEncodedStreams(){}}}, RTCRtpReceiver:{prototype:{createEncodedStreams(){}}}};
        const media = new MediaE2EE({api:"streams",scope,workerURL:"fake",onHardwareUnavailable:e => {error=e.message;}});
        await media.ready;
        const track = {id:"screen",kind:"video",readyState:"live",stop(){stopped++;this.readyState="ended";},dispatchEvent(){ended++;}};
        const sender = {track, createEncodedStreams:()=>({readable:{},writable:{}}), getParameters:()=>({encodings:[{active:true}]}),
            getStats:async()=>{if(failedOperation==="sender stats")throw Error("unavailable"); return new Map();}};
        media.protectSender(sender,"video");
        media.stats = async()=>{if(failedOperation==="worker stats")throw Error("unavailable");return {endpoint:{activeEpoch:1}};};
        await media.bind(sender,"self|screen");
        if(failedOperation==="proof") {
            const request=media.request.bind(media);
            media.request=(op,...args)=>op==="hardwareProof"?Promise.reject(Error("unavailable")):request(op,...args);
        }
        const poll=media.hardwarePollers.get(media.endpointID(sender));
        try {
            await poll(); assert.equal(stopped,0);
            clock=11002; await poll();
            assert.equal(stopped,1);assert.equal(ended,1);assert.equal(error,"hardware_unverified");
        } finally {media.close();}
    });
}

test("a late rejected hardware poll cannot stop its replacement capture", async t => {
    let clock=1, reject, stopped=0;
    t.mock.method(performance,"now",()=>clock);
    class Worker {postMessage({seq}){queueMicrotask(()=>this.onmessage?.({data:{seq,ok:true,value:{gen:1}}}));}terminate(){}}
    const media=new MediaE2EE({api:"streams",scope:{Worker,crypto:globalThis.crypto,RTCRtpSender:{prototype:{createEncodedStreams(){}}},RTCRtpReceiver:{prototype:{createEncodedStreams(){}}}},workerURL:"fake"});
    await media.ready;
    const track={id:"old",kind:"video",readyState:"live",stop(){stopped++;}};
    const sender={track,createEncodedStreams:()=>({readable:{},writable:{}}),getParameters:()=>({encodings:[{active:true}]})};
    media.protectSender(sender,"video"); await media.bind(sender,"self|screen");
    media.stats=()=>new Promise((_,r)=>{reject=r;});
    try {
        const pending=media.hardwarePollers.get(media.endpointID(sender))();
        sender.track={...track,id:"new"};clock=20000;reject(Error("late"));await pending;
        assert.equal(stopped,0);
    } finally {media.close();}
});


test("a 130-transceiver replacement bounds detach requests below worker capacity", async () => {
    let inFlight=0, peak=0;
    class Worker {
        postMessage({seq}) {
            if(seq===undefined)return;
            peak=Math.max(peak,++inFlight);
            queueMicrotask(()=>{inFlight--;this.onmessage?.({data:{seq,ok:true,value:{gen:1}}});});
        }
        terminate(){}
    }
    const media=new MediaE2EE({api:"streams",hardwareVideoRequired:false,scope:{Worker,crypto:globalThis.crypto,RTCRtpSender:{prototype:{createEncodedStreams(){}}},RTCRtpReceiver:{prototype:{createEncodedStreams(){}}}},workerURL:"fake"});
    await media.ready;
    const endpoint=()=>({track:{kind:"audio"},createEncodedStreams:()=>({readable:{},writable:{}})});
    const transceivers=Array.from({length:130},()=>({sender:endpoint(),receiver:endpoint()}));
    for(const tr of transceivers)media.protectTransceiver(tr,"audio");
    try {
        await media.releasePeer({getTransceivers:()=>transceivers});
        assert.equal(media.closed,false);assert.ok(peak<=8,`peak ${peak}`);assert.equal(media.pending.size,0);
        assert.ok(transceivers.every(tr=>!media.endpointID(tr.sender)&&!media.endpointID(tr.receiver)));
    }finally{media.close();}
});


function replacementFixture() {
    const log=[], replies=[];
    let hold=false;
    class Worker {
        postMessage(message) {
            const {seq,op}=message;if(seq===undefined)return;
            log.push([op,message.pid,message.sourceTag]);
            const reply=()=>this.onmessage?.({data:{seq,ok:true,value:{gen:1}}});
            if(hold&&op==="bind"&&message.pid==="")replies.push(reply);else queueMicrotask(reply);
        }
        terminate(){}
    }
    const media=new MediaE2EE({api:"streams",scope:{Worker,crypto:globalThis.crypto,RTCRtpSender:{prototype:{createEncodedStreams(){}}},RTCRtpReceiver:{prototype:{createEncodedStreams(){}}}},workerURL:"fake"});
    const sender={track:{id:"old",kind:"video",readyState:"live"},createEncodedStreams:()=>({readable:{},writable:{}}),
        getParameters:()=>({encodings:[{active:false}]}),async replaceTrack(track){log.push(["replace",track?.id]);if(this.reject)throw Error("replacement rejected");this.track=track;}};
    media.protectSender(sender,"video");
    return {media,sender,log,replies,hold:()=>{hold=true;}};
}

for(const reject of [false,true])test(`source replacement waits for revocation and revalidates ${reject?"the previous":"the new"} source`,async()=>{
    const f=replacementFixture();await f.media.ready;await f.media.bind(f.sender,"self|screen");
    f.hold();f.sender.reject=reject;
    try {
        const next={id:"new",kind:"video",readyState:"live"};
        const pending=f.sender.replaceTrack(next);
        for(let i=0;i<10;i++)await Promise.resolve();
        assert.equal(f.replies.length,1);assert.equal(f.sender.track.id,"old");assert.equal(f.log.some(x=>x[0]==="replace"),false);
        f.replies.shift()();
        if(reject)await assert.rejects(pending,/replacement rejected/);else await pending;
        assert.equal(f.sender.track.id,reject?"old":"new");
        const binds=f.log.filter(x=>x[0]==="bind");
        assert.deepEqual(binds.map(x=>x.slice(1)),[["self|screen","old"],["","old"],["self|screen",reject?"old":"new"]]);
    }finally{f.media.close();}
});

test("detachment during revocation prevents a late native source replacement",async()=>{
    const f=replacementFixture();await f.media.ready;await f.media.bind(f.sender,"self|screen");f.hold();
    try {
        const pending=f.sender.replaceTrack({id:"new",kind:"video"});
        for(let i=0;i<10;i++)await Promise.resolve();
        await f.media.releasePeer({getTransceivers:()=>[{sender:f.sender}]});
        f.replies.shift()();await assert.rejects(pending,/endpoint ended/);
        assert.equal(f.log.some(x=>x[0]==="replace"),false);
    }finally{f.media.close();}
});

test("concurrent replacements and binds are serialized; null and camera replacements remain supported",async()=>{
    const f=replacementFixture();await f.media.ready;await f.media.bind(f.sender,"self|screen");
    try {
        await Promise.all([f.sender.replaceTrack({id:"new",kind:"video"}),f.media.bind(f.sender,"self|screen"),f.sender.replaceTrack(null)]);
        assert.equal(f.sender.track,null);assert.equal(f.media.boundPid(f.sender),"self|screen");
        await f.media.bind(f.sender,"self|cam");f.log.length=0;
        await f.sender.replaceTrack({id:"camera",kind:"video"});
        assert.deepEqual(f.log,[["replace","camera"]]);
    }finally{f.media.close();}
});

test("a hung native stats call times out, stops capture, and never accumulates native requests",async t=>{
    const f=replacementFixture();await f.media.ready;await f.media.bind(f.sender,"self|screen");
    let clock=1,calls=0,stopped=0,resolve;
    t.mock.method(performance,"now",()=>clock);
    f.sender.getParameters=()=>({encodings:[{active:true}]});
    f.sender.track.stop=()=>{stopped++;f.sender.track.readyState="ended";};
    f.sender.getStats=()=>{calls++;return new Promise(r=>{resolve=r;});};
    f.media.stats=async()=>({endpoint:{activeEpoch:1}});
    t.mock.timers.enable({apis:["setTimeout"]});
    try {
        const poll=f.media.hardwarePollers.get(f.media.endpointID(f.sender));
        const pending=poll();for(let i=0;i<10;i++)await Promise.resolve();
        clock=6000;t.mock.timers.tick(5000);await pending;assert.equal(stopped,0);
        clock=12000;await poll();assert.equal(stopped,1);assert.equal(calls,1);
        resolve(new Map());await Promise.resolve();assert.equal(stopped,1);
    }finally{f.media.close();}
});


for(const failedPid of ["","self|screen"])test(`a failed source ${failedPid?"rebind":"revocation"} can retry and enforce the capture deadline`,async t=>{
    const f=replacementFixture();await f.media.ready;await f.media.bind(f.sender,"self|screen");
    const request=f.media.request.bind(f.media);let failed=false,clock=1,stopped=0,calls=0;
    f.media.request=(op,data,...rest)=>{
        if(op==="bind"&&data.pid===failedPid&&!failed){failed=true;return Promise.reject(Error("bind failed"));}
        return request(op,data,...rest);
    };
    t.mock.method(performance,"now",()=>clock);
    try {
        await assert.rejects(f.sender.replaceTrack({id:"new",kind:"video",readyState:"live"}),/bind failed/);
        if(!failedPid)assert.equal(f.log.some(x=>x[0]==="replace"),false);
        await f.media.bind(f.sender,"self|screen");
        f.sender.getParameters=()=>({encodings:[{active:true}]});
        f.sender.track.stop=()=>{stopped++;f.sender.track.readyState="ended";};
        f.sender.getStats=async()=>{calls++;return new Map();};
        f.media.stats=async()=>({endpoint:{activeEpoch:1}});
        const poll=f.media.hardwarePollers.get(f.media.endpointID(f.sender));
        await poll();clock=20000;await poll();
        assert.equal(calls,1,"an expired deadline stops before issuing another stats request");assert.equal(stopped,1);
    }finally{f.media.close();}
});

async function hardwarePollFixture(t) {
    let clock = 1, evidence = "hardware", stopped = 0;
    const proofs = [], counters = {};
    t.mock.method(performance, "now", () => clock);
    t.mock.timers.enable({ apis: ["setTimeout"] });
    class Worker {
        postMessage({ seq, op, enabled }) {
            if (op === "hardwareProof") proofs.push(enabled);
            if (seq !== undefined) queueMicrotask(() => this.onmessage?.({ data: { seq, ok: true, value: { gen: 1 } } }));
        }
        terminate() {}
    }
    const media = new MediaE2EE({ api: "streams", workerURL: "fake", scope: {
        Worker, crypto: globalThis.crypto, RTCRtpSender: { prototype: { createEncodedStreams() {} } },
        RTCRtpReceiver: { prototype: { createEncodedStreams() {} } },
    } });
    await media.ready;
    const track = { id: "proven-screen", kind: "video", readyState: "live",
        stop() { stopped++; this.readyState = "ended"; }, dispatchEvent() {} };
    const sender = { track, createEncodedStreams: () => ({ readable: {}, writable: {} }),
        getParameters: () => ({ encodings: [{ active: true }] }),
        getStats: async () => evidence === "pending" ? new Map() : new Map([
            ["source", { trackIdentifier: sender.track.id, frames: counters.frames }],
            ["video", { id: counters.id ?? "video", type: "outbound-rtp", kind: "video", mediaSourceId: "source", framesEncoded: counters.encoded,
                powerEfficientEncoder: evidence === "hardware", encoderImplementation: evidence === "hardware" ? "Intel QSV MFT" : "OpenH264" }],
        ]),
    };
    media.protectSender(sender, "video");
    media.stats = async () => ({ endpoint: { activeEpoch: 1 } });
    await media.bind(sender, "self|screen");
    t.after(() => media.close());
    return { media, sender, track, proofs, counters, stopped: () => stopped,
        async sample(at, value) { clock = at; evidence = value; await media.hardwarePollers.get(media.endpointID(sender))?.(); } };
}

test("a hardware encoder that stops encoding captured frames loses its proof", async t => {
    const f = await hardwarePollFixture(t);
    Object.assign(f.counters, { encoded: 10, frames: 100 });
    await f.sample(1, "hardware");
    Object.assign(f.counters, { frames: 200 });
    await f.sample(5001, "hardware");
    assert.equal(f.stopped(), 0);
    Object.assign(f.counters, { frames: 300 });
    await f.sample(15002, "hardware");
    assert.equal(f.stopped(), 1, "a stalled encoder cannot keep the share alive");
});

test("an uplink outage that pauses encoding keeps the hardware proof", async t => {
    const f = await hardwarePollFixture(t);
    f.sender.transport = { iceTransport: { state: "connected" } };
    Object.assign(f.counters, { encoded: 10, frames: 100 });
    await f.sample(1, "hardware");
    f.sender.transport.iceTransport.state = "disconnected";
    for (const [at, frames] of [[5001, 200], [15002, 300], [30003, 400]]) {
        f.counters.frames = frames;
        await f.sample(at, "hardware");
    }
    assert.equal(f.stopped(), 0, "the browser does not encode while ICE is disconnected");
});

test("a new outbound row whose counter restarted is not a stalled encoder", async t => {
    const f = await hardwarePollFixture(t);
    Object.assign(f.counters, { encoded: 500, frames: 100 });
    await f.sample(1, "hardware");
    Object.assign(f.counters, { id: "video-new-ssrc", encoded: 5, frames: 200 });
    await f.sample(5001, "hardware");
    Object.assign(f.counters, { encoded: 50, frames: 300 });
    await f.sample(15002, "hardware");
    assert.equal(f.stopped(), 0);
});

test("a static source that delivers no new frames keeps its hardware proof", async t => {
    const still = await hardwarePollFixture(t);
    Object.assign(still.counters, { encoded: 10, frames: 100 });
    for (const at of [1, 5001, 15002, 30003]) await still.sample(at, "hardware");
    assert.equal(still.stopped(), 0, "no new captured frames means nothing to encode");
    still.counters.frames = 101;
    await still.sample(30304, "hardware");
    for (const at of [35004, 45005, 60006]) await still.sample(at, "hardware");
    assert.equal(still.stopped(), 0, "one stray captured frame cannot end a later static share");
});

test("a missing active key epoch does not renew the hardware deadline", async t => {
    const f = await hardwarePollFixture(t);
    f.media.stats = async () => ({ endpoint: { activeEpoch: 0 } });
    await f.sample(1, "hardware");
    await f.sample(10002, "hardware");
    assert.equal(f.stopped(), 1);
});

test("an established hardware stream gets a fresh deadline after a transient missing report", async t => {
    const f = await hardwarePollFixture(t);
    await f.sample(1, "hardware");
    await f.sample(120001, "hardware");
    await f.sample(120301, "pending");
    assert.equal(f.stopped(), 0, "stream age must not make one missing report fatal");
    assert.equal(f.proofs.at(-1), false, "missing evidence immediately revokes upload permission");
    await f.sample(130302, "pending");
    assert.equal(f.stopped(), 1, "persistent missing evidence still stops capture");
});

test("hardware fallback preserves the selected window through a bounded recovery opportunity", async t => {
    const f = await hardwarePollFixture(t);
    await f.sample(1, "hardware");
    await f.sample(60001, "software");
    assert.equal(f.stopped(), 0);
    assert.equal(f.proofs.at(-1), false, "software fallback never receives hardware authorization");
    await f.sample(75001, "software");
    assert.equal(f.stopped(), 0, "the 10-second sender recovery scheduler must have time to run");
    await f.sample(80002, "software");
    assert.equal(f.stopped(), 1, "a failed recovery cannot retain capture indefinitely");
});

test("a recovered hardware encoder keeps the original capture and renews proof", async t => {
    const f = await hardwarePollFixture(t);
    await f.sample(1, "hardware");
    await f.sample(60001, "software");
    await f.sample(75001, "hardware");
    assert.equal(f.sender.track, f.track);
    assert.equal(f.track.readyState, "live");
    assert.equal(f.stopped(), 0);
    assert.deepEqual(f.proofs, [true, false, true]);
    await f.sample(75002, "pending");
    await f.sample(85003, "pending");
    assert.equal(f.stopped(), 1, "the unknown-proof deadline applies again after recovery");
});

test("a source that never proved hardware still rejects software immediately", async t => {
    const f = await hardwarePollFixture(t);
    await f.sample(1, "software");
    assert.equal(f.proofs.at(-1), false);
    assert.equal(f.stopped(), 1);
});

test("a replacement source cannot inherit the previous window's hardware recovery grace", async t => {
    const f = await hardwarePollFixture(t);
    await f.sample(1, "hardware");
    f.sender.track = { ...f.track, id: "new-window" };
    await f.media.bind(f.sender, "self|screen");
    await f.sample(60001, "software");
    assert.equal(f.stopped(), 1);
    assert.equal(f.track.readyState, "live", "the previous source is not stopped by a late poll");
    assert.equal(f.sender.track.readyState, "ended");
    assert.equal(f.proofs.at(-1), false);
});

for (const outcome of ["pending", "rejected stats", "rejected proof"]) test(`hardware fallback grace survives ${outcome} while the encoder restarts`, async t => {
    const f = await hardwarePollFixture(t);
    await f.sample(1, "hardware");
    await f.sample(60001, "software");
    if (outcome === "rejected stats") f.sender.getStats = async () => { throw Error("encoder restarting"); };
    if (outcome === "rejected proof") {
        const request = f.media.request.bind(f.media);
        f.media.request = (op, ...args) => op === "hardwareProof" ? Promise.reject(Error("worker busy")) : request(op, ...args);
    }
    await f.sample(75001, "pending");
    assert.equal(f.stopped(), 0, "an inconclusive poll must not shorten an admitted fallback retry");
    assert.equal(f.proofs.at(-1), false);
    await f.sample(80002, "pending");
    assert.equal(f.stopped(), 1, "inconclusive retries still have a bounded deadline");
});


test("first software evidence retains recovery grace when its proof revocation rejects", async t => {
    const f = await hardwarePollFixture(t);
    await f.sample(1, "hardware");
    const request = f.media.request.bind(f.media), attempted = [];
    f.media.request = (op, data, ...args) => {
        if (op !== "hardwareProof") return request(op, data, ...args);
        attempted.push(data.enabled);
        return Promise.reject(Error("worker busy on first revocation"));
    };
    await f.sample(60001, "software");
    await f.sample(75001, "pending");
    assert.equal(f.stopped(), 0, "the sampled fallback owns the window even if its first revocation fails");
    assert.deepEqual(attempted, [false, false], "failed revocations must never extend positive proof");
    await f.sample(80002, "pending");
    assert.equal(f.stopped(), 1, "failed revocations remain bounded by the recovery deadline");
});

async function hungHardwareFixture(t, grace, failedOperation) {
    t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: 1 });
    t.mock.method(performance, "now", () => Date.now());
    let evidence = grace === 20000 ? "hardware" : "pending", hold = false, calls = 0, stopped = 0;
    const held = [];
    class Worker {
        postMessage(message) {
            if (message.seq === undefined) return;
            const reply = () => this.onmessage?.({ data: { seq: message.seq, ok: true, value: { gen: 1, endpoint: { activeEpoch: 1 } } } });
            if (hold && message.op === (failedOperation === "worker stats" ? "stats" : "hardwareProof")) {
                calls++; held.push(reply);
            } else queueMicrotask(reply);
        }
        terminate() {}
    }
    const media = new MediaE2EE({ api: "streams", workerURL: "fake", scope: {
        Worker, crypto: globalThis.crypto, RTCRtpSender: { prototype: { createEncodedStreams() {} } },
        RTCRtpReceiver: { prototype: { createEncodedStreams() {} } },
    } });
    await media.ready;
    const track = { id: "deadline-screen", kind: "video", readyState: "live",
        stop() { stopped++; this.readyState = "ended"; }, dispatchEvent() {} };
    const sender = { track, createEncodedStreams: () => ({ readable: {}, writable: {} }),
        getParameters: () => ({ encodings: [{ active: true }] }),
        getStats: () => {
            if (hold && failedOperation === "sender stats") { calls++; return new Promise(() => {}); }
            return Promise.resolve(evidence === "pending" ? new Map() : new Map([
                ["source", { trackIdentifier: sender.track.id }],
                ["video", { type: "outbound-rtp", kind: "video", mediaSourceId: "source", powerEfficientEncoder: evidence === "hardware", encoderImplementation: evidence === "hardware" ? "Intel QSV MFT" : "OpenH264" }],
            ]));
        },
    };
    media.protectSender(sender, "video"); await media.bind(sender, "self|screen");
    const poll = media.hardwarePollers.get(media.endpointID(sender));
    if (grace === 20000) await poll();
    evidence = grace === 20000 ? "software" : "pending";
    await poll();
    evidence = "pending"; hold = true;
    const flush = async () => { for (let i = 0; i < 30; i++) await Promise.resolve(); };
    t.after(() => media.close());
    return { media, sender, held, calls: () => calls, stopped: () => stopped, flush, evidence: value => { evidence = value; } };
}

for (const grace of [10000, 20000]) for (const operation of ["worker stats", "sender stats", "proof"]) {
    test(grace + "ms capture deadline expires while " + operation + " is genuinely pending", async t => {
        const f = await hungHardwareFixture(t, grace, operation);
        t.mock.timers.tick(grace - 100); await f.flush();
        assert.equal(f.calls(), 1, "exactly one request may remain in flight");
        assert.equal(f.stopped(), 0);
        t.mock.timers.tick(100); await f.flush();
        assert.equal(f.stopped(), 1, "capture stop must not await request timeout");
        assert.equal(f.sender.track.readyState, "ended");
        for (const reply of f.held.splice(0)) reply();
        await f.flush(); assert.equal(f.stopped(), 1, "late completion cannot stop twice");
    });
}

for (const teardown of ["rebind", "detach", "close"]) test("pending proof deadline is cleaned on " + teardown, async t => {
    const f = await hungHardwareFixture(t, 20000, "proof");
    t.mock.timers.tick(19900); await f.flush();
    assert.equal(f.calls(), 1);
    if (teardown === "rebind") await f.media.bind(f.sender, "self|cam");
    else if (teardown === "detach") await f.media.releasePeer({ getTransceivers: () => [{ sender: f.sender }] });
    else f.media.close();
    assert.equal(f.media.hardwareTimers.size, 0, "owner teardown removes both poll and deadline timers");
    t.mock.timers.tick(100); await f.flush();
    assert.equal(f.stopped(), 0, "retired deadline cannot stop the capture");
    for (const reply of f.held.splice(0)) reply();
    await f.flush();
    assert.equal(f.media.hardwareTimers.size, 0, "late completion cannot revive an old watcher");
});

test("late old proof cannot stop or unschedule a replacement source watcher", async t => {
    const f = await hungHardwareFixture(t, 20000, "proof");
    t.mock.timers.tick(19900); await f.flush();
    const oldPoll = f.media.hardwarePollers.get(f.media.endpointID(f.sender));
    let replacementStopped = 0;
    f.sender.track = { ...f.sender.track, id: "replacement-screen", stop() { replacementStopped++; } };
    await f.media.bind(f.sender, "self|screen");
    const nextPoll = f.media.hardwarePollers.get(f.media.endpointID(f.sender));
    assert.notEqual(nextPoll, oldPoll);
    t.mock.timers.tick(100); await f.flush();
    for (const reply of f.held.splice(0)) reply();
    await f.flush();
    assert.equal(f.stopped(), 0); assert.equal(replacementStopped, 0);
    assert.equal(f.media.hardwarePollers.get(f.media.endpointID(f.sender)), nextPoll);
    assert.ok(f.media.hardwareTimers.size > 0, "new owner retains its independent timers");
});

test("demand returning during a stuck poll gets an independent deadline", async t => {
    const f = await hungHardwareFixture(t, 20000, "proof");
    t.mock.timers.tick(19900); await f.flush();
    f.sender.getParameters = () => ({ encodings: [{ active: false }] });
    t.mock.timers.tick(100); await f.flush();
    assert.equal(f.stopped(), 0);
    f.sender.getParameters = () => ({ encodings: [{ active: true }] });
    t.mock.timers.tick(300); await f.flush();
    t.mock.timers.tick(10000); await f.flush();
    assert.equal(f.stopped(), 1, "a pending request cannot hide resumed demand");
    assert.equal(f.calls(), 1, "poll serialization still bounds in-flight requests");
});

test("initial software stops capture without awaiting a hung proof revocation", async t => {
    const f = await hardwarePollFixture(t);
    const post = f.media.worker.postMessage.bind(f.media.worker), attempted = [];
    f.media.worker.postMessage = message => {
        if (message.op === "hardwareProof") { attempted.push(message.enabled); return; }
        post(message);
    };
    const pending = f.sample(1, "software");
    for (let i = 0; i < 30; i++) await Promise.resolve();
    assert.equal(f.stopped(), 1, "software-only admission has no capture grace");
    assert.deepEqual(attempted, [false], "no hardware authorization is granted");
    f.media.close(); await pending;
});

test("a positive proof arriving after the deadline cannot revive polling", async t => {
    const f = await hungHardwareFixture(t, 20000, "proof");
    f.evidence("hardware");
    t.mock.timers.tick(19900); await f.flush();
    assert.equal(f.calls(), 1);
    t.mock.timers.tick(100); await f.flush();
    assert.equal(f.stopped(), 1); assert.equal(f.media.hardwareTimers.size, 0);
    for (const reply of f.held.splice(0)) reply();
    await f.flush();
    assert.equal(f.stopped(), 1); assert.equal(f.media.hardwareTimers.size, 0);
    assert.equal(f.media.hardwarePollers.size, 0);
});
