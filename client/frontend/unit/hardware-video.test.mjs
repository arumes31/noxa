import {test} from "node:test";
import assert from "node:assert/strict";
import {hardwareVideoEvidence as evidence} from "../src/hardware-video.js";
const rows=(extra)=>new Map([["v",{type:"outbound-rtp",kind:"video",...extra}]]);
test("only actual hardware evidence releases a stream",()=>{
 assert.equal(evidence(rows({powerEfficientEncoder:true,encoderImplementation:"ExternalEncoder"})),"pending");
 assert.equal(evidence(rows({powerEfficientEncoder:false,encoderImplementation:"libvpx"})),"software");
 assert.equal(evidence(rows({encoderImplementation:"ExternalEncoder"})),"pending");
 assert.equal(evidence(rows({powerEfficientEncoder:true,encoderImplementation:"OpenH264"})),"software");
 assert.equal(evidence(rows({powerEfficientEncoder:true,encoderImplementation:"IntelQuickSyncMFT"})),"hardware");
 assert.equal(evidence(new Map()),"pending");
});

test("proof must describe exactly one active encoder of the current capture",()=>{
 const report=new Map([["s",{type:"media-source",trackIdentifier:"current"}],["v",{type:"outbound-rtp",kind:"video",mediaSourceId:"s",powerEfficientEncoder:true,encoderImplementation:"IntelQuickSyncMFT"}]]);
 assert.equal(evidence(report,"current"),"hardware");
 assert.equal(evidence(report,"old"),"pending");
 report.set("second",{...report.get("v")});assert.equal(evidence(report,"current"),"pending","multiple encodings violate the one-upload policy");
 report.delete("second");report.get("v").active=false;assert.equal(evidence(report,"current"),"pending");
});
