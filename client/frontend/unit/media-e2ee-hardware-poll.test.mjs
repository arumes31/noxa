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
        assert.equal(calls,2);assert.equal(stopped,1);
    }finally{f.media.close();}
});
