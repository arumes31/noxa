import test from "node:test";
import assert from "node:assert/strict";
import {Groups} from "../src/core/groups.js";
test("group creation and invitation use fresh revision and skip an existing invite",async()=>{
 let group={id:"g1",revision:2,members:[{unique_id:"self"}]};const requests=[];
 const session=Object.assign(new EventTarget(),{uniqueID:"self",conn:{request:async(type,request)=>{
  requests.push(request);
  if(request.action==="invite"){assert.equal(request.revision,2);group={...group,revision:3,members:[...group.members,{unique_id:request.target,pending:true}]};return {action:"invite"};}
  return {action:request.action,conversations:[group]};
 }}});
 const groups=new Groups(session,{});let updates=0;groups.addEventListener("list",()=>updates++);
 const created=await groups.create(" Raid ");assert.equal(created.id,"g1");assert.equal(requests[0].name,"Raid");
 await groups.invite("g1","peer");await groups.invite("g1","peer");
 assert.equal(requests.filter(r=>r.action==="invite").length,1);assert.equal(groups.list.get("g1").revision,3);assert.ok(updates>0);
 groups.close();
});

import {ProtocolError} from "../src/core/connection.js";
test("bulk invitations retry only explicit rate rejection with a fresh revision",async()=>{
 let revision=1, invites=0;const seen=[];
 const session=Object.assign(new EventTarget(),{uniqueID:"self",conn:{request:async(_t,r)=>{
  if(r.action==="get")return {conversations:[{id:"g1",revision,members:[]}]};
  seen.push(r.revision);if(++invites===1){revision=2;throw new ProtocolError({message:"group rate limit exceeded",retry_after_ms:1});}
  return {};
 }}});
 const groups=new Groups(session,{});await groups.invite("g1","peer");assert.deepEqual(seen,[1,2]);groups.close();
});
test("an aborted rate wait never resends a group mutation",async()=>{
 const controller=new AbortController();let calls=0;
 const session=Object.assign(new EventTarget(),{uniqueID:"self",conn:{request:async()=>{
  calls++;queueMicrotask(()=>controller.abort());throw new ProtocolError({message:"group rate limit exceeded",retry_after_ms:1000});
 }}});
 const groups=new Groups(session,{});await assert.rejects(groups.create("Raid",{signal:controller.signal}),{name:"AbortError"});assert.equal(calls,1);groups.close();
});
test("an uncertain network failure never retries group creation",async()=>{
 let calls=0;const session=Object.assign(new EventTarget(),{uniqueID:"self",conn:{request:async()=>{calls++;throw new Error("connection lost");}}});
 const groups=new Groups(session,{});await assert.rejects(groups.create("Raid"),/connection lost/);assert.equal(calls,1);groups.close();
});
