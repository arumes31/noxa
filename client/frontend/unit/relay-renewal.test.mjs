import { test } from "node:test";
import assert from "node:assert/strict";
import { maintainRelay } from "../src/relay-renewal.js";

function fixture(t) {
    const originals = { window: globalThis.window, document: globalThis.document, setInterval, clearInterval, now: Date.now };
    let tick, time=100000, live=true, fail=false, port=40000, generation=1;
    globalThis.window = new EventTarget(); globalThis.document = new EventTarget();
    globalThis.setInterval = fn => { tick=fn; return 1; }; globalThis.clearInterval=()=>{}; Date.now=()=>time;
    t.after(()=>{stop();globalThis.window=originals.window;globalThis.document=originals.document;globalThis.setInterval=originals.setInterval;globalThis.clearInterval=originals.clearInterval;Date.now=originals.now;});
    const pc = new EventTarget(), calls=[];
    Object.assign(pc,{signalingState:"stable",iceConnectionState:"connected",remoteDescription:{type:"answer"},getConfiguration:()=>({iceServers:[]}),setConfiguration:c=>calls.push(c),getStats:async()=>new Map([["transport",{type:"transport",selectedCandidatePairId:"pair"}],["pair",{localCandidateId:"local"}],["local",{candidateType:"relay",address:"1.2.3.4",port}]])});
    let restarts=0;
    const app={GetRelayLeaseForTab:async()=>({generation}),GetICEServersForTab:async()=>[{urls:["turn:relay"],username:String(generation),credential:"test"}]};
    const stop=maintainRelay(pc,{app,tabID:"a",current:()=>live,restart:async()=>{restarts++;if(fail)throw Error("transient");}});
    return {pc,app,calls,stop,flush:()=>new Promise(resolve=>setImmediate(resolve)),tick:async()=>{time+=16000;tick();await new Promise(resolve=>setImmediate(resolve));},generation:n=>{generation=n;},port:n=>{port=n;},retire:()=>{live=false;},fail:value=>{fail=value;},restarts:()=>restarts};
}
test("renews configuration and confirms a new selected endpoint without closing tracks",async t=>{
    const f=fixture(t);await f.flush();assert.equal(f.restarts(),1);
    f.port(40001);await f.tick();assert.equal(f.restarts(),1);
    f.generation(2);await f.tick();assert.equal(f.calls.at(-1).iceServers[0].username,"2");assert.equal(f.restarts(),2);
    f.generation(1);await f.tick();assert.equal(f.calls.length,2);
});
test("failed restart is retried without rejecting the renewal owner permanently",async t=>{
    const f=fixture(t);f.fail(true);await f.flush();assert.equal(f.restarts(),1);
    f.fail(false);await f.tick();assert.equal(f.restarts(),2);
    f.port(40002);await f.tick();assert.equal(f.restarts(),2);
});
test("retired peer cannot publish a delayed credential response",async t=>{
    const f=fixture(t);await f.flush();f.port(40001);await f.tick();
    let resolve;f.app.GetRelayLeaseForTab=()=>new Promise(r=>{resolve=r;});
    const pending=f.tick();await f.flush();f.retire();resolve({generation:3});await pending;
    assert.equal(f.calls.length,1);assert.equal(f.restarts(),1);
});
test("missing candidate stats is not proof of successful migration",async t=>{
    const f=fixture(t);await f.flush();f.pc.getStats=async()=>new Map();await f.tick();assert.equal(f.restarts(),2);
});
