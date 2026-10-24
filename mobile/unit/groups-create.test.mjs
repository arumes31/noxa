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
