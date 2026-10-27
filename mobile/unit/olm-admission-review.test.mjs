import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {expectedBinding, sealPayload, openPayload} from '../src/core/olm-admission.js';
test('shared Unicode binding vectors',()=>{
 for(const v of JSON.parse(readFileSync(new URL('../../internal/e2ee/admission/testdata/binding-unicode.json',import.meta.url),'utf8'))) assert.equal(expectedBinding({scope:'direct',sender:v.sender,peer:v.peer},{}).conversation,v.conversation);
});
test('opaque body and strict payload context',()=>{
 const m={},t={};const body={'User-1':{'é':1}};
 const p=sealPayload(m,t,body);assert.deepEqual(openPayload(m,t,p),body);
 assert.throws(()=>openPayload(m,t,p.replace('"context":','"body":null,"context":')));
 assert.throws(()=>openPayload(m,t,p.replace('"version":','"version":"bad","version":')));
 assert.throws(()=>openPayload(m,t,p.replace('"context":','"extra":0,"context":')));
 assert.throws(()=>sealPayload(m,t,'x'.repeat(65536)));
 const deep = '['.repeat(5000)+'0'+']'.repeat(5000);
 const deepPayload = p.replace(JSON.stringify(body), deep);
 assert.doesNotThrow(()=>openPayload(m,t,deepPayload));
});
