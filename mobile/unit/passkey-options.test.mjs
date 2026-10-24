import test from "node:test";
import assert from "node:assert/strict";
import nacl from "tweetnacl";
import {generateIdentity, fromBase64, utf8} from "../src/core/crypto.js";
import {registerPasskey, linkWithPasskey} from "../src/core/device.js";

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


test("passkey login commits both new device keys before authentication", async () => {
 const requests = [];
 const identity = {edPublic:new Uint8Array(32).fill(1), xPublic:new Uint8Array(32).fill(2)};
 const conn = { request:async (_type, request) => { requests.push(request); return {webauthn:{publicKey:{challenge:"AQ"}}}; } };
 await linkWithPasskey(conn, identity, {get:async()=>({toJSON:()=>({type:"public-key"})})});
 assert.equal(requests[0].action,"passkey_login_begin");
 assert.equal(requests[0].ed25519_public_key, requests[1].ed25519_public_key);
 assert.equal(requests[0].x25519_public_key, requests[1].x25519_public_key);
});


test("registration signs the verified credential key and account with the linked identity", async t => {
 const previous = Object.getOwnPropertyDescriptor(globalThis, "location");
 Object.defineProperty(globalThis,"location",{value:{origin:"https://example.test"},configurable:true});
 t.after(()=>{if(previous)Object.defineProperty(globalThis,"location",previous);else delete globalThis.location;});
 const identity = generateIdentity();
 const session = {identity, uniqueID:"account", deviceID:"phone"};
 const requests = [];
 const conn = {request:async (_type, request) => {
  requests.push(request);
  return request.action === "passkey_register_begin" ? {
   webauthn:{publicKey:{challenge:"AQ",user:{id:"Ag"},rp:{id:"example.test"}}},
   dm_certificate:{principal:"account",issuer:"phone",rp_id:"example.test",chain:[]},
  } : {passkeys:[]};
 }};
 const credentials = {create:async()=>({rawId:new Uint8Array([1,2]),response:{getPublicKey:()=>new Uint8Array([3,4])},toJSON:()=>({type:"public-key"})})};
 await registerPasskey(conn,"test",credentials,session);
 const cert = requests[1].dm_certificate;
 assert.equal(cert.credential_id, "AQI");
 const transcript = ["noxa-passkey-delegation-v1","account","phone","AQI","AwQ=","example.test","https://example.test"].join("\n");
 assert.equal(nacl.sign.detached.verify(utf8(transcript),fromBase64(cert.signature),identity.edPublic),true);
});
