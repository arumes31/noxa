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
