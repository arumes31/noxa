import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import vm from 'node:vm';
import { ChatSession } from '../src/core/chat-session.js';
import { generateIdentity } from '../src/core/crypto.js';
import { Msg } from '../src/core/frame.js';

class FakeConnection extends EventTarget {
  open = true;
  on(type, fn) { const f = e => fn(e.detail); this.addEventListener(String(type), f); return () => this.removeEventListener(String(type), f); }
  deliver(type, detail) { this.dispatchEvent(new CustomEvent(String(type), {detail})); }
  close() { this.open=false; }
}

for (const phase of ['identity validation', 'metadata update']) {
 for (const supersede of [false,true]) {
 test(supersede ? `superseded connection during ${phase} closes only its own session` : `offline DM emitted during ${phase} must reach the receiver`, async () => {
 const conn = new FakeConnection(), connections=[conn];
 let opens=0,checks=0;
 const holdOnce=async()=>{if(++checks===1){entered();await blocked;}};
 let release, entered;
 const blocked = new Promise(r => {release=r});
 const atUpdate = new Promise(r => {entered=r});
 const seen = [];
 class DirectMessages extends EventTarget {
   constructor(session) { super(); this.ready=Promise.resolve(); session.addEventListener('direct', e=>seen.push(e.detail.client_msg_id)); }
   async importHistory() {} close() {}
 }
 class Empty extends EventTarget { async refresh() {} close() {} }
 const none=()=>{};
 const sources = {
  './core/chat-session.js': {ChatSession},
  './core/direct.js': {DirectMessages}, './core/calls.js': {Calls:Empty}, './core/groups.js':{Groups:Empty}, './core/voice.js':{Voice:Empty},
  './core/connection.js':{Connection:{open:async()=>{if(opens++===0)return conn;const next=new FakeConnection();connections.push(next);return next}}}, './core/crypto.js':{generateIdentity},
  './core/device.js':{signIn:async()=>({unique_id:'alice',device_id:'phone',client_id:'c1'})},
  './core/identity-store.js':{assertIdentityFence:async()=>{if(phase==='identity validation')await holdOnce()},beginIdentity:none,clearIdentity:none,loadIdentity:none,saveIdentity:none,updateMeta:async()=>{if(phase==='metadata update')await holdOnce()}},
  './core/outbox.js':{enqueue:none,flush:async()=>{},showPending:async()=>{},waitingCount:async()=>0},
  './core/push.js':{announceRead:none,closeNotifications:none,reportVisibility:none,setBadge:none,syncPush:async()=>({})},
  './core/settings.js':{get:()=>[]},'./core/servers.js':{addServer:()=>[]},
  './notifier.js':{notifier:{install(){},decide(){return {count:true}},read(){}}},
  './core/conversation-prefs.js':{activityShared:()=>false,isMuted:()=>false,syncMuted:()=>{},watchCallLog:()=>{},resetCallLog:()=>{}},
  './core/saved.js':{SavedMessages:class extends EventTarget {}},
 };
 const context=vm.createContext({EventTarget,CustomEvent,setTimeout,clearTimeout,clearInterval,console});
 const mod=new vm.SourceTextModule(await fs.readFile(new URL('../src/app.js',import.meta.url),'utf8'),{context});
 await mod.link(spec=>new vm.SyntheticModule(Object.keys(sources[spec]),function(){for(const [k,v] of Object.entries(sources[spec]))this.setExport(k,v)},{context}));
 await mod.evaluate();
 const connecting=mod.namespace.connect();
 await atUpdate;
 if(supersede) {
   const newer=await mod.namespace.connect();
   release();
   await assert.rejects(connecting,e=>e.code==='fenced');
   assert.equal(mod.namespace.app.session,newer);
   assert.equal(conn.open,false);
   assert.equal(connections[1].open,true);
   return;
 }
 conn.deliver(Msg.Event,{type:'chat',data:{direct:true,offline:true,client_msg_id:'spooled-during-startup'}});
 release();
 await connecting;
 conn.deliver(Msg.Event,{type:'chat',data:{direct:true,client_msg_id:'live-after-startup'}});
 assert.deepEqual(seen,['spooled-during-startup','live-after-startup']);
 });
 }
}

