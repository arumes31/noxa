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
  close() {}
}

test('offline DM emitted while updateMeta awaits must reach the receiver', async () => {
 const conn = new FakeConnection();
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
  './core/connection.js':{Connection:{open:async()=>conn}}, './core/crypto.js':{generateIdentity},
  './core/device.js':{signIn:async()=>({unique_id:'alice',device_id:'phone',client_id:'c1'})},
  './core/identity-store.js':{clearIdentity:none,loadIdentity:none,saveIdentity:none,updateMeta:async()=>{entered();await blocked}},
  './core/outbox.js':{enqueue:none,flush:async()=>{},showPending:async()=>{},waitingCount:async()=>0},
  './core/push.js':{announceRead:none,closeNotifications:none,reportVisibility:none,setBadge:none,syncPush:async()=>({})},
  './core/saved.js':{SavedMessages:class extends EventTarget {}},
 };
 const context=vm.createContext({EventTarget,CustomEvent,setTimeout,clearTimeout,clearInterval,console});
 const mod=new vm.SourceTextModule(await fs.readFile(new URL('../src/app.js',import.meta.url),'utf8'),{context});
 await mod.link(spec=>new vm.SyntheticModule(Object.keys(sources[spec]),function(){for(const [k,v] of Object.entries(sources[spec]))this.setExport(k,v)},{context}));
 await mod.evaluate();
 const connecting=mod.namespace.connect();
 await atUpdate;
 conn.deliver(Msg.Event,{type:'chat',data:{direct:true,offline:true,client_msg_id:'spooled-during-startup'}});
 release();
 await connecting;
 conn.deliver(Msg.Event,{type:'chat',data:{direct:true,client_msg_id:'live-after-startup'}});
 assert.deepEqual(seen,['spooled-during-startup','live-after-startup']);
});

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
   './core/identity-store.js': {clearIdentity:()=>writes.push("clear"),loadIdentity:none,saveIdentity:()=>writes.push("save"),updateMeta:()=>writes.push("update")},
   './core/outbox.js': {enqueue:none,flush:none,showPending:none,waitingCount:none},
   './core/push.js': {announceRead:none,closeNotifications:none,reportVisibility:none,setBadge:none,syncPush:none},
   './core/saved.js':{SavedMessages:class extends EventTarget {}},
  };
  const context=vm.createContext({EventTarget,CustomEvent,setTimeout,clearTimeout,clearInterval,console});
  const mod=new vm.SourceTextModule(await fs.readFile(new URL('../src/app.js',import.meta.url),'utf8'),{context});
  await mod.link(spec=>new vm.SyntheticModule(Object.keys(sources[spec]),function(){for(const [k,v] of Object.entries(sources[spec]))this.setExport(k,v)},{context}));
  await mod.evaluate();
  mod.namespace.app.identity=savedIdentity;
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
  './core/identity-store.js':{clearIdentity:none,loadIdentity:none,saveIdentity:async()=>{entered();await blocked},updateMeta:none},
  './core/outbox.js':{enqueue:none,flush:async()=>{},showPending:async()=>{},waitingCount:async()=>0},
  './core/push.js':{announceRead:none,closeNotifications:none,reportVisibility:none,setBadge:none,syncPush:async()=>({})},
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
  './core/identity-store.js':{clearIdentity:none,loadIdentity:none,saveIdentity:none,updateMeta:none},
  './core/outbox.js':{enqueue:none,flush:none,showPending:none,waitingCount:none},
  './core/push.js':{announceRead:none,closeNotifications:none,reportVisibility:none,setBadge:none,syncPush:none},
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
