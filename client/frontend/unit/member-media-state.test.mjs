import {test} from "node:test";
import assert from "node:assert/strict";
import {memberMediaState as state} from "../src/member-media-state.js";
test("transport and keys have separate readiness",()=>{
 assert.equal(state({connected:false,status:{epoch:1},self:true}),"connecting");
 assert.equal(state({connected:true,status:{epoch:0},self:true}),"keying");
 assert.equal(state({connected:true,status:{epoch:1},self:true}),"ready");
});
test("both directions must have current-epoch key material",()=>{
 const status={participants:[{clientID:"peer",sent:true,received:false}]};
 assert.equal(state({connected:true,status,clientID:"peer"}),"keying");
 status.participants[0].received=true;
 assert.equal(state({connected:true,status,clientID:"peer"}),"ready");
 assert.equal(state({connected:true,status,clientID:"other"}),"keying");
 status.participants[0].failed=true;
 assert.equal(state({connected:true,status,clientID:"peer"}),"retrying");
 status.state="paused";
 assert.equal(state({connected:true,status,clientID:"peer"}),"paused");
});