test('TURN renewal retries after one transient request failure', async () => {
 const timers = new Map(); let id=0; let requests=0;
 const realSet=globalThis.setTimeout,realClear=globalThis.clearTimeout;
 globalThis.setTimeout=fn=>{timers.set(++id,fn);return id};
 globalThis.clearTimeout=i=>timers.delete(i);
 try {
  const conn=new FakeConnection();
  conn.request=async()=>{requests++; if(requests===2)throw new Error('temporary unavailable');return {server_time:Date.now(),refresh_at:Date.now()+5000,ice_servers:[]}};
  const session=new ChatSession(conn,{}, {capabilities:['managed-relay-v1']});
  await session.relayServers();
  session.onRelayRenewal(()=>{});
  const fire=async()=>{const [i,fn]=timers.entries().next().value;timers.delete(i);await fn()};
  await fire(); // scheduled renewal fails
  await fire(); // promised 30-second retry
  assert.equal(requests,3);
  assert.equal(timers.size,1);
  session.close();
  assert.equal(timers.size,0);
 } finally {globalThis.setTimeout=realSet;globalThis.clearTimeout=realClear}
});


for (const existingGuest of [false, true]) {
 test(`failed guest login preserves saved identity (guest=${existingGuest})`, async () => {
  const savedIdentity = { id: "saved" }, freshIdentity = { id: "fresh" };
  const savedMeta = existingGuest ? { guest: true, nickname: "Existing guest" } : { linked: true, deviceID: "linked-phone" };
  const writes = [];
  let attempted;
  const none = () => {};
  const sources = {
   './core/chat-session.js': {ChatSession},
   './core/direct.js': {DirectMessages: class {}}, './core/calls.js': {Calls:class {}}, './core/groups.js': {Groups:class {}}, './core/voice.js': {Voice:class {}},
   './core/connection.js': {Connection:{open:async()=>({close:none})}}, './core/crypto.js': {generateIdentity:()=>freshIdentity},
   './core/device.js': {signIn:async(conn,identity)=>{attempted=identity;throw new Error("invalid server password")}},
   './core/identity-store.js': {assertIdentityFence: none, beginIdentity: ()=>({generation:1,active:true}), clearIdentity:()=>writes.push("clear"),loadIdentity:none,saveIdentity:()=>writes.push("save"),updateMeta:()=>writes.push("update")},
   './core/outbox.js': {enqueue:none,flush:none,showPending:none,waitingCount:none},
   './core/push.js': {announceRead:none,closeNotifications:none,reportVisibility:none,setBadge:none,syncPush:none},
   './core/settings.js':{get:()=>[]},'./core/servers.js':{addServer:()=>[]},
   './notifier.js': {notifier:{install(){},decide(){return {count:true}},read(){}}},
   './core/conversation-prefs.js':{activityShared:()=>false,isMuted:()=>false,syncMuted:()=>{},watchCallLog:()=>{},resetCallLog:()=>{}},
   './core/saved.js':{SavedMessages:class extends EventTarget {}},
  };
  const context=vm.createContext({EventTarget,CustomEvent,setTimeout,clearTimeout,clearInterval,console});
  const mod=new vm.SourceTextModule(await fs.readFile(new URL('../src/app.js',import.meta.url),'utf8'),{context});
  await mod.link(spec=>new vm.SyntheticModule(Object.keys(sources[spec]),function(){for(const [k,v] of Object.entries(sources[spec]))this.setExport(k,v)},{context}));
  await mod.evaluate();
  mod.namespace.app.identity=savedIdentity;
  mod.namespace.app.identityFence={generation:1,active:true};
  mod.namespace.app.meta=savedMeta;
  await assert.rejects(mod.namespace.connect({guest:{nickname:"Guest",serverPassword:"wrong"}}), /invalid server password/);
  assert.equal(mod.namespace.app.identity,savedIdentity);
  assert.equal(mod.namespace.app.meta,savedMeta);
  assert.equal(attempted,existingGuest?savedIdentity:freshIdentity);
  assert.deepEqual(writes,[]);
 });
}

