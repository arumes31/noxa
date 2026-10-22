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
    let restarts=0, failures=0, restartHook=async()=>{};
    const app={GetRelayLeaseForTab:async()=>({generation}),GetICEServersForTab:async()=>[{urls:["turn:relay"],username:String(generation),credential:"test"}]};
    const stop=maintainRelay(pc,{app,tabID:"a",current:()=>live,restart:async()=>{restarts++;if(fail)throw Error("transient");await restartHook();},onFailure:()=>{failures++;}});
    return {pc,app,calls,stop,flush:()=>new Promise(resolve=>setImmediate(resolve)),tick:async(delta=16000)=>{time+=delta;tick();await new Promise(resolve=>setImmediate(resolve));},generation:n=>{generation=n;},port:n=>{port=n;},retire:()=>{live=false;},fail:value=>{fail=value;},restarts:()=>restarts,failures:()=>failures,restartHook:hook=>{restartHook=hook;}};
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


test("rapid polling and wake events let ICE nominate before retrying", async t => {
    const f = fixture(t); await f.flush();
    assert.equal(f.restarts(), 1);
    await f.tick(100);
    window.dispatchEvent(new Event("online")); await f.flush();
    document.dispatchEvent(new Event("visibilitychange")); await f.flush();
    assert.equal(f.restarts(), 1);
    await f.tick(900);
    assert.equal(f.restarts(), 2, "an unchanged path must still get a bounded retry");
    await f.tick(100);
    window.dispatchEvent(new Event("online")); await f.flush();
    assert.equal(f.restarts(), 2, "wake must also respect the retry's nomination window");
    f.port(40002); await f.tick(1000);
    assert.equal(f.restarts(), 2);
});


test("wake during pending negotiation cannot erase its completion deadline", async t => {
    const f = fixture(t); await f.flush();
    let finish;
    f.restartHook(() => new Promise(resolve => { finish = resolve; }));
    await f.tick(1000); assert.equal(f.restarts(), 2);
    await f.tick(2000);
    window.dispatchEvent(new Event("online")); await f.flush();
    f.restartHook(async () => {}); finish(); await f.flush();
    await f.tick(100);
    window.dispatchEvent(new Event("online")); await f.flush();
    assert.equal(f.restarts(), 2);
    await f.tick(900); assert.equal(f.restarts(), 3);
});

test("exhausted retries wait for final nomination deadline before failure", async t => {
    const f = fixture(t); await f.flush();
    for (let attempt = 0; attempt < 4; attempt++) await f.tick();
    assert.equal(f.restarts(), 5);
    window.dispatchEvent(new Event("online")); await f.flush();
    assert.equal(f.failures(), 0);
    await f.tick(29999); assert.equal(f.failures(), 0);
    await f.tick(1); assert.equal(f.failures(), 1);
    assert.equal(f.restarts(), 5);
});
