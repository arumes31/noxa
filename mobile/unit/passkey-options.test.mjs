import test from "node:test";
import assert from "node:assert/strict";
import {registerPasskey} from "../src/core/device.js";

test("passkey registration never injects an unrequested PRF extension", async()=>{
 const requests=[];
 const conn={request:async(type,request)=>{
  requests.push(request);
  if(request.action==="passkey_register_begin")return {webauthn:{publicKey:{challenge:"AQ",user:{id:"Ag",name:"member",displayName:"Member"},rp:{id:"example.test",name:"noXa"},pubKeyCredParams:[{type:"public-key",alg:-7}],extensions:{credProps:true}}}};
  return {passkeys:[{passkey_id:"saved"}]};
 }};
 const credentials={create:async({publicKey})=>{
  assert.deepEqual(publicKey.extensions,{credProps:true});
  assert.equal(publicKey.extensions.prf,undefined);
  return {toJSON:()=>({id:"saved",type:"public-key",clientExtensionResults:{credProps:{rk:true}}})};
 }};
 const result=await registerPasskey(conn,"iPhone",credentials);
 assert.equal(result[0].passkey_id,"saved");
 assert.deepEqual(requests[1].webauthn.clientExtensionResults,{credProps:{rk:true}});
});
