import test from "node:test";
import assert from "node:assert/strict";
import {Voice} from "../src/core/voice.js";
// Without an encrypted-media adapter (no server support) the join fails
// before changing channel or opening the microphone.
test("missing server support refuses before channel entry and permits another join",async t=>{
 const oldNavigator=Object.getOwnPropertyDescriptor(globalThis,"navigator");const oldRTC=Object.getOwnPropertyDescriptor(globalThis,"RTCPeerConnection");let stopped=0;const channels=[];
 Object.defineProperty(globalThis,"navigator",{configurable:true,value:{mediaDevices:{getUserMedia:async()=>({getTracks:()=>[{stop(){stopped++;}}]})}}});
 Object.defineProperty(globalThis,"RTCPeerConnection",{configurable:true,value:class{constructor(){throw new Error("RTC unavailable");}}});
 t.after(()=>{for(const [key,old]of [["navigator",oldNavigator],["RTCPeerConnection",oldRTC]]){if(old)Object.defineProperty(globalThis,key,old);else delete globalThis[key];}});
 const session=Object.assign(new EventTarget(),{relayServers:async()=>[],conn:{open:true,on:()=>()=>{},request:async(_t,r)=>{channels.push(r.channel_id);return {channel_id:r.channel_id};}}});
 const voice=new Voice(session);
 for(let i=0;i<2;i++){await assert.rejects(voice.join(1),/server_outdated/);assert.equal(voice.active,false);assert.equal(voice.mic,null);}
 assert.deepEqual(channels,[]);assert.equal(stopped,0);voice.close();
});


for (const action of ["leave", "close"]) {
 test(`cancelled voice ${action} stops a late microphone and never starts RTC`, async t => {
  const oldNavigator = Object.getOwnPropertyDescriptor(globalThis, "navigator");
  const oldRTC = Object.getOwnPropertyDescriptor(globalThis, "RTCPeerConnection");
  let resolveMicrophone, entered, stopped = 0, rtc = 0;
  const started = new Promise(resolve => { entered = resolve; });
  const capture = new Promise(resolve => { resolveMicrophone = resolve; });
  Object.defineProperty(globalThis, "navigator", {configurable:true, value:{mediaDevices:{getUserMedia:() => { entered(); return capture; }}}});
  Object.defineProperty(globalThis, "RTCPeerConnection", {configurable:true, value:class { constructor() { rtc++; throw new Error("unexpected late RTC"); } }});
  t.after(() => { for (const [key, old] of [["navigator", oldNavigator], ["RTCPeerConnection", oldRTC]]) { if (old) Object.defineProperty(globalThis, key, old); else delete globalThis[key]; } });
  const channels = [];
  const session = Object.assign(new EventTarget(), {relayServers:async()=>[], conn:{open:true,on:()=>()=>{},request:async(_type, data)=>{channels.push(data.channel_id);return {channel_id:data.channel_id};}}});
  const voice = new Voice(session, null, {media:{status:async()=>({supported:true,principal:"alice",device:"phone",clientID:"self"})}});
  const joining = voice.join(1);
  await started;
  await voice[action]();
  // Cancellation must release the Join UI even while the permission prompt remains open.
  assert.equal(await joining, false);
  resolveMicrophone({getTracks:()=>[{stop(){stopped++;}}]});
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(stopped, 1);
  assert.equal(rtc, 0);
  assert.equal(voice.mic, null);
  assert.equal(voice.active, false);
  if (action === "leave") { assert.deepEqual(channels, [1, 0]); voice.close(); }
 });
}


test("superseding a join releases its microphone while relay lookup is pending", async t => {
 const oldNavigator = Object.getOwnPropertyDescriptor(globalThis, "navigator");
 const stopped = [0, 0]; let capture = 0; let relayReady;
 let atRelay = new Promise(resolve => { relayReady = resolve; });
 Object.defineProperty(globalThis, "navigator", {configurable:true, value:{mediaDevices:{getUserMedia:async() => {
  const index = capture++; return {getTracks:()=>[{stop(){stopped[index]++;}}]};
 }}}});
 t.after(() => { if(oldNavigator) Object.defineProperty(globalThis, "navigator", oldNavigator); else delete globalThis.navigator; });
 const session = Object.assign(new EventTarget(), {relayServers:() => { relayReady(); return new Promise(()=>{}); }, conn:{open:true,on:()=>()=>{},request:async(_type,data)=>({channel_id:data.channel_id})}});
 const voice = new Voice(session, null, {media:{status:async()=>({supported:true,principal:"alice",device:"phone",clientID:"self"})}});
 const first = voice.join(1); await atRelay;
 atRelay = new Promise(resolve => { relayReady = resolve; });
 const second = voice.join(2); await atRelay;
 assert.equal(await first, false);
 assert.deepEqual(stopped, [1, 0]);
 await voice.leave();
 assert.equal(await second, false);
 assert.deepEqual(stopped, [1, 1]);
 assert.equal(voice.mic, null); voice.close();
});

test("cancelled echo join does not report an active echo test", async () => {
 const session = Object.assign(new EventTarget(), {serverInfo:async()=>({echo_channel_id:6}),conn:{on:()=>()=>{}}});
 const voice = new Voice(session); voice.join = async () => false;
 assert.equal(await voice.startEchoTest(), false);
 assert.equal(voice.echo, null); voice.close();
});


