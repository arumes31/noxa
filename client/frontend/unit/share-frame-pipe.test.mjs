import test from 'node:test';
import assert from 'node:assert/strict';
import {createCaptureFramePipe} from '../src/share-frame-pipe.js';
function setup(t, options={}) {
    const original=Object.getOwnPropertyDescriptor(globalThis,'Worker'); let worker, errors=0, layouts=[];
    class FakeWorker {
        constructor(url, config) {this.url=url;this.config=config;this.messages=[];this.terminations=0;worker=this;}
        postMessage(message, transfer) {this.messages.push({message,transfer});if(message.op==='init'&&options.ready!==false)queueMicrotask(()=>this.receive({op:'ready'}));}
        receive(data){this.onmessage({data});}
        terminate(){this.terminations++;}
    }
    Object.defineProperty(globalThis,'Worker',{value:FakeWorker,configurable:true});
    t.after(()=>{if(original)Object.defineProperty(globalThis,'Worker',original);else delete globalThis.Worker;});
    const readable={},writable={},region={x:0,y:0,width:1,height:1};
    const pipe=createCaptureFramePipe(readable,writable,{region,onLayout:l=>layouts.push(l),onError:()=>errors++});
    t.after(()=>pipe.close());
    return {pipe,worker,readable,writable,region,layouts,get errors(){return errors;}};
}
test('capture endpoints transfer once and no per-frame messages are needed',async t=>{
    const {pipe,worker,readable,writable,region,layouts}=setup(t);
    assert.equal(worker.config.type,'module');assert.ok(worker.url.pathname.endsWith('/share-frame.worker.js'));
    assert.deepEqual(worker.messages[0],{message:{op:'init',readable,writable,region},transfer:[readable,writable]});
    const constraint={width:{max:1920},height:{max:1080}};let complete=false;
    const applied=pipe.setOutput(constraint).then(()=>{complete=true;});await Promise.resolve();await Promise.resolve();
    assert.equal(complete,false);const message=worker.messages.at(-1).message;assert.equal(message.op,'output');assert.deepEqual(message.output,constraint);
    worker.receive({op:'applied',seq:message.seq+1});await Promise.resolve();assert.equal(complete,false);
    worker.receive({op:'layout',layout:{displayWidth:1920,displayHeight:1080}});
    worker.receive({op:'applied',seq:message.seq});await applied;assert.equal(complete,true);assert.equal(layouts.length,1);assert.equal(worker.messages.length,2);
});
test('worker failure rejects pending constraints, ends capture once and ignores late messages',async t=>{
    const f=setup(t);const pending=f.pipe.setOutput({width:1280});await Promise.resolve();await Promise.resolve();
    const rejected=assert.rejects(pending,{name:'AbortError'});f.worker.onerror();await rejected;
    f.worker.onerror();f.worker.receive({op:'layout',layout:{displayWidth:9999}});f.pipe.close();
    assert.equal(f.errors,1);assert.equal(f.worker.terminations,1);assert.deepEqual(f.layouts,[]);
    await assert.rejects(f.pipe.setOutput({}),{name:'AbortError'});
});
test('closing capture during worker startup rejects waiting constraints',async t=>{
    const f=setup(t,{ready:false});const pending=f.pipe.setOutput({});const rejected=assert.rejects(pending,{name:'AbortError'});f.pipe.close();await rejected;
    f.worker.receive({op:'ready'});assert.equal(f.errors,0);assert.equal(f.worker.terminations,1);
});
test('a stalled worker has a bounded startup deadline',async t=>{
    t.mock.timers.enable({apis:['setTimeout']});const f=setup(t,{ready:false});const pending=f.pipe.setOutput({});const rejected=assert.rejects(pending,{name:'AbortError'});
    for(let i=0;i<20;i++)t.mock.timers.tick(250);await rejected;assert.equal(f.errors,1);assert.equal(f.worker.terminations,1);
});
test('a stalled constraint acknowledgement ends capture instead of silently using stale bounds',async t=>{
    t.mock.timers.enable({apis:['setTimeout']});const f=setup(t);const pending=f.pipe.setOutput({width:320});await Promise.resolve();await Promise.resolve();const rejected=assert.rejects(pending,{name:'AbortError'});
    for(let i=0;i<20;i++)t.mock.timers.tick(250);await rejected;assert.equal(f.errors,1);assert.equal(f.worker.terminations,1);
});
for (const phase of ['startup','output']) test(`queued ${phase} acknowledgement survives an overdue UI timer`,async t=>{
    let clock=0;t.mock.method(performance,'now',()=>clock);t.mock.timers.enable({apis:['setTimeout']});
    const f=setup(t,{ready:phase!=='startup'});let done=false;
    const pending=f.pipe.setOutput({width:320}).then(()=>{done=true;});await Promise.resolve();await Promise.resolve();
    clock=6500;t.mock.timers.tick(250);
    assert.equal(f.worker.terminations,0,'a blocked UI must not expire a working worker');
    if(phase==='startup'){f.worker.receive({op:'ready'});await Promise.resolve();await Promise.resolve();}
    const request=f.worker.messages.find(x=>x.message.op==='output').message;
    f.worker.receive({op:'applied',seq:request.seq});await pending;
    assert.equal(done,true);assert.equal(f.errors,0);
    t.mock.timers.tick(10000);assert.equal(f.worker.terminations,0,'cancelled timers cannot end the capture later');
});

test('an unresponsive worker still expires after a blocked UI becomes responsive',async t=>{
    let clock=0;t.mock.method(performance,'now',()=>clock);t.mock.timers.enable({apis:['setTimeout']});
    const f=setup(t,{ready:false});const pending=f.pipe.setOutput({});const rejected=assert.rejects(pending,{name:'AbortError'});
    clock=6500;t.mock.timers.tick(250);assert.equal(f.errors,0);
    for(let i=0;i<20;i++){clock+=250;t.mock.timers.tick(250);}
    await rejected;assert.equal(f.errors,1);assert.equal(f.worker.terminations,1);
});
test('repeated UI stalls cannot retain an unresponsive worker past the hard ceiling',async t=>{
    let clock=0;t.mock.method(performance,'now',()=>clock);t.mock.timers.enable({apis:['setTimeout']});
    const f=setup(t,{ready:false});const pending=f.pipe.setOutput({});const rejected=assert.rejects(pending,{name:'AbortError'});
    for(let i=0;i<29;i++){clock+=1000;t.mock.timers.tick(250);}
    assert.equal(f.errors,0);clock=30000;t.mock.timers.tick(250);
    await rejected;assert.equal(f.errors,1);assert.equal(f.worker.terminations,1);
});
