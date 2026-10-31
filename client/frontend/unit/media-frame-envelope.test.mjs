import {test} from "node:test";
import assert from "node:assert/strict";
import {mediaFrameEnvelope,h264ClearHeader} from "../src/media-frame-envelope.js";
const frame=(bytes,mime="video/h264")=>({data:Uint8Array.from(bytes).buffer,getMetadata:()=>({mimeType:mime})});
test("H264 3-byte headers are normalized without stripping plaintext slice bytes",()=>{
 const f=frame([0,0,1,0x67,66,0xe0,31,0,0,1,0x65,0x88,0,0,3,1]);
 assert.deepEqual(h264ClearHeader(new Uint8Array(f.data)),{length:12,normalized:14});
 assert.equal(mediaFrameEnvelope(f).size,18);
});
test("only the H264 ciphertext tail is unescaped for trailer and length validation",()=>{
 const f=frame([0,0,0,1,0x65,0x88,0,0,3,1,0,0,3,3,12,2]);
 const e=mediaFrameEnvelope(f,true);
 assert.deepEqual([...e.tail],[0,0,1,0,0,3,12,2]);
 assert.equal(e.size,14);
 assert.equal(new Uint8Array(f.data).length,16,"input frame is never changed by inspection");
});
test("malformed, missing and unsupported H264 headers fail closed",()=>{
 for(const bytes of [[],[0x65,0x88,1],[0,0,1,0xe5,0x88],[0,0,1,0x62,0x88],[0,0,1,0x65]])assert.equal(mediaFrameEnvelope(frame(bytes)),null);
});
test("VP8 and Opus keep their exact wire length and bytes",()=>{
 for(const mime of ["video/vp8","audio/opus"]){const f=frame([0,0,3,1,12,0],mime);assert.equal(mediaFrameEnvelope(f,true).size,6);assert.deepEqual([...mediaFrameEnvelope(f,true).tail],[0,0,3,1,12,0]);}
});


test("Intel temporal prefix NALs are retained, spatial and mismatched prefixes refused",()=>{
 const prefix=[0x6e,0xc0,0x80,0x07,0xc0];
 const bytes=[0,0,1,...prefix,0,0,1,0x65,0x88,1,2,3];
 assert.deepEqual(h264ClearHeader(Uint8Array.from(bytes)),{length:13,normalized:15});
 for(const change of [[5,0x81],[6,0x17],[4,0x80]]) {
  const bad=Uint8Array.from(bytes);bad[change[0]]=change[1];assert.equal(h264ClearHeader(bad),null);
 }
});