test("rejected channel switch preserves the current call and microphone", async () => {
 const channels = []; let closed = 0, stopped = 0;
 const session = Object.assign(new EventTarget(), {conn:{open:true,on:()=>()=>{},request:async(_type, data)=>{
  channels.push(data.channel_id); throw new Error("wrong password");
 }}});
 const voice = new Voice(session);
 const pc = {close(){closed++;}};
 const mic = {getTracks:()=>[{stop(){stopped++;}}]};
 voice.channelID = 1; voice.pc = pc; voice.mic = mic;
 await assert.rejects(voice.join(2, "incorrect"), /wrong password/);
 assert.deepEqual(channels, [2]);
 assert.equal(voice.channelID, 1); assert.equal(voice.active, true);
 assert.equal(voice.pc, pc); assert.equal(voice.mic, mic);
 assert.equal(closed, 0); assert.equal(stopped, 0);
 voice.close();
});

for (const reason of ["link_required", "binding_changed", "secure_unavailable"]) {
 test(`voice preflight ${reason} never changes the channel or opens the microphone`, async t => {
  const oldNavigator = Object.getOwnPropertyDescriptor(globalThis, "navigator");
  let micCalls = 0; const channels = [];
  Object.defineProperty(globalThis, "navigator", {configurable:true, value:{mediaDevices:{getUserMedia:async() => {micCalls++; throw new Error("unexpected microphone");}}}});
  t.after(() => {if(oldNavigator)Object.defineProperty(globalThis,"navigator",oldNavigator);else delete globalThis.navigator;});
  const session = Object.assign(new EventTarget(), {relayServers:async()=>[], conn:{open:true,on:()=>()=>{},request:async(_type,data)=>{channels.push(data.channel_id);return {channel_id:data.channel_id};}}});
  const voice = new Voice(session, null, {media:{status:async()=>({supported:true,principal:"alice",device:"phone",clientID:"self",reason})}});
  await assert.rejects(voice.join(7), error => error.reason === reason);
  assert.deepEqual(channels, [], "a rejected encrypted session must not briefly enter and leave the channel");
  assert.equal(micCalls, 0); assert.equal(voice.active, false); voice.close();
 });
}

for (const action of ["leave", "close"]) {
 test(`cancelling voice ${action} releases pending trust preflight`, async () => {
  let statusReady, resolveStatus; const entered = new Promise(resolve => {statusReady = resolve;});
  const pending = new Promise(resolve => {resolveStatus = resolve;}); const channels = [];
  const session = Object.assign(new EventTarget(), {conn:{open:true,on:()=>()=>{},request:async(_type,data)=>{channels.push(data.channel_id);return {channel_id:data.channel_id};}}});
  const voice = new Voice(session,null,{media:{status:()=>{statusReady();return pending;}}});
  const joining = voice.join(7); await entered; await voice[action](); assert.equal(await joining,false);
  resolveStatus({supported:true,principal:"alice",device:"phone",clientID:"self"}); await Promise.resolve();
  assert.equal(channels.includes(7),false);assert.equal(voice.active,false);voice.close();
 });
}

test("leaving immediately after successful preflight never sends a stale channel join", async () => {
 const channels = [];
 const session = Object.assign(new EventTarget(), {conn:{open:true,on:()=>()=>{},request:async(_type,data)=>{channels.push(data.channel_id);return {channel_id:data.channel_id};}}});
 const voice = new Voice(session,null,{media:{status:()=>({supported:true,principal:"alice",device:"phone",clientID:"self"})}});
 const joining = voice.join(7); let leaving;
 queueMicrotask(() => queueMicrotask(() => {leaving=voice.leave();}));
 assert.equal(await joining,false);await leaving;
 assert.equal(channels.includes(7),false,"cancellation between preflight and join must not move the server-side member");
 assert.equal(voice.active,false);voice.close();
});

test("encryption is checked again after channel entry and a changed status rolls back safely", async t => {
 const previous = Object.getOwnPropertyDescriptor(globalThis,"navigator");let stopped=0,calls=0;const channels=[];
 Object.defineProperty(globalThis,"navigator",{configurable:true,value:{mediaDevices:{getUserMedia:async()=>({getTracks:()=>[{stop(){stopped++;}}]})}}});
 t.after(()=>{if(previous)Object.defineProperty(globalThis,"navigator",previous);else delete globalThis.navigator;});
 const session=Object.assign(new EventTarget(),{relayServers:async()=>[],conn:{open:true,on:()=>()=>{},request:async(_type,data)=>{channels.push(data.channel_id);return{channel_id:data.channel_id};}}});
 const voice=new Voice(session,null,{media:{status:async()=>++calls===1?{supported:true,principal:"alice",device:"phone",clientID:"self"}:{supported:true,reason:"binding_changed"}}});
 await assert.rejects(voice.join(7),error=>error.reason==="binding_changed");
 assert.equal(calls,2);assert.deepEqual(channels,[7,0]);assert.equal(stopped,1);assert.equal(voice.active,false);voice.close();
});