test('guest snapshot received while saving identity subscribes to channels', async () => {
 const conn = new FakeConnection();
 conn.request=async(type,body)=>{seen.push(...body.channel_ids);return {channel_ids:body.channel_ids}};
 let release, entered;
 const blocked = new Promise(r => {release=r});
 const atUpdate = new Promise(r => {entered=r});
 const seen = [];
 class DirectMessages extends EventTarget {
   constructor(session) { super(); this.ready=Promise.resolve();  }
   async importHistory() {} close() {}
 }
 class Empty extends EventTarget { async refresh() {} close() {} }
 const none=()=>{};
 const sources = {
  './core/chat-session.js': {ChatSession},
  './core/direct.js': {DirectMessages}, './core/calls.js': {Calls:Empty}, './core/groups.js':{Groups:Empty}, './core/voice.js':{Voice:Empty},
  './core/connection.js':{Connection:{open:async()=>conn}}, './core/crypto.js':{generateIdentity},
  './core/device.js':{signIn:async()=>({unique_id:'alice',device_id:'phone',client_id:'c1'})},
  './core/identity-store.js':{assertIdentityFence:none,beginIdentity:none,clearIdentity:none,loadIdentity:none,saveIdentity:async()=>{entered();await blocked},updateMeta:none},
  './core/outbox.js':{enqueue:none,flush:async()=>{},showPending:async()=>{},waitingCount:async()=>0},
  './core/push.js':{announceRead:none,closeNotifications:none,reportVisibility:none,setBadge:none,syncPush:async()=>({})},
  './core/settings.js':{get:()=>[]},'./core/servers.js':{addServer:()=>[]},
  './notifier.js':{notifier:{install(){},decide(){return {count:true}},read(){}}},
  './core/conversation-prefs.js':{activityShared:()=>false,isMuted:()=>false,syncMuted:()=>{},watchCallLog:()=>{},resetCallLog:()=>{}},
  './core/saved.js':{SavedMessages:class extends EventTarget {}},
 };
 const context=vm.createContext({EventTarget,CustomEvent,setTimeout,clearTimeout,clearInterval,console});
 const mod=new vm.SourceTextModule(await fs.readFile(new URL('../src/app.js',import.meta.url),'utf8'),{context});
 await mod.link(spec=>new vm.SyntheticModule(Object.keys(sources[spec]),function(){for(const [k,v] of Object.entries(sources[spec]))this.setExport(k,v)},{context}));
 await mod.evaluate();
 const connecting=mod.namespace.connect({guest:{nickname:'Guest',serverPassword:''}});
 await atUpdate;
 conn.deliver(Msg.Snapshot,{root_channels:[{ChannelID:1,Name:'Lobby',SystemKind:'home'},{ChannelID:2,Name:'Public1'}]});
 release();
 await connecting;
 assert.deepEqual(seen,[2]);
});


