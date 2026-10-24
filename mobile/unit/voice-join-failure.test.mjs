import test from "node:test";
import assert from "node:assert/strict";
import {Voice} from "../src/core/voice.js";
test("failed media setup rolls back the channel and permits another join",async t=>{
 const oldNavigator=Object.getOwnPropertyDescriptor(globalThis,"navigator");const oldRTC=Object.getOwnPropertyDescriptor(globalThis,"RTCPeerConnection");let stopped=0;const channels=[];
 Object.defineProperty(globalThis,"navigator",{configurable:true,value:{mediaDevices:{getUserMedia:async()=>({getTracks:()=>[{stop(){stopped++;}}]})}}});
 Object.defineProperty(globalThis,"RTCPeerConnection",{configurable:true,value:class{constructor(){throw new Error("RTC unavailable");}}});
 t.after(()=>{for(const [key,old]of [["navigator",oldNavigator],["RTCPeerConnection",oldRTC]]){if(old)Object.defineProperty(globalThis,key,old);else delete globalThis[key];}});
 const session=Object.assign(new EventTarget(),{relayServers:async()=>[],conn:{open:true,on:()=>()=>{},request:async(_t,r)=>{channels.push(r.channel_id);return {channel_id:r.channel_id};}}});
 const voice=new Voice(session);
 for(let i=0;i<2;i++){await assert.rejects(voice.join(1),/RTC unavailable/);assert.equal(voice.active,false);assert.equal(voice.mic,null);}
 assert.deepEqual(channels,[1,0,1,0]);assert.equal(stopped,2);voice.close();
});