test('foreground and restored-page events probe an apparently online session', async () => {
 const none=()=>{};
 const sources={
  './core/chat-session.js':{ChatSession},'./core/direct.js':{DirectMessages:class{}},
  './core/calls.js':{Calls:class{}},'./core/groups.js':{Groups:class{}},'./core/voice.js':{Voice:class{}},
  './core/connection.js':{Connection:class{}},'./core/crypto.js':{generateIdentity},'./core/device.js':{signIn:none},
  './core/identity-store.js':{assertIdentityFence:none,beginIdentity:none,clearIdentity:none,loadIdentity:none,saveIdentity:none,updateMeta:none},
  './core/outbox.js':{enqueue:none,flush:none,showPending:none,waitingCount:none},
  './core/push.js':{announceRead:none,closeNotifications:none,reportVisibility:none,setBadge:none,syncPush:none},
  './core/settings.js':{get:()=>[]},'./core/servers.js':{addServer:()=>[]},
  './notifier.js':{notifier:{install(){},decide(){return {count:true}},read(){}}},
  './core/conversation-prefs.js':{activityShared:()=>false,isMuted:()=>false,syncMuted:()=>{},watchCallLog:()=>{},resetCallLog:()=>{}},
  './core/saved.js':{SavedMessages:class extends EventTarget {}},
 };
 const window=new EventTarget(),document=Object.assign(new EventTarget(),{visibilityState:'hidden'});
 const context=vm.createContext({EventTarget,CustomEvent,setTimeout,clearTimeout,clearInterval,console,window,document});
 const mod=new vm.SourceTextModule(await fs.readFile(new URL('../src/app.js',import.meta.url),'utf8'),{context});
 await mod.link(spec=>new vm.SyntheticModule(Object.keys(sources[spec]),function(){for(const [k,v] of Object.entries(sources[spec]))this.setExport(k,v)},{context}));
 await mod.evaluate();
 let probes=0;
 mod.namespace.app.identity={};mod.namespace.app.session={conn:{probe:async()=>{probes++}}};
 document.dispatchEvent(new Event('visibilitychange'));assert.equal(probes,0);
 document.visibilityState='visible';document.dispatchEvent(new Event('visibilitychange'));assert.equal(probes,1);
 window.dispatchEvent(Object.assign(new Event('pageshow'),{persisted:true}));assert.equal(probes,2);
 window.dispatchEvent(new Event('online'));assert.equal(probes,3);
});


test('staged session bounds replay and discards it on rejection', () => {
 const acceptedConnection=new FakeConnection();
 const accepted=new ChatSession(acceptedConnection,generateIdentity(),{},{deferIncoming:true});
 const flushed=[];
 accepted.addEventListener('direct',e=>flushed.push(e.detail.client_msg_id));
 for(let i=0;i<1024;i++) acceptedConnection.deliver(Msg.Event,{type:'chat',data:{direct:true,client_msg_id:String(i)}});
 assert.equal(flushed.length,0);
 accepted.activateIncoming();
 accepted.activateIncoming();
 assert.deepEqual(flushed,Array.from({length:1024},(_,i)=>String(i)));
 accepted.close();
 const conn = new FakeConnection();
 const session = new ChatSession(conn, generateIdentity(), {}, {deferIncoming:true});
 const seen=[];
 session.addEventListener('direct', e=>seen.push(e.detail));
 for(let i=0;i<1025;i++) conn.deliver(Msg.Event,{type:'chat',data:{direct:true,client_msg_id:String(i)}});
 assert.throws(()=>session.activateIncoming(),/replay limit exceeded/);
 assert.deepEqual(seen,[]);
 const rejected = new ChatSession(conn, generateIdentity(), {}, {deferIncoming:true});
 rejected.addEventListener('direct', e=>seen.push(e.detail));
 conn.deliver(Msg.Event,{type:'chat',data:{direct:true,client_msg_id:'stale'}});
 rejected.close();
 rejected.activateIncoming();
 assert.deepEqual(seen,[]);
});


test('staged session closes before retaining more than 8 MiB of aggregate UTF-8 payloads', () => {
 const conn=new FakeConnection();
 const session=new ChatSession(conn,generateIdentity(),{},{deferIncoming:true});
 const seen=[];
 session.addEventListener('direct',e=>seen.push(e.detail));
 // Each frame is a little over 512 KiB in UTF-8 (below the 1 MiB ingress
 // frame limit). JS string length alone would incorrectly permit all 16.
 const payload={type:'chat',data:{direct:true,text:'\u00e9'.repeat(256*1024)}};
 for(let i=0;i<15;i++) conn.deliver(Msg.Event,payload);
 assert.equal(conn.open,true);
 conn.deliver(Msg.Event,payload);
 assert.equal(conn.open,false);
 assert.throws(()=>session.activateIncoming(),/replay limit exceeded/);
 assert.deepEqual(seen,[]);
});
